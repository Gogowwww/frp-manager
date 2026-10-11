"""Comptes supplémentaires et permissions (ACL)."""

import pytest

from conftest import PASSWORD, post, saved_config, set_password

OTHER_PASSWORD = "un autre mot de passe solide"


@pytest.fixture
def owner(appmod, client):
    """Client connecté en propriétaire."""
    set_password(appmod)
    assert post(client, "/api/login", {"username": "admin", "password": PASSWORD}).status_code == 200
    return client


@pytest.fixture
def instances(appmod, monkeypatch, tmp_path):
    """Deux instances factices, sans découverte réelle."""
    confs = {}
    for iid in ("frps", "frpc"):
        conf = tmp_path / f"{iid}.toml"
        conf.write_text(f'# {iid}\n')
        confs[iid] = {"type": iid, "source": "systemd", "service": iid, "config": str(conf)}
    monkeypatch.setattr(appmod, "INSTANCES", confs)
    monkeypatch.setattr(appmod, "detect_frp", lambda force=False: dict(confs))
    return confs


def create_user(client, **fields):
    body = {"username": "alice", "password": OTHER_PASSWORD, "role": "viewer", **fields}
    return post(client, "/api/users", body)


def login_as(appmod, username, password=OTHER_PASSWORD):
    c = appmod.app.test_client()
    r = post(c, "/api/login", {"username": username, "password": password})
    assert r.status_code == 200, r.get_json()
    return c


def put(client, url, body):
    from conftest import csrf_of
    return client.put(url, json=body, headers={"X-CSRF-Token": csrf_of(client)})


def delete(client, url):
    from conftest import csrf_of
    return client.delete(url, headers={"X-CSRF-Token": csrf_of(client)})


# ── Permissions ──────────────────────────────────────────────────────────────
def test_implications_and_roles(appmod):
    assert appmod.expand_permissions(["config.write"]) == {"config.write", "config.read", "instances.view"}
    assert appmod.expand_permissions(["*"]) == set(appmod.PERMISSIONS)
    assert "users.manage" not in appmod.user_permissions({"role": "operator"})
    assert appmod.user_permissions({"role": "custom", "permissions": ["nope"]}) == set()


def test_scoped_account_keeps_only_instance_permissions(appmod):
    u = {"role": "custom", "permissions": ["config.write", "firewall.manage"], "instances": ["frpc"]}
    assert appmod.user_permissions(u) == {"config.write", "config.read", "instances.view"}
    assert appmod.user_instances({"role": "admin", "instances": ["frpc"]}) is None
    assert appmod.user_instances({"role": "viewer", "instances": "bogus"}) == set()   # illisible : rien


def test_owner_is_admin(appmod, owner):
    me = owner.get("/api/me").get_json()["user"]
    assert me["owner"] and me["role"] == "admin" and "users.manage" in me["permissions"]


# ── Création et connexion ────────────────────────────────────────────────────
def test_create_and_login_viewer(appmod, owner, instances):
    r = create_user(owner)
    assert r.status_code == 200, r.get_json()
    stored = saved_config(appmod)["users"]
    (rec,) = stored.values()
    assert rec["username"] == "alice" and rec["password_hash"].startswith(("$argon2", "scrypt$"))
    assert "password_hash" not in r.get_json()["user"]

    alice = login_as(appmod, "alice")
    assert alice.get("/api/nicknames").status_code == 200
    assert post(alice, "/api/service/frpc/restart").status_code == 403
    assert alice.get("/api/config/frpc").status_code == 403
    assert alice.get("/api/manager/config").status_code == 403
    assert alice.get("/api/users").status_code == 403


def test_usernames_are_unique_ignoring_case(owner):
    assert create_user(owner, username="ADMIN").status_code == 409
    assert create_user(owner).status_code == 200
    assert create_user(owner, username="Alice").status_code == 409


def test_weak_password_and_unknown_role_refused(owner):
    assert create_user(owner, password="court").status_code == 400
    assert create_user(owner, role="root").status_code == 400
    assert create_user(owner, role="custom", permissions=["tout"]).status_code == 400


def test_wrong_password_for_other_account(appmod, owner):
    create_user(owner)
    c = appmod.app.test_client()
    assert post(c, "/api/login", {"username": "alice", "password": PASSWORD}).status_code == 401


# ── Limitation aux instances ─────────────────────────────────────────────────
def test_instance_scope(appmod, owner, instances):
    appmod.MGR_CFG["nicknames"] = {"frps": "serveur", "frpc": "client"}
    create_user(owner, role="custom", permissions=["config.read", "instances.control"], instances=["frpc"])
    alice = login_as(appmod, "alice")
    assert alice.get("/api/nicknames").get_json()["nicknames"] == {"frpc": "client"}
    assert alice.get("/api/config/frpc").get_json()["content"] == "# frpc\n"
    r = alice.get("/api/config/frps")
    assert r.status_code == 403 and "instance" in r.get_json()["msg"]
    assert post(alice, "/api/nickname/frps", {"nickname": "x"}).status_code == 403
    assert post(alice, "/api/nickname/frpc", {"nickname": "x"}).status_code == 200


