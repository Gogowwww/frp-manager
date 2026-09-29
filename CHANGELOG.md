# Changelog

**English** · [Français](CHANGELOG.fr.md)

All notable changes to FRP Manager are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and version numbers follow each other (`0.0.50`, `0.0.51`…): every version
first ships as a pre-release, then becomes the final release with the same
number. Versions older than 0.0.51 are described in the
[GitHub release notes](https://github.com/Gogowwww/frp-manager/releases).

## [Unreleased]

### Added

- Docker images for `arm64` and `armv7` as well as `amd64` (Raspberry Pi,
  ARM servers): built with buildx and QEMU by the release and dev workflows;
  the promotion copies the whole multi-architecture list to `:latest`.

### Changed

- Tidier repository: the panel's code (`app.py`, `install.sh`, `requirements.txt`,
  `templates/`) lives in `panel/`, the security policy, contributing guide and
  code of conduct in `docs/`, the demo in `website/demo/`, the ruff settings in
  `pyproject.toml`. The release archive keeps the same content, so installing
  and updating work as before. The old `build-dev.sh` and `release-docker.sh`
  scripts are removed (replaced by the Forgejo workflows).

## [0.0.53] - 2026-09-28

Pre-release, then final release, on the same day.

frp is no longer updated automatically: you update it from the panel, when you decide.

### Removed

- The frp auto-update is removed (`frp-autoupdate.py`: daily cron job, check
  at every panel start, Discord notification). frp is only updated from the
  panel's Updates page. `install.sh` installs frp with `app.py --install-frp`
  (same verified download as the panel), and an existing install removes the
  cron job, the script and its line in the systemd service on its next start.

### Fixed

- On the command line (`app.py --reset-password`, `--install-frp`), messages
  are in French only if the system language is French, like `install.sh`.

## [0.0.52] - 2026-09-28

Pre-release, then final release, on the same day.

The panel is now fully bilingual, and gets its own website.

### Added

- Project website, [frp-manager.gogow.fr](https://frp-manager.gogow.fr): home
  page, documentation (generated from the repository's files) and live demo,
  in English and French (`website/`).
- English versions of SECURITY, CONTRIBUTING, CODE_OF_CONDUCT and this
  changelog (the French ones are now the `.fr.md` files).
- Messages sent by the server (errors, confirmations, frp and panel install
  logs, live log notices, firewall port labels) follow the interface language,
  French or English. `install.sh` and `frp-autoupdate.py` speak the system
  language (French if `LANG` is French, English otherwise).

### Changed

- English is now an official language: the panel follows the browser's
  language (English for any language other than French) and English is no
  longer marked as a preview in Settings.
- The demo is served under `/demo/` of the website. Its panel now has a
  password, like every install since 0.0.51, and logging in brings you back to
  `/demo/` instead of `/demo/index.html`.
- Website screenshots are taken from the demo, in English and French
  (`website/shots.py`).

## [0.0.51] - 2026-09-28

Pre-release, then final release, on the same day.

A security-focused release. Updating from the panel or with `install.sh` is
seamless: the existing password keeps working.

### Security

- Password hashed with argon2id (argon2-cffi), with scrypt as a fallback if
  the module is missing. The old unsalted SHA-256 hash is still accepted and is
  rehashed automatically at the first successful login. Constant-time
  comparisons.
- Mandatory initial setup: without a password, the panel only shows the account
  creation page (12 characters minimum); no other page or API route answers.
- Login attempts limited per IP address: 5 attempts, then a 30 s lockout that
  doubles with each failure (1 h at most).
- CSRF token required on every request that changes something; WebSocket
  origin checked.
- HttpOnly and SameSite=Strict session cookies, Secure over HTTPS
  (`cookie_secure` behind a TLS reverse proxy). Changing the password logs out
  the other sessions.
- Content-Security-Policy (nonce-based scripts), X-Content-Type-Options,
  X-Frame-Options and Referrer-Policy headers; API responses never cached.
- systemd service and Docker container names checked against an allow-list; no
  more `sh -c` command built from data.
- TOML configurations checked (tomllib/tomli) before being written; writing
  confined to frp's configuration directories, symbolic links resolved.
- frp archives extracted without `extractall`: absolute or `..` paths, links,
  non-ELF files and decompression bombs rejected; download and upload sizes
  capped.
- frp archives checked against the SHA-256 published with each frp release;
  third-party mirrors (ghproxy, ghfast, gh-proxy) can be disabled (Settings or
  `FRP_MANAGER_NO_MIRRORS=1`) and are never used without a checksum.
- Panel update checked against the SHA-256 attached to the release
  (`frp-manager.zip.sha256`, published from this version on) and archive
  inspected before extraction.
- Panel configuration file written atomically, readable by root only; Discord
  webhook URL masked in the `frp-autoupdate.py` logs.

### Changed

- A new install listens on `127.0.0.1` instead of `0.0.0.0`. Existing installs
  keep their listening address. `FRP_MANAGER_HOST` takes precedence over
  `bind_host`, and `FRP_MANAGER_PORT` sets the port of a new install.
- The panel now refuses to write an frp configuration outside `/etc/frp`,
  `/usr/local/etc/frp`, `/opt/frp` and `/root/frp`, or an invalid TOML
  configuration (HTTP 400 with the error details).
- 12 characters minimum for any new password (existing shorter passwords stay
  valid).
- README restructured (quick start, security, comparison); details moved to
  `docs/`.

### Added

- `python3 app.py --reset-password` to reset the credentials from the console.
- "Third-party download mirrors" setting (Settings → Network access).
- SECURITY.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md, issue and pull request
  templates.
- pytest tests (login, hash migration, TOML validation, service names,
  archives) and continuous integration: tests, ruff, pip-audit.

[Unreleased]: https://github.com/Gogowwww/frp-manager/compare/v0.0.53...HEAD
[0.0.53]: https://github.com/Gogowwww/frp-manager/compare/v0.0.52...v0.0.53
[0.0.52]: https://github.com/Gogowwww/frp-manager/compare/v0.0.51...v0.0.52
[0.0.51]: https://github.com/Gogowwww/frp-manager/compare/v0.0.50...v0.0.51
