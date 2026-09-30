"""Notifications webhook : validation, formats, secrets, événements."""

import hashlib
import hmac
import json

import pytest

from conftest import post, saved_config, set_password

HOOK = {"name": "Alertes", "format": "discord", "url": "https://discord.com/api/webhooks/1/secret-token",
        "events": ["instance.down"]}


class FakeResponse:
    def __init__(self, status=204):
        self.status_code = status


@pytest.fixture
def sent(appmod, monkeypatch):
    calls = []

    def fake_post(url, data=None, headers=None, timeout=None, allow_redirects=None):
        calls.append({"url": url, "body": data, "headers": headers, "allow_redirects": allow_redirects})
        return FakeResponse(204)

    monkeypatch.setattr(appmod.req, "post", fake_post)
    monkeypatch.setattr(appmod.time, "sleep", lambda s: None)
    return calls


def logged_in(appmod, client):
    set_password(appmod)
    post(client, "/api/login", {"username": "admin", "password": "correct horse battery staple"})


def item(event="instance.down", **extra):
    return {"event": event, "level": "error", "title": ("Titre", "Title"), "text": ("Texte", "Text"),
            "subject": "frps", "instance": "frps", "data": {}, "time": "2026-09-30T10:00:00+02:00", **extra}


def test_save_hides_url_and_secret(appmod, client):
    logged_in(appmod, client)
    r = post(client, "/api/webhooks", {"webhooks": [{**HOOK, "secret": "s3cret"}]})
    assert r.status_code == 200
    public = r.get_json()["webhooks"][0]
    assert public["url_hint"] == "https://discord.com/…" and public["has_secret"]
    assert "secret-token" not in r.get_data(as_text=True) and "s3cret" not in r.get_data(as_text=True)
    got = client.get("/api/webhooks").get_data(as_text=True)
    assert "secret-token" not in got and "s3cret" not in got
    assert "webhooks" not in client.get("/api/manager/config").get_json()["config"]
    assert saved_config(appmod)["webhooks"][0]["url"] == HOOK["url"]


def test_blank_url_and_secret_keep_previous(appmod, client):
    logged_in(appmod, client)
    hook_id = post(client, "/api/webhooks", {"webhooks": [{**HOOK, "secret": "s3cret"}]}).get_json()["webhooks"][0]["id"]
    post(client, "/api/webhooks", {"webhooks": [{**HOOK, "id": hook_id, "url": "", "name": "Renommé"}]})
    saved = saved_config(appmod)["webhooks"][0]
    assert saved["id"] == hook_id and saved["name"] == "Renommé"
    assert saved["url"] == HOOK["url"] and saved["secret"] == "s3cret"
    post(client, "/api/webhooks", {"webhooks": [{**HOOK, "id": hook_id, "secret_clear": True}]})
    assert saved_config(appmod)["webhooks"][0]["secret"] == ""


@pytest.mark.parametrize("patch", [
    {"name": ""}, {"url": "ftp://x/y"}, {"url": "https://"}, {"url": "https://a b"}, {"format": "sms"},
    {"events": []}, {"events": ["nimporte.quoi"]}, {"cooldown": -1}, {"cooldown": "abc"},
    {"headers": "pas-un-en-tete"}, {"format": "telegram"}, {"format": "ntfy", "url": "https://ntfy.sh/"},
    {"format": "generic", "template": '{"a": '}, {"format": "generic", "template": "pas du json"},
])
def test_invalid_webhook_refused(appmod, client, patch):
    logged_in(appmod, client)
    r = post(client, "/api/webhooks", {"webhooks": [{**HOOK, **patch}]})
    assert r.status_code == 400 and not r.get_json()["ok"]
    assert "webhooks" not in saved_config(appmod) or saved_config(appmod)["webhooks"] == []


def test_requires_login(appmod, client):
    set_password(appmod)
    assert client.get("/api/webhooks").status_code == 401
    assert post(client, "/api/webhooks/test", {"webhook": HOOK}).status_code == 401


