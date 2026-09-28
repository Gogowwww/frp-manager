"""Validation des noms, des configs TOML, des chemins et des archives."""

import io
import tarfile
import zipfile

import pytest


# ── Noms de services, conteneurs, instances ─────────────────────────────────
@pytest.mark.parametrize("name", ["frps", "frpc", "frpc2", "frpc-maison", "frps_vps.eu", "frpc@1"])
def test_valid_service_names(appmod, name):
    assert appmod.valid_service_name(name)


@pytest.mark.parametrize("name", [
    "", "sshd", "frp", "frpx", "frpc;reboot", "frpc reboot", "frpc$(id)", "../frpc", "frpc/../../x",
    "-frpc", "frpc\n", "frpc`id`", "frpc|sh", "f" * 100, None, 42,
])
def test_invalid_service_names(appmod, name):
    assert not appmod.valid_service_name(name)


@pytest.mark.parametrize("name,ok", [
    ("frpc", True), ("my-stack_frpc.1", True), ("0123456789ab", True),
    ("../../containers", False), ("a/b", False), ("-x", False), ("x?y=1", False), ("", False),
])
def test_container_names(appmod, name, ok):
    assert appmod.valid_container_name(name) is ok


def test_service_action_refuses_bad_name(appmod, monkeypatch):
    calls = []
    monkeypatch.setattr(appmod, "run_cmd", lambda cmd, **kw: calls.append(cmd) or (True, "", ""))
    ok, msg = appmod.service_action("frpc; rm -rf /", "restart")
    assert not ok and not calls
    ok, _ = appmod.service_action("frpc2", "restart")
    assert ok and calls == [["systemctl", "restart", "frpc2"]]


# ── Configs TOML ─────────────────────────────────────────────────────────────
VALID_TOML = 'serverAddr = "vps.example.net"\nserverPort = 7000\n\n[[proxies]]\nname = "ssh"\ntype = "tcp"\nlocalPort = 22\nremotePort = 6000\n'


def test_valid_toml_accepted(appmod):
    appmod.validate_config_content("/etc/frp/frpc.toml", VALID_TOML)


@pytest.mark.parametrize("content", [
    'serverAddr = "vps.example.net\n',            # guillemet non fermé
    "[[proxies]\nname = 'x'\n",                   # section mal formée
    'serverPort = 7000\nserverPort = 7001\n',     # clé en double
    "serverAddr = \x00\n",
])
def test_invalid_toml_rejected(appmod, content):
    with pytest.raises(ValueError):
        appmod.validate_config_content("/etc/frp/frpc.toml", content)


def test_oversized_config_rejected(appmod):
    with pytest.raises(ValueError):
        appmod.validate_config_content("/etc/frp/frpc.toml", "# " + "x" * appmod.CONFIG_MAX_BYTES)


def test_invalid_toml_never_written(appmod, tmp_path, monkeypatch):
    conf_dir = tmp_path / "frp"
    conf_dir.mkdir()
    target = conf_dir / "frpc.toml"
    target.write_text(VALID_TOML)
    monkeypatch.setattr(appmod, "CONFIG_SEARCH_PATHS", [conf_dir])
    ok, msg, code = appmod._write_config_file(target, 'serverAddr = "cassé\n')
    assert not ok and code == 400 and "TOML invalide" in msg
    assert target.read_text() == VALID_TOML
    ok, _, code = appmod._write_config_file(target, VALID_TOML.replace("7000", "7001"))
    assert ok and code == 200 and "7001" in target.read_text()


# ── Confinement des chemins ──────────────────────────────────────────────────
def test_config_path_confined(appmod, tmp_path, monkeypatch):
    conf_dir = tmp_path / "frp"
    conf_dir.mkdir()
    monkeypatch.setattr(appmod, "CONFIG_SEARCH_PATHS", [conf_dir])
    assert appmod.safe_config_path(conf_dir / "frpc.toml").name == "frpc.toml"
    for bad in (conf_dir / ".." / "frpc.toml", tmp_path / "ailleurs.toml",
                conf_dir / "frpc.sh", "/etc/passwd", conf_dir / "sous" / ".." / ".." / "x.toml"):
        with pytest.raises(ValueError):
            appmod.safe_config_path(bad)


def test_config_symlink_escape_refused(appmod, tmp_path, monkeypatch):
    conf_dir = tmp_path / "frp"
    conf_dir.mkdir()
    outside = tmp_path / "secret.toml"
    outside.write_text("x = 1\n")
    link = conf_dir / "frpc.toml"
    try:
        link.symlink_to(outside)
    except (OSError, NotImplementedError):
        pytest.skip("liens symboliques indisponibles ici")
    monkeypatch.setattr(appmod, "CONFIG_SEARCH_PATHS", [conf_dir])
    with pytest.raises(ValueError):
        appmod.safe_config_path(link)


# ── Archives frp ─────────────────────────────────────────────────────────────
ELF = b"\x7fELF" + b"\x00" * 60


def make_tar(path, members):
    with tarfile.open(path, "w:gz") as tf:
        for name, data, kind in members:
            info = tarfile.TarInfo(name)
            if kind == "sym":
                info.type, info.linkname = tarfile.SYMTYPE, "/etc/shadow"
                tf.addfile(info)
            else:
                info.size = len(data)
                tf.addfile(info, io.BytesIO(data))
    return path


