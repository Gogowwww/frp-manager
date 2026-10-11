"""Connexion unique OpenID Connect : fournisseur simulé (découverte, /token, userinfo)."""

import base64
import json
import time
from urllib.parse import parse_qs, urlsplit

import pytest

from conftest import PASSWORD, post, saved_config, set_password

ISSUER = "https://idp.example.com/realms/test"


def jwt(claims):
    enc = lambda d: base64.urlsafe_b64encode(json.dumps(d).encode()).rstrip(b"=").decode()  # noqa: E731
    return f"{enc({'alg': 'RS256'})}.{enc(claims)}.c2lnbmF0dXJl"


class Resp:
    def __init__(self, data, status=200):
        self._data, self.status_code = data, status

    def json(self):
        return self._data

    def raise_for_status(self):
        if self.status_code >= 400:
            raise appmod_req.HTTPError(f"HTTP {self.status_code}")


appmod_req = None


class FakeIdP:
    """Fournisseur : renvoie un jeton d'identité dont on choisit les revendications."""

    def __init__(self, appmod, monkeypatch):
        global appmod_req
        appmod_req = appmod.req
        self.claims = {}
        self.userinfo = None
        self.token_requests = []
        monkeypatch.setattr(appmod.req, "get", self.get)
        monkeypatch.setattr(appmod.req, "post", self.post)

    def get(self, url, **kw):
        if url == ISSUER + "/.well-known/openid-configuration":
            return Resp({"issuer": ISSUER, "authorization_endpoint": ISSUER + "/auth",
                         "token_endpoint": ISSUER + "/token", "userinfo_endpoint": ISSUER + "/userinfo",
                         "token_endpoint_auth_methods_supported": ["client_secret_basic"]})
        if url == ISSUER + "/userinfo" and self.userinfo is not None:
            return Resp(self.userinfo)
        return Resp({}, 404)

    def post(self, url, data=None, auth=None, **kw):
        assert url == ISSUER + "/token"
        self.token_requests.append({"data": data, "auth": auth})
        return Resp({"access_token": "at", "id_token": jwt(self.claims)})


@pytest.fixture
def idp(appmod, monkeypatch):
    set_password(appmod)
    appmod._oidc_meta.clear()
    appmod.MGR_CFG["oidc"] = {"enabled": True, "issuer": ISSUER, "client_id": "frpm", "client_secret": "s3cret"}
    return FakeIdP(appmod, monkeypatch)


def start(client):
    """Ouvre le flux → paramètres envoyés au fournisseur."""
    r = client.get("/auth/oidc/login")
    assert r.status_code == 302, r.data
    q = {k: v[0] for k, v in parse_qs(urlsplit(r.headers["Location"]).query).items()}
    assert r.headers["Location"].startswith(ISSUER + "/auth?")
    return q


def finish(client, idp, q, **claims):
    base = {"iss": ISSUER, "aud": "frpm", "sub": "u-1", "exp": time.time() + 300, "nonce": q["nonce"]}
    idp.claims = {**base, **claims}
    return client.get(f"/auth/oidc/callback?code=abc&state={q['state']}")


def error_of(r):
    return parse_qs(urlsplit(r.headers.get("Location", "")).query).get("error", [None])[0]


def test_authorization_request(appmod, client, idp):
    q = start(client)
    assert q["response_type"] == "code" and q["client_id"] == "frpm"
    assert q["code_challenge_method"] == "S256" and q["scope"].split()[0] == "openid"
    assert q["redirect_uri"].endswith("/auth/oidc/callback")


def test_unknown_account_refused_without_auto_create(appmod, client, idp):
    r = finish(client, idp, start(client))
    assert error_of(r) == "no_account"
    assert client.get("/api/me").status_code == 401


def test_auto_create_and_sign_in(appmod, client, idp):
    appmod.MGR_CFG["oidc"]["auto_create"] = True
    r = finish(client, idp, start(client), preferred_username="admin", email="a@corp.io", email_verified=True)
    assert r.status_code == 200 and b"refresh" in r.data
    me = client.get("/api/me").get_json()["user"]
    assert me["source"] == "oidc" and me["role"] == "viewer"
    assert me["username"] == "admin-2"                     # « admin » est pris par le propriétaire
    assert "users.manage" not in me["permissions"]
    tok = idp.token_requests[0]
    assert tok["auth"] == ("frpm", "s3cret") and tok["data"]["code_verifier"]
    # Deuxième connexion : même compte
    c2 = appmod.app.test_client()
    assert finish(c2, idp, start(c2)).status_code == 200
    assert len(saved_config(appmod)["users"]) == 1


def test_preprovisioned_account_linked_by_verified_email(appmod, client, idp):
    owner = appmod.app.test_client()
    post(owner, "/api/login", {"username": "admin", "password": PASSWORD})
    r = post(owner, "/api/users", {"username": "bob", "source": "oidc", "email": "bob@corp.io", "role": "operator"})
    assert r.status_code == 200, r.get_json()
    # E-mail non vérifié : pas de liaison
    assert error_of(finish(client, idp, start(client), email="bob@corp.io", email_verified=False)) == "no_account"
    r = finish(client, idp, start(client), email="bob@corp.io", email_verified=True)
    assert r.status_code == 200
    me = client.get("/api/me").get_json()["user"]
    assert me["username"] == "bob" and me["role"] == "operator"
    (rec,) = saved_config(appmod)["users"].values()
    assert rec["oidc"] == {"iss": ISSUER, "sub": "u-1"}