def test_generic_payload_and_signature(appmod, sent):
    hook = appmod._webhook_normalize({"name": "n", "url": "https://example.org/hook", "events": ["*"],
                                      "secret": "cle", "headers": "Authorization: Bearer abc"})
    ok, status, _ = appmod._webhook_send(hook, item())
    assert ok and status == 204
    call = sent[0]
    body = json.loads(call["body"])
    assert body["event"] == "instance.down" and body["title"] == "Titre" and body["instance"] == "frps"
    assert call["headers"]["X-FRPManager-Signature"] == "sha256=" + hmac.new(b"cle", call["body"], hashlib.sha256).hexdigest()
    assert call["headers"]["Authorization"] == "Bearer abc" and call["headers"]["X-FRPManager-Event"] == "instance.down"
    assert call["allow_redirects"] is False


def test_language_per_webhook(appmod, sent):
    hook = appmod._webhook_normalize({"name": "n", "url": "https://example.org/h", "events": ["*"], "lang": "en"})
    appmod._webhook_send(hook, item())
    assert json.loads(sent[0]["body"])["title"] == "Title"


def test_template_escapes_values(appmod, sent):
    hook = appmod._webhook_normalize({"name": "n", "url": "https://example.org/h", "events": ["*"],
                                      "template": '{"content": "{{title}}: {{message}}"}'})
    appmod._webhook_send(hook, item(text=('dit "bonjour"\nligne 2', "x")))
    assert json.loads(sent[0]["body"]) == {"content": 'Titre: dit "bonjour"\nligne 2'}


def test_discord_slack_telegram_ntfy_gotify(appmod, sent):
    def build(fmt, url, **extra):
        hook = appmod._webhook_normalize({"name": "n", "format": fmt, "url": url, "events": ["*"], **extra})
        return appmod._webhook_request(hook, item())

    url, raw, _ = build("discord", "https://discord.com/api/webhooks/1/t")
    assert json.loads(raw)["embeds"][0]["title"] == "Titre" and json.loads(raw)["embeds"][0]["color"] == 0xEF4444
    url, raw, _ = build("slack", "https://hooks.slack.com/services/A/B/C")
    assert json.loads(raw)["attachments"][0]["text"] == "Texte"
    url, raw, _ = build("telegram", "https://api.telegram.org/bot123:ABC", chat_id="-100")
    assert url.endswith("/bot123:ABC/sendMessage") and json.loads(raw)["chat_id"] == "-100"
    url, raw, _ = build("ntfy", "https://ntfy.sh/mon-sujet")
    body = json.loads(raw)
    assert url == "https://ntfy.sh/" and body["topic"] == "mon-sujet" and body["priority"] == 5
    url, raw, _ = build("gotify", "https://gotify.lan/message?token=T")
    assert url == "https://gotify.lan/message?token=T" and json.loads(raw)["priority"] == 8


def test_telegram_escapes_html(appmod):
    hook = appmod._webhook_normalize({"name": "n", "format": "telegram", "url": "https://api.telegram.org/bot1:A",
                                      "chat_id": "1", "events": ["*"]})
    _, raw, _ = appmod._webhook_request(hook, item(text=("<b>x</b> & y", "")))
    assert "&lt;b&gt;x&lt;/b&gt; &amp; y" in json.loads(raw)["text"]


def test_retries_then_gives_up(appmod, monkeypatch):
    attempts = []

    def failing(url, **kw):
        attempts.append(1)
        return FakeResponse(503)

    monkeypatch.setattr(appmod.req, "post", failing)
    monkeypatch.setattr(appmod.time, "sleep", lambda s: None)
    hook = appmod._webhook_normalize({**HOOK})
    assert appmod._webhook_send(hook, item()) == (False, 503, "HTTP 503")
    assert len(attempts) == len(appmod.WEBHOOK_RETRY_DELAYS)