# ── Effet immédiat des modifications ─────────────────────────────────────────
def test_disable_logs_out_at_once(appmod, owner):
    uid = create_user(owner).get_json()["user"]["id"]
    alice = login_as(appmod, "alice")
    assert alice.get("/api/me").status_code == 200
    assert put(owner, f"/api/users/{uid}", {"enabled": False}).status_code == 200
    assert alice.get("/api/me").status_code == 401
    assert post(appmod.app.test_client(), "/api/login",
                {"username": "alice", "password": OTHER_PASSWORD}).status_code == 401


def test_role_change_applies_to_open_session(appmod, owner, instances):
    uid = create_user(owner).get_json()["user"]["id"]
    alice = login_as(appmod, "alice")
    assert alice.get("/api/config/frpc").status_code == 403
    put(owner, f"/api/users/{uid}", {"role": "operator"})
    assert alice.get("/api/config/frpc").status_code == 200


def test_admin_password_reset_drops_sessions(appmod, owner):
    uid = create_user(owner).get_json()["user"]["id"]
    alice = login_as(appmod, "alice")
    assert put(owner, f"/api/users/{uid}", {"password": "encore un mot de passe"}).status_code == 200
    assert alice.get("/api/me").status_code == 401
    login_as(appmod, "alice", "encore un mot de passe")


def test_delete_account(appmod, owner):
    uid = create_user(owner).get_json()["user"]["id"]
    alice = login_as(appmod, "alice")
    assert delete(owner, f"/api/users/{uid}").status_code == 200
    assert alice.get("/api/me").status_code == 401
    assert saved_config(appmod)["users"] == {}


# ── Garde-fous ───────────────────────────────────────────────────────────────
def test_owner_and_self_protected(appmod, owner):
    assert put(owner, "/api/users/owner", {"enabled": False}).status_code == 400
    assert delete(owner, "/api/users/owner").status_code == 400
    uid = create_user(owner, role="admin").get_json()["user"]["id"]
    alice = login_as(appmod, "alice")
    assert put(alice, f"/api/users/{uid}", {"role": "viewer"}).status_code == 400
    assert delete(alice, f"/api/users/{uid}").status_code == 400


def test_user_manager_cannot_escalate(appmod, owner):
    create_user(owner, role="custom", permissions=["users.manage", "logs.view"])
    bob = create_user(owner, username="bob", role="viewer").get_json()["user"]["id"]
    alice = login_as(appmod, "alice")
    # Pas de droit qu'alice n'a pas
    assert create_user(alice, username="carol", role="admin").status_code == 403
    assert create_user(alice, username="carol", role="custom", permissions=["settings.manage"]).status_code == 403
    assert put(alice, f"/api/users/{bob}", {"role": "operator"}).status_code == 403
    # Un compte plus puissant (bob est lecteur : ports.view, firewall.view… qu'alice n'a pas)
    assert delete(alice, f"/api/users/{bob}").status_code == 403
    # Mais dans ses propres droits, oui
    assert create_user(alice, username="carol", role="custom", permissions=["logs.view"]).status_code == 200
    # Et jamais la connexion unique
    assert alice.get("/api/oidc").status_code == 403


def test_settings_manager_cannot_take_over_owner(appmod, owner):
    create_user(owner, role="custom", permissions=["settings.manage"])
    alice = login_as(appmod, "alice")
    assert post(alice, "/api/manager/config", {"new_password": "je prends la main ici"}).status_code == 403
    assert post(alice, "/api/manager/config", {"username": "moi"}).status_code == 403
    assert post(alice, "/api/manager/config", {"download_mirrors": False}).status_code == 200


def test_manager_config_hides_accounts(appmod, owner):
    create_user(owner)
    cfg = owner.get("/api/manager/config").get_json()["config"]
    assert "users" not in cfg and "oidc" not in cfg and "password_hash" not in cfg


def test_owner_rename_cannot_collide(owner):
    create_user(owner)
    assert post(owner, "/api/manager/config", {"username": "alice"}).status_code == 409


# ── Mot de passe personnel ───────────────────────────────────────────────────
def test_change_own_password(appmod, owner):
    create_user(owner)
    alice = login_as(appmod, "alice")
    other = login_as(appmod, "alice")
    assert post(alice, "/api/me/password", {"current_password": "faux", "new_password": "x" * 12}).status_code == 403
    r = post(alice, "/api/me/password", {"current_password": OTHER_PASSWORD, "new_password": "nouveau mot de passe"})
    assert r.status_code == 200
    assert alice.get("/api/me").status_code == 200        # session courante gardée
    assert other.get("/api/me").status_code == 401        # les autres non
    login_as(appmod, "alice", "nouveau mot de passe")


def test_boot_carries_permissions(appmod, owner, instances):
    create_user(owner, role="custom", permissions=["logs.view"], instances=["frps"])
    alice = login_as(appmod, "alice")
    html = alice.get("/").get_data(as_text=True)
    assert '"permissions": ["instances.view", "logs.view"]' in html
    assert '"frpc"' not in html.split('id="boot"')[1].split("</script>")[0]
