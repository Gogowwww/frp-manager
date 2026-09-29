"""Tests de FRP Manager : panel/app.py est importé avec une config dans un dossier
temporaire (FRP_MANAGER_CONFIG), jamais celle de /etc/frp-manager."""

import hashlib
import importlib
import json
import os
import sys
import tempfile
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
_CONF_DIR = Path(tempfile.mkdtemp(prefix="frpm-tests-"))
os.environ["FRP_MANAGER_CONFIG"] = str(_CONF_DIR / "frp-manager.json")
sys.path.insert(0, str(ROOT / "panel"))

app_module = importlib.import_module("app")

PASSWORD = "correct horse battery staple"


@pytest.fixture
def appmod(tmp_path, monkeypatch):
    """Module app avec une config neuve, sans mot de passe, et sans
    verrouillage de connexion hérité d'un autre test."""
    conf = tmp_path / "frp-manager.json"
    monkeypatch.setattr(app_module, "MGR_CONF_FILE", conf)
    monkeypatch.setattr(app_module, "MGR_CONF_DIR", tmp_path)
    monkeypatch.setattr(app_module, "MGR_CFG", app_module._default_manager_config())
    monkeypatch.setenv("LANG", "fr_FR.UTF-8")  # messages hors requête : français, quel que soit le poste
    monkeypatch.delenv("LC_ALL", raising=False)
    monkeypatch.delenv("LC_MESSAGES", raising=False)
    app_module._login_failures.clear()
    app_module.app.config.update(TESTING=True, SESSION_COOKIE_SECURE=False)
    return app_module


@pytest.fixture
def client(appmod):
    return appmod.app.test_client()


def csrf_of(client):
    """Jeton CSRF de la session du client (celui que la page pose dans <meta>)."""
    with client.session_transaction() as sess:
        if "csrf" not in sess:
            sess["csrf"] = "jeton-de-test"
        return sess["csrf"]


def post(client, url, body=None):
    return client.post(url, json=body or {}, headers={"X-CSRF-Token": csrf_of(client)})


def set_password(appmod, password=PASSWORD, username="admin", legacy_sha256=False):
    stored = (hashlib.sha256(password.encode()).hexdigest() if legacy_sha256
              else appmod.hash_password(password))
    cfg = {**appmod.MGR_CFG, "username": username, "password_hash": stored}
    appmod.save_manager_config(cfg)
    appmod.MGR_CFG = cfg
    return stored


def saved_config(appmod):
    return json.loads(Path(appmod.MGR_CONF_FILE).read_text())
