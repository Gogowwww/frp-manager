"""Connexion, configuration initiale, CSRF, limitation des tentatives, en-têtes."""

import hashlib

from conftest import PASSWORD, csrf_of, post, saved_config, set_password


# ── Hachage ──────────────────────────────────────────────────────────────────
def test_hash_is_salted_and_slow(appmod):
    a, b = appmod.hash_password(PASSWORD), appmod.hash_password(PASSWORD)
    assert a != b                                  # sel aléatoire
    assert a.startswith(("$argon2id$", "scrypt$"))
    assert appmod.verify_password(PASSWORD, a) == (True, False)
    assert appmod.verify_password("mauvais mot de passe", a)[0] is False


def test_scrypt_fallback(appmod, monkeypatch):
    monkeypatch.setattr(appmod, "_ARGON2", None)
    stored = appmod.hash_password(PASSWORD)
    assert stored.startswith("scrypt$")
    assert appmod.verify_password(PASSWORD, stored) == (True, False)
    assert appmod.verify_password(PASSWORD + "x", stored) == (False, False)


def test_legacy_sha256_is_accepted_and_flagged(appmod):
    legacy = hashlib.sha256(PASSWORD.encode()).hexdigest()
    assert appmod.verify_password(PASSWORD, legacy) == (True, True)
    assert appmod.verify_password("autre", legacy) == (False, False)


def test_garbage_hash_never_matches(appmod):
    for stored in ("", "abc", "scrypt$1$2", "$argon2id$v=19$cassé"):
        assert appmod.verify_password(PASSWORD, stored)[0] is False


# ── Migration du hash SHA-256 ────────────────────────────────────────────────
def test_login_migrates_legacy_hash(appmod, client):
    legacy = set_password(appmod, legacy_sha256=True)
    r = post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    assert r.status_code == 200 and r.get_json()["ok"]
    stored = saved_config(appmod)["password_hash"]
    assert stored != legacy
    assert stored.startswith(("$argon2id$", "scrypt$"))
    # L'ancien mot de passe marche toujours, avec le nouveau hash
    client2 = appmod.app.test_client()
    assert post(client2, "/api/login", {"username": "admin", "password": PASSWORD}).status_code == 200


def test_failed_login_does_not_touch_legacy_hash(appmod, client):
    legacy = set_password(appmod, legacy_sha256=True)
    assert post(client, "/api/login", {"username": "admin", "password": "mauvais"}).status_code == 401
    assert saved_config(appmod)["password_hash"] == legacy


# ── Connexion ────────────────────────────────────────────────────────────────
def test_login_success_opens_session(appmod, client):
    set_password(appmod)
    assert client.get("/api/status").status_code == 401
    r = post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    assert r.status_code == 200
    with client.session_transaction() as sess:
        assert sess["authenticated"] is True


def test_login_rejects_wrong_username_or_password(appmod, client):
    set_password(appmod)
    assert post(client, "/api/login", {"username": "root", "password": PASSWORD}).status_code == 401
    assert post(client, "/api/login", {"username": "admin", "password": "nope"}).status_code == 401


def test_password_change_logs_out_other_sessions(appmod, client):
    set_password(appmod)
    other = appmod.app.test_client()
    post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    post(other, "/api/login", {"username": "admin", "password": PASSWORD})
    r = post(client, "/api/manager/config", {"new_password": "un tout nouveau mot de passe"})
    assert r.status_code == 200
    assert client.get("/api/manager/config").status_code == 200     # session courante gardée
    assert other.get("/api/manager/config").status_code == 401      # l'autre est déconnectée


def test_csrf_token_survives_password_change(appmod, client):
    set_password(appmod)
    post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    before = csrf_of(client)
    assert post(client, "/api/manager/config", {"new_password": "un tout nouveau mot de passe"}).status_code == 200
    assert csrf_of(client) == before
    r = client.post("/api/nickname/frpc", json={"nickname": "Maison"}, headers={"X-CSRF-Token": before})
    assert r.status_code == 200


def test_new_password_must_be_long_enough(appmod, client):
    set_password(appmod)
    post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    r = post(client, "/api/manager/config", {"new_password": "court"})
    assert r.status_code == 400