def test_allowed_domains(appmod, client, idp):
    appmod.MGR_CFG["oidc"].update(auto_create=True, allowed_domains=["corp.io"])
    assert error_of(finish(client, idp, start(client), email="x@evil.io", email_verified=True)) == "domain"
    assert error_of(finish(client, idp, start(client), email="x@corp.io")) == "domain"   # non vérifié
    assert finish(client, idp, start(client), email="x@corp.io", email_verified=True).status_code == 200


def test_group_roles(appmod, client, idp):
    appmod.MGR_CFG["oidc"].update(auto_create=True, group_roles={"frp-admins": "admin", "frp-ops": "operator"})
    finish(client, idp, start(client), groups=["staff", "frp-ops"])
    assert client.get("/api/me").get_json()["user"]["role"] == "operator"
    # Les groupes décident à chaque connexion
    c2 = appmod.app.test_client()
    finish(c2, idp, start(c2), groups=["frp-admins"])
    assert c2.get("/api/me").get_json()["user"]["role"] == "admin"
    c3 = appmod.app.test_client()
    finish(c3, idp, start(c3), groups=[])
    assert c3.get("/api/me").get_json()["user"]["role"] == "viewer"


def test_groups_from_userinfo(appmod, client, idp):
    appmod.MGR_CFG["oidc"].update(auto_create=True, group_roles={"frp-ops": "operator"})
    idp.userinfo = {"sub": "u-1", "groups": ["frp-ops"]}
    finish(client, idp, start(client))
    assert client.get("/api/me").get_json()["user"]["role"] == "operator"


def test_userinfo_for_other_subject_ignored(appmod, client, idp):
    appmod.MGR_CFG["oidc"].update(auto_create=True, group_roles={"frp-ops": "operator"})
    idp.userinfo = {"sub": "quelqu-un-d-autre", "groups": ["frp-ops"]}
    finish(client, idp, start(client))
    assert client.get("/api/me").get_json()["user"]["role"] == "viewer"


@pytest.mark.parametrize("claims, code", [
    ({"nonce": "rejoue"}, "claims"),
    ({"aud": "autre-client"}, "claims"),
    ({"iss": "https://faux.example.com"}, "claims"),
    ({"exp": 1}, "claims"),
    ({"sub": ""}, "claims"),
])
def test_bad_id_token(appmod, client, idp, claims, code):
    appmod.MGR_CFG["oidc"]["auto_create"] = True
    assert error_of(finish(client, idp, start(client), **claims)) == code
    assert client.get("/api/me").status_code == 401


def test_state_mismatch_and_missing_cookie(appmod, client, idp):
    q = start(client)
    assert error_of(client.get("/auth/oidc/callback?code=abc&state=autre")) == "state"
    fresh = appmod.app.test_client()
    assert error_of(fresh.get(f"/auth/oidc/callback?code=abc&state={q['state']}")) == "state"


def test_provider_error(appmod, client, idp):
    start(client)
    assert error_of(client.get("/auth/oidc/callback?error=access_denied&state=x")) == "denied"


def test_disabled_account(appmod, client, idp):
    appmod.MGR_CFG["oidc"]["auto_create"] = True
    finish(client, idp, start(client))
    users = appmod.MGR_CFG["users"]
    (uid,) = users
    users[uid]["enabled"] = False
    assert client.get("/api/me").status_code == 401
    c2 = appmod.app.test_client()
    assert error_of(finish(c2, idp, start(c2))) == "disabled_user"


def test_disabled_sso(appmod, client, idp):
    appmod.MGR_CFG["oidc"]["enabled"] = False
    assert error_of(client.get("/auth/oidc/login")) == "disabled"
    assert b"/auth/oidc/login" not in client.get("/login").data


def test_login_page_shows_button_and_error(appmod, client, idp):
    appmod.MGR_CFG["oidc"]["label"] = "Keycloak"
    page = client.get("/login?error=no_account").get_data(as_text=True)
    assert "/auth/oidc/login" in page and "Keycloak" in page and 'data-oidc-error="no_account"' in page
    page = client.get("/login?error=<script>").get_data(as_text=True)
    assert "<script>" not in page.split("data-oidc-error")[1][:30]


# ── Réglages ─────────────────────────────────────────────────────────────────
def test_settings_validation_and_secret(appmod, client, idp):
    post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    got = client.get("/api/oidc").get_json()
    assert got["oidc"]["has_secret"] and "client_secret" not in got["oidc"]
    assert got["redirect_uri"].endswith("/auth/oidc/callback")
    assert post(client, "/api/oidc", {"default_role": "admin"}).status_code == 400
    assert post(client, "/api/oidc", {"issuer": "http://idp.example.com"}).status_code == 400
    assert post(client, "/api/oidc", {"enabled": True, "client_id": ""}).status_code == 400
    assert post(client, "/api/oidc", {"group_roles": {"g": "root"}}).status_code == 400
    r = post(client, "/api/oidc", {"label": "Entreprise", "allowed_domains": "corp.io, Example.org"})
    assert r.status_code == 200
    stored = saved_config(appmod)["oidc"]
    assert stored["client_secret"] == "s3cret"                       # gardé s'il n'est pas ressaisi
    assert stored["allowed_domains"] == ["corp.io", "example.org"]
    assert post(client, "/api/oidc/test", {"issuer": ISSUER}).get_json()["ok"]