def test_extracts_only_frp_binaries(appmod, tmp_path):
    arc = make_tar(tmp_path / "frp.tar.gz", [
        ("frp_0.71.0_linux_amd64/frps", ELF, "file"),
        ("frp_0.71.0_linux_amd64/frpc", ELF, "file"),
        ("frp_0.71.0_linux_amd64/LICENSE", b"MIT", "file"),
    ])
    out = tmp_path / "out"
    out.mkdir()
    found = appmod.extract_frp_binaries(arc, out)
    assert sorted(found) == ["frpc", "frps"]
    assert sorted(p.name for p in out.iterdir()) == ["frpc", "frps"]


@pytest.mark.parametrize("members", [
    [("../frps", ELF, "file")],
    [("/usr/local/bin/frps", ELF, "file")],
    [("frp/../../frpc", ELF, "file")],
    [("frp/frps", b"", "sym")],
    [("frp/frps", b"#!/bin/sh\nrm -rf /\n", "file")],     # pas un exécutable ELF
])
def test_malicious_archives_rejected(appmod, tmp_path, members):
    arc = make_tar(tmp_path / "evil.tar.gz", members)
    out = tmp_path / "out"
    out.mkdir()
    with pytest.raises(ValueError):
        appmod.extract_frp_binaries(arc, out)
    assert not (tmp_path / "frps").exists() and not (tmp_path / "frpc").exists()


def test_decompression_bomb_rejected(appmod, tmp_path, monkeypatch):
    monkeypatch.setattr(appmod, "ARCHIVE_MAX_UNPACKED", 1024)
    arc = make_tar(tmp_path / "bomb.tar.gz", [("frp/padding", b"\x00" * 4096, "file")])
    with pytest.raises(ValueError):
        appmod.extract_frp_binaries(arc, tmp_path)


def test_panel_zip_checks(appmod, tmp_path):
    good = tmp_path / "good.zip"
    with zipfile.ZipFile(good, "w") as zf:
        zf.writestr("frp-manager/app.py", "print('ok')")
    with zipfile.ZipFile(good) as zf:
        appmod.check_panel_zip(zf)
    for name in ("../app.py", "/etc/cron.d/x", "C:/x.py", "frp-manager\\..\\..\\x"):
        bad = tmp_path / "bad.zip"
        with zipfile.ZipFile(bad, "w") as zf:
            zf.writestr(name, "x")
        with zipfile.ZipFile(bad) as zf, pytest.raises(ValueError):
            appmod.check_panel_zip(zf)


def test_checksum_file_parsing(appmod):
    text = ("84f27e39f11169f7adcef8e8b70c9329de17747b1f14dad9fb95eef5682ea716  frp_0.71.0_linux_amd64.tar.gz\n"
            "garbage line\n")
    sums = appmod.parse_checksums(text)
    assert sums == {"frp_0.71.0_linux_amd64.tar.gz":
                    "84f27e39f11169f7adcef8e8b70c9329de17747b1f14dad9fb95eef5682ea716"}


def test_mirrors_can_be_disabled(appmod, monkeypatch):
    monkeypatch.setitem(appmod.MGR_CFG, "download_mirrors", True)
    assert len(appmod.build_download_mirrors("v0.71.0", "f.tar.gz")) > 1
    monkeypatch.setitem(appmod.MGR_CFG, "download_mirrors", False)
    urls = appmod.build_download_mirrors("v0.71.0", "f.tar.gz")
    assert urls == ["https://github.com/fatedier/frp/releases/download/v0.71.0/f.tar.gz"]


# ── Retrait de frp-autoupdate.py ─────────────────────────────────────────────
def test_legacy_autoupdate_removed(appmod, tmp_path, monkeypatch):
    cron, script, unit = tmp_path / "frp-autoupdate", tmp_path / "frp-autoupdate.py", tmp_path / "frp-manager.service"
    cron.write_text("0 3 * * * root python3 frp-autoupdate.py\n")
    script.write_text("print('x')\n")
    unit.write_text("[Service]\nExecStart=/opt/frp-manager/venv/bin/python3 app.py\n"
                    "ExecStartPost=/bin/bash -c 'python3 /opt/frp-manager/frp-autoupdate.py &'\n")
    monkeypatch.setattr(appmod, "IN_DOCKER", False)
    monkeypatch.setattr(appmod, "LEGACY_AUTOUPDATE_CRON", cron)
    monkeypatch.setattr(appmod, "LEGACY_AUTOUPDATE_SCRIPT", script)
    monkeypatch.setattr(appmod, "PANEL_UNIT_FILE", unit)
    calls = []
    monkeypatch.setattr(appmod, "run_cmd", lambda cmd, **kw: calls.append(cmd) or (True, "", ""))
    appmod.remove_frp_autoupdate()
    assert not cron.exists() and not script.exists()
    assert "frp-autoupdate" not in unit.read_text() and "ExecStart=" in unit.read_text()
    assert calls == [["systemctl", "daemon-reload"]]
    appmod.remove_frp_autoupdate()             # déjà fait : plus rien à changer
    assert calls == [["systemctl", "daemon-reload"]]