# ── Configuration initiale ───────────────────────────────────────────────────
def test_setup_required_without_password(appmod, client):
    r = client.get("/")
    assert r.status_code == 302 and r.headers["Location"].endswith("/setup")
    assert client.get("/login").status_code == 302
    assert client.get("/api/status").status_code == 401
    assert post(client, "/api/login", {"username": "admin", "password": ""}).status_code == 401
    assert client.get("/setup").status_code == 200


def test_setup_enforces_min_length(appmod, client):
    r = post(client, "/api/setup", {"username": "admin", "password": "trop court"})
    assert r.status_code == 400
    assert appmod.needs_setup()


def test_setup_creates_account_once(appmod, client):
    r = post(client, "/api/setup", {"username": "moi", "password": PASSWORD})
    assert r.status_code == 200
    assert not appmod.needs_setup()
    assert saved_config(appmod)["username"] == "moi"
    assert client.get("/api/manager/config").status_code == 200     # connecté d'office
    # Une seconde tentative (autre navigateur) est refusée et la page disparaît
    other = appmod.app.test_client()
    assert post(other, "/api/setup", {"username": "pirate", "password": "x" * 20}).status_code == 409
    assert other.get("/setup").status_code == 302
    assert saved_config(appmod)["username"] == "moi"


# ── CSRF ─────────────────────────────────────────────────────────────────────
def test_post_without_csrf_token_is_rejected(appmod, client):
    set_password(appmod)
    csrf_of(client)
    r = client.post("/api/login", json={"username": "admin", "password": PASSWORD})
    assert r.status_code == 403
    r = client.post("/api/login", json={"username": "admin", "password": PASSWORD},
                    headers={"X-CSRF-Token": "faux"})
    assert r.status_code == 403


def test_pages_embed_csrf_token(appmod, client):
    set_password(appmod)
    body = client.get("/login").get_data(as_text=True)
    with client.session_transaction() as sess:
        assert f'content="{sess["csrf"]}"' in body


# ── Limitation des tentatives ────────────────────────────────────────────────
def test_login_rate_limit(appmod, client):
    set_password(appmod)
    codes = [post(client, "/api/login", {"username": "admin", "password": "mauvais"}).status_code
             for _ in range(appmod.LOGIN_FREE_ATTEMPTS)]
    assert codes[:-1] == [401] * (appmod.LOGIN_FREE_ATTEMPTS - 1)
    assert codes[-1] == 429
    # Même le bon mot de passe est refusé pendant le verrouillage
    r = post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    assert r.status_code == 429 and int(r.headers["Retry-After"]) > 0


def test_lockout_grows(appmod):
    ip, now = "203.0.113.9", 1_000_000.0
    for _ in range(appmod.LOGIN_FREE_ATTEMPTS):
        appmod._login_failed(ip, now)
    first = appmod._login_retry_after(ip, now)
    appmod._login_failed(ip, now)
    assert appmod._login_retry_after(ip, now) > first
    for _ in range(40):
        appmod._login_failed(ip, now)
    assert appmod._login_retry_after(ip, now) <= appmod.LOGIN_LOCK_MAX


# ── En-têtes et cookies ──────────────────────────────────────────────────────
def test_security_headers(appmod, client):
    set_password(appmod)
    r = client.get("/login")
    csp = r.headers["Content-Security-Policy"]
    assert "frame-ancestors 'none'" in csp and "'nonce-" in csp and "unsafe-eval" not in csp
    assert r.headers["X-Content-Type-Options"] == "nosniff"
    assert r.headers["X-Frame-Options"] == "DENY"
    assert r.headers["Referrer-Policy"] == "no-referrer"


def test_session_cookie_flags(appmod, client):
    set_password(appmod)
    appmod.app.config["SESSION_COOKIE_SECURE"] = True
    r = client.get("/login", base_url="https://localhost")
    cookie = r.headers["Set-Cookie"]
    assert "HttpOnly" in cookie and "SameSite=Strict" in cookie and "Secure" in cookie


def test_manager_config_never_exposes_secrets(appmod, client):
    set_password(appmod)
    post(client, "/api/login", {"username": "admin", "password": PASSWORD})
    cfg = client.get("/api/manager/config").get_json()["config"]
    assert "password_hash" not in cfg and "secret_key" not in cfg


def test_config_file_is_private(appmod):
    import os
    import stat
    set_password(appmod)
    if os.name == "posix":
        assert stat.S_IMODE(os.stat(appmod.MGR_CONF_FILE).st_mode) == 0o600
