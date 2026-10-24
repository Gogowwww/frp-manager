# Changelog

**English** · [Français](CHANGELOG.fr.md)

All notable changes to FRP Manager are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and, since 0.1.0, version numbers follow
[semantic versioning](https://semver.org/): every version first ships as a
pre-release, then becomes the final release with the same number. Versions
older than 0.0.51 are described in the
[GitHub release notes](https://github.com/Gogowwww/frp-manager/releases).

## [Unreleased]

### Changed

- **Lighter `frp-manager.zip`** (about 1 MB less): the archive of each release
  now holds only the program, `install.sh`, `app.py`, `docker-compose.yml`, `LICENSE`
  and the English `README.md` and `CHANGELOG.md`. The `docs/` folder (README
  screenshots) and the French versions are left out: updates only use the
  program, and the documentation is on the website.

### Removed

- **Fewer documentation files**: the Docker page (privileges and risks) is
  now the "Docker and Portainer" section of the installation guide, the code
  of conduct is down to a short summary of the Contributor Covenant, and the
  go-mmproxy history section and the internal GitHub settings note are gone.
- **`pyproject.toml`**: the ruff configuration for the Python bridge, which
  the CI never ran. The bridge is still checked with
  `python3 -m py_compile`, like in the CI.

## [0.3.0] - 2026-10-24

The public port of a new port is picked by the frps server again.

Pre-release.

### Removed

- **Public port picked by the panel** (0.1.1): a new port no longer gets a
  random public port, and the "Random" button of the editor and the "Pick a
  random port" button of the Ports page are gone. Left empty, the public port
  is picked by the frps server; "Show the assigned port" is still there to
  see it. Typing a number by hand is still possible, and ports already
  saved keep theirs.

## [0.2.1] - 2026-10-20

"Show the assigned port" explains what it changes before acting.

Pre-release.

### Changed

- **"Show the assigned port" warns before acting**: a dialog explains what
  the panel adds to `frpc.toml` (local dashboard on `127.0.0.1`,
  `frp-manager` user, random password), that frpc will restart and how to
  remove it, before doing it. The security policy and the README describe it
  too.

## [0.2.0] - 2026-10-19

The port assigned by the server shows in Ports in one click.

Pre-release.

### Added

- **Show the port assigned by the server**: for a port left without a
  number, a "Show the assigned port" button on the Ports page turns on frpc's
  local dashboard and restarts frpc; the row then shows the port frps picked,
  with the "Assigned" badge. This dashboard only listens on `127.0.0.1`
  (unreachable from the network), on a free port from 7400, with a random
  password, in the frpc configuration only root can read. Nothing is turned
  on without that click. Not for a frpc in a container, which the panel
  cannot reach locally.

## [0.1.1] - 2026-10-19

The panel picks the public port of a new port by itself.

Pre-release.

### Changed

- **Public port picked by the panel**: a new TCP or UDP port gets a public
  port picked at random (10000–32767), with a "Random" button to pick
  another one. The port is thus known and no longer changes each time frpc
  starts, with nothing else to turn on. Leaving it empty is still possible
  (the server picks one). For ports already opened without a number, a "Pick
  a random port" button gives them one.

## [0.1.0] - 2026-10-19

The port assigned by the server shows in Ports.

Pre-release, then final release, on the same day. First version with
semantic versioning; it also includes the 0.0.60 pre-release.

### Added

- **Port assigned by the server**: a port opened without a number
  (`remotePort` missing or 0), whose port frps picks, now shows in Ports the
  public address actually assigned, with an "Assigned" badge. The panel reads
  it from frpc's local dashboard (`[webServer]`); when it is off, a message
  explains how to turn it on. A port in error on the frpc side (port already
  taken…) gets an "Error" badge with frpc's message.

### Fixed

- A port opened with `remotePort = 0` was shown as "server:0" instead of
  "port assigned by the server".

## [0.0.60] - 2026-10-19

A deleted frps or frpc service can be reinstalled from the dashboard.

Pre-release, included in the 0.1.0 release.

### Added

- **Reinstall a deleted frps or frpc service**: when the dashboard no longer
  has a frps (or frpc) service, a "Reinstall frps" button creates
  `frps.service` again as the installer does, on `/etc/frp/frps.toml`: the
  configuration kept when it was deleted, or else a default one. The program
  is downloaded (and verified) if it is missing. The service is created
  stopped, to be started from its card; the log is shown in a dialog.

### Fixed

- Following a frp installation could stop before the end (on an intermediate
  “[OK]” line): the server now reports when the task is over and whether it
  succeeded.

## [0.0.59] - 2026-10-12

Sign in with a passkey, and the documentation in the GitHub wiki.

Pre-release, then final release, on the same day.

### Added

- **Passkeys**: every account that signs in with a password can add passkeys
  (Settings → My account, password asked again) and then sign in without a
  password, from the "Sign in with a passkey" button or the suggestions of the
  username field. Passkeys can be renamed and deleted (sessions opened with a
  deleted passkey are closed); an administrator sees how many each account has
  and can remove them (lost device). Requires a domain name with a valid HTTPS
  certificate, or `localhost`.
- **GitHub wiki**: the documentation (README, installation, Docker, security,
  contributing, changelog), in English and French, is also published to the
  repository wiki, as on the website; it is regenerated each time the public
  main is published.

### Changed

- **README screenshots** retaken from the current interface, in English in the
  English README and in French in the French one, settings included.
- **Project website and demo**: their generator has left the public
  repository; they are still built automatically from `docs/` and the panel
  interface.

## [0.0.58] - 2026-10-12

The panel moves to Go: a single, lighter program that can also be installed with apt.

Pre-release, then final release, on the same day.

### Changed

- **The panel is rewritten in Go**: a single static program, with no Python and
  no dependency to install, for amd64, arm64 and armv7; about 18 MB of memory
  when idle, and a much lighter Docker image (Alpine).
  The interface, the API, the configuration (`/etc/frp-manager/frp-manager.json`),
  accounts and passwords stay the same; only the session has to be opened again
  once. Passwords hashed by the Python versions (argon2id, scrypt, old SHA-256)
  are accepted, then rehashed with argon2id when needed.
- **Automatic move from a Python version**: updating from 0.0.57 or earlier
  (**Update** button) installs the Go program for the machine, SHA-256 checked,
  and points the service to it. `install.sh` does the same and removes the
  Python environment that is no longer needed.
- Panel updates now require the SHA-256 published with the release: the running
  program is never replaced without it.
- argon2 computations (sign-in, password change) are limited to two at a time:
  a burst of attempts can no longer exhaust memory.
- **Configuration** moves into Settings, in an **frp** group: the instance
  picker sits in the section header, and leaving the section with unsaved
  changes still asks for confirmation. The old address (`#/config?iid=…`) leads
  to the section.
- The project website (home page, documentation, online demo) is also built
  and served by a Go program (`go run ./website build|serve`), with the same
  pages; its screenshots are lossless WebP. The home page gets an **apt** tab
  next to Docker and the install script.

### Added

- **APT repository**: once the repository is added (key and `deb` line, see
  the installation documentation), `sudo apt install frp-manager` installs the
  panel and `apt upgrade` updates it. The package prepares frp like
  `install.sh` (frps/frpc services, default configurations with a random frps
  port, frp download). `stable` distribution for releases, `testing` for
  pre-releases; amd64, arm64 and armhf. The `.deb` files are also attached to
  every release.
- **Random frps port**: at install time, `install.sh` offers a listening port
  picked at random between 10000 and 32767 for `frps.toml` (Enter to accept,
  `7000` or another number otherwise), free on the machine, instead of the 7000
  that bots target; `FRPS_BIND_PORT` answers without asking, and the port is
  shown at the end of the install. In Settings → Configuration, a **Random**
  button next to the frps listening port picks one the same way (Docker
  installs included).

### Fixed

- Connections blocked by the firewall are shown live again: since 0.0.56, the
  `/ws/firewall` WebSocket closed right after opening, and the interface fell
  back to periodic polling.

## [0.0.57] - 2026-10-12

All settings in one place.

Pre-release.

### Changed

- **All settings in one place**: Notifications, Updates, Users and Single
  sign-on leave the main menu and become sections of **Settings**, in three
  groups (Personal: My account, Appearance; Access and security: Panel access,
  Users, Single sign-on; System: Notifications, Updates). A menu on the left, a
  scrolling row on mobile; each account only sees the sections it is allowed.
  Old addresses (`#/notifications`, `#/updates`, `#/users`) lead to the right
  section, and the update dot moves to Settings.
- **My account** shows the profile (username, role, instances) above the
  password.

## [0.0.56] - 2026-10-11

Several accounts with their own permissions, and OpenID Connect single sign-on.

Pre-release.

### Added

- **Users and permissions (ACL)**: a **Users** page creates more accounts besides
  the owner (the account from the initial setup, always administrator). Each
  account has a role — administrator, operator, viewer — or a custom set among
  17 permissions (see instances, control, delete, read or edit configurations,
  logs, ports, firewall, updates, panel update, notifications, settings,
  accounts), and can be limited to some instances. Every API route and
  WebSocket checks them; changes, disabling and deletion apply at once, open
  sessions included. An account that manages users can't grant permissions it
  doesn't have. The interface hides what the account can't do; configuration,
  ports and firewall become read-only without the edit permission.
- **Single sign-on with OpenID Connect**: a "Sign in with …" button on the sign-in
  page (Keycloak, Authentik, Authelia, Zitadel, Google, Microsoft Entra…).
  Authorization code flow with PKCE, `state` and `nonce`. Accounts are prepared
  by email (linked on the first sign-in whose email the provider has verified)
  or created on first sign-in, optionally restricted to some email domains;
  roles can follow the provider's groups. Password sign-in always remains
  available.
- **My account** in Settings: every local account changes its own password.

### Changed

- Settings are only shown to accounts allowed to change them; only the owner
  changes the owner's username and password there.

## [0.0.55] - 2026-09-30

Pre-release, then final release, on the same day.

### Added

- Webhook notifications: a **Notifications** page sends alerts to Discord, Slack
  / Mattermost, Telegram, ntfy, Gotify, a JSON address (optional template and
  HMAC-SHA256 signature) or plain text. Events: instance stopped or started,
  address locked out, sign-in, configuration or firewall changed, panel or frp
  update available or installed, panel started. Per-webhook event choice,
  instance filter, message language, delay between sends, extra headers, test
  button and history of the last sends. Sends are queued and retried; addresses,
  secrets and headers never go back to the browser.
- Website: structured data (SoftwareApplication, TechArticle, BreadcrumbList),
  breadcrumbs, `hreflang` and `lastmod` in the sitemap, full Open Graph and
  Twitter tags, `llms.txt`, non-blocking fonts, preloaded main image, gzip
  compression by the site server, and `website/seo_check.py`, an audit run before
  each publication of the site.

### Changed

- Site titles and descriptions tuned to search-result lengths; the two
  documentation index pages now have distinct titles.

## [0.0.54] - 2026-09-29

Pre-release on 2026-09-29, final release on 2026-09-30.

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

[Unreleased]: https://github.com/Gogowwww/frp-manager/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/Gogowwww/frp-manager/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/Gogowwww/frp-manager/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/Gogowwww/frp-manager/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/Gogowwww/frp-manager/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/Gogowwww/frp-manager/compare/v0.0.60...v0.1.0
[0.0.60]: https://github.com/Gogowwww/frp-manager/compare/v0.0.59...v0.0.60
[0.0.59]: https://github.com/Gogowwww/frp-manager/compare/v0.0.58...v0.0.59
[0.0.58]: https://github.com/Gogowwww/frp-manager/compare/v0.0.57...v0.0.58
[0.0.57]: https://github.com/Gogowwww/frp-manager/compare/v0.0.56...v0.0.57
[0.0.56]: https://github.com/Gogowwww/frp-manager/compare/v0.0.55...v0.0.56
[0.0.55]: https://github.com/Gogowwww/frp-manager/compare/v0.0.54...v0.0.55
[0.0.54]: https://github.com/Gogowwww/frp-manager/compare/v0.0.53...v0.0.54
[0.0.53]: https://github.com/Gogowwww/frp-manager/compare/v0.0.52...v0.0.53
[0.0.52]: https://github.com/Gogowwww/frp-manager/compare/v0.0.51...v0.0.52
[0.0.51]: https://github.com/Gogowwww/frp-manager/compare/v0.0.50...v0.0.51