def test_client_error_not_retried(appmod, monkeypatch):
    attempts = []
    monkeypatch.setattr(appmod.req, "post", lambda url, **kw: attempts.append(1) or FakeResponse(404))
    monkeypatch.setattr(appmod.time, "sleep", lambda s: None)
    assert appmod._webhook_send(appmod._webhook_normalize({**HOOK}), item())[:2] == (False, 404)
    assert len(attempts) == 1


def test_matching_filters_events_instances_cooldown(appmod):
    hook = {"id": "a", "events": ["instance.down"], "instances": ["frps"], "cooldown": 60}
    assert not appmod._webhook_matches(hook, item("instance.up"))
    assert not appmod._webhook_matches(hook, item(instance="frpc"))
    assert appmod._webhook_matches(hook, item())
    assert not appmod._webhook_matches(hook, item())                       # dans le délai
    assert appmod._webhook_matches(hook, item(subject="autre"))
    assert appmod._webhook_matches({"id": "b", "events": ["*"]}, item("login.success", instance=None))


def test_test_endpoint(appmod, client, sent):
    logged_in(appmod, client)
    r = post(client, "/api/webhooks/test", {"webhook": HOOK})
    assert r.get_json()["ok"] and len(sent) == 1
    assert client.get("/api/webhooks").get_json()["history"][0]["event"] == "test"
    bad = post(client, "/api/webhooks/test", {"webhook": {**HOOK, "url": "nope"}})
    assert bad.status_code == 400


def test_no_active_webhook_queues_nothing(appmod):
    while not appmod._webhook_queue.empty():
        appmod._webhook_queue.get_nowait()
    appmod.webhook_emit("instance.down", ("a", "a"), ("b", "b"))
    assert appmod._webhook_queue.empty()
    appmod.MGR_CFG = {**appmod.MGR_CFG, "webhooks": [appmod._webhook_normalize({**HOOK})]}
    appmod.webhook_emit("instance.down", ("a", "a"), ("b", "b"))
    assert appmod._webhook_queue.get_nowait()["event"] == "instance.down"
    appmod.webhook_emit("evenement.inconnu", ("a", "a"), ("b", "b"))
    assert appmod._webhook_queue.empty()


def test_instance_change_needs_two_readings(appmod):
    appmod.MGR_CFG = {**appmod.MGR_CFG, "webhooks": [appmod._webhook_normalize({**HOOK, "events": ["*"]})]}
    while not appmod._webhook_queue.empty():
        appmod._webhook_queue.get_nowait()
    seen, pending = {}, {}
    state = lambda running: {"frps": {"binary_found": True, "service": "frps", "status": {"running": running}}}
    for running in (True, False, True, False, False):       # panne d'un seul relevé, puis panne réelle
        appmod._webhook_instance_events(state(running), seen, pending)
    events = []
    while not appmod._webhook_queue.empty():
        events.append(appmod._webhook_queue.get_nowait()["event"])
    assert events == ["instance.down"]


def test_login_lockout_and_success_emit(appmod, client):
    appmod.MGR_CFG = {**appmod.MGR_CFG, "webhooks": [appmod._webhook_normalize({**HOOK, "events": ["*"]})]}
    set_password(appmod)
    appmod.MGR_CFG = {**appmod.MGR_CFG, "webhooks": [appmod._webhook_normalize({**HOOK, "events": ["*"]})]}
    while not appmod._webhook_queue.empty():
        appmod._webhook_queue.get_nowait()
    for _ in range(appmod.LOGIN_FREE_ATTEMPTS):
        post(client, "/api/login", {"username": "admin", "password": "faux"})
    events = []
    while not appmod._webhook_queue.empty():
        events.append(appmod._webhook_queue.get_nowait())
    assert [e["event"] for e in events] == ["login.locked"]
    assert events[0]["data"]["failures"] == appmod.LOGIN_FREE_ATTEMPTS
