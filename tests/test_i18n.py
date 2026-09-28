"""Messages du serveur dans la langue de l'interface (en-tête X-Lang)."""

import threading

from conftest import csrf_of, set_password


def login(client, lang):
    return client.post("/api/login", json={"username": "admin", "password": "mauvais"},
                       headers={"X-CSRF-Token": csrf_of(client), "X-Lang": lang})


def test_messages_follow_x_lang(appmod, client):
    set_password(appmod)
    assert login(client, "fr").get_json()["msg"] == "Identifiants incorrects"
    assert login(client, "en").get_json()["msg"] == "Wrong username or password"


def test_accept_language_fallback(appmod, client):
    set_password(appmod)
    r = client.post("/api/login", json={"username": "admin", "password": "x"},
                    headers={"X-CSRF-Token": csrf_of(client), "Accept-Language": "en-US,en;q=0.9"})
    assert r.get_json()["msg"] == "Wrong username or password"


def test_setup_validation_in_english(appmod, client):
    r = client.post("/api/setup", json={"username": "admin", "password": "court"},
                    headers={"X-CSRF-Token": csrf_of(client), "X-Lang": "en"})
    assert r.status_code == 400 and "at least 12 characters" in r.get_json()["msg"]


def test_invalid_toml_message_in_english(appmod, tmp_path, monkeypatch):
    conf = tmp_path / "frp"
    conf.mkdir()
    monkeypatch.setattr(appmod, "CONFIG_SEARCH_PATHS", [conf])
    with appmod.app.test_request_context(headers={"X-Lang": "en"}):
        ok, msg, code = appmod._write_config_file(conf / "frpc.toml", 'serverAddr = "x\n')
    assert not ok and code == 400 and msg.startswith("Invalid TOML")


def test_thread_keeps_request_language(appmod):
    seen = []
    with appmod.app.test_request_context(headers={"X-Lang": "en"}):
        worker = threading.Thread(target=appmod._in_lang(appmod._lang(), lambda: seen.append(appmod.M("fr", "en"))))
    worker.start()
    worker.join()
    assert seen == ["en"]


def test_firewall_rule_errors_translated(appmod):
    with appmod.app.test_request_context(headers={"X-Lang": "en"}):
        try:
            appmod._fw_normalize_rule({"mode": "block", "ports": "7000", "sources": []}, 0)
        except appmod.RuleError as e:
            assert str(e) == "Rule 1: enter at least one address to block"
        else:
            raise AssertionError("règle acceptée")


def test_port_labels_translated(appmod):
    items = [{"label": "Connexion des clients frpc"}, {"label": "Port « ssh »"}, {"label": "KCP"}]
    with appmod.app.test_request_context(headers={"X-Lang": "en"}):
        assert [i["label"] for i in appmod.with_port_labels(items)] == \
            ["frpc client connections", "Port “ssh”", "KCP"]
    with appmod.app.test_request_context(headers={"X-Lang": "fr"}):
        assert appmod.with_port_labels(items) == items


