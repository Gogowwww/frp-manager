# FRP Manager

A self-hosted web UI and dashboard for [frp](https://github.com/fatedier/frp): run **frps** and **frpc** from your browser instead of editing TOML files, restarting systemd units and reading logs over SSH.

[![License](https://img.shields.io/github/license/Gogowwww/frp-manager)](LICENSE)
[![Release](https://img.shields.io/github/v/release/Gogowwww/frp-manager)](https://github.com/Gogowwww/frp-manager/releases)
![Platform](https://img.shields.io/badge/platform-Linux-blue)
[![Docker](https://img.shields.io/badge/image-ghcr.io-2496ED?logo=docker&logoColor=white)](https://github.com/users/Gogowwww/packages/container/package/frp-manager)
[![Website](https://img.shields.io/badge/website-frp--manager.org-0e9f6e)](https://frp-manager.org)
[![Plumber Score](https://score.getplumber.io/github.com/Gogowwww/frp-manager.svg)](https://score.getplumber.io/github.com/Gogowwww/frp-manager)

**English** · [Français](README.fr.md)

![FRP Manager dashboard](docs/img/dashboard-en.png)

## Quick start

With Docker Compose, on the machine that runs (or will run) frps or frpc:

```bash
curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.yml
docker compose up -d
```

The panel listens on `127.0.0.1:8765` only. From your computer, open an SSH tunnel with `ssh -L 8765:127.0.0.1:8765 user@server`, browse to `https://127.0.0.1:8765` and create the administrator account. To open it to your network, see [Reaching the panel from another machine](docs/installation.md#reaching-the-panel-from-another-machine).

Without Docker, the install script sets up the panel (a single program, no dependency), frp and their systemd services:

```bash
curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager && sudo bash install.sh
```

Or with apt, once FRP Manager's repository is added: `sudo apt install frp-manager`, then `apt upgrade` for updates; see [Install with apt](docs/installation.md#install-with-apt).

Not ready to install? The [live demo](https://frp-manager.org/demo/) runs the real interface with sample data.

## Features

- **Dashboard**: automatic detection of frps/frpc systemd services, binaries, configs and Docker containers; start, stop, restart, start on boot; live status over WebSocket; nicknames; a deleted frps/frpc service can be reinstalled in one click.
- **Ports (frpc)**: readable list (`vps.example.net:25565 → 127.0.0.1:25565`) and a guided editor for `tcp`, `udp`, `http`, `https`, `stcp`, `xtcp` and visitors, with PROXY protocol, encryption and compression options, the same port in TCP and UDP at once, and [port ranges](docs/installation.md#ports-tcp--udp-and-port-ranges) (`30000-30010`). Settings the editor does not handle are kept as they are. A port left without a number gets its public port from the server; the panel can show it, after a confirmation, through frpc's local dashboard (see [SECURITY](docs/SECURITY.md)).
- **Firewall (frps)**: allow-only or block rules by address, network or whole provider (AS number), applied in the kernel with nftables before Docker's NAT; blocked connections in real time; optional community lists.
- **Configuration**: frps and frpc settings in sections, each field showing its TOML key; configs are parsed before being written.
- **Logs**: systemd journal, log file or Docker container, streamed live (WebSocket, SSE fallback), with a filter.
- **Notifications**: webhooks to Discord, Slack, Telegram, ntfy, Gotify or any URL (JSON template, HMAC signature) for stopped instances, locked-out addresses and updates.
- **Updates**: one-click frp install with SHA-256 verification, manual archive upload, one-click panel update.
- **Users and permissions**: several accounts, each with a role (administrator, operator, viewer) or a custom set of permissions, optionally limited to some instances; changes apply at once, open sessions included.
- **Single sign-on**: OpenID Connect (Keycloak, Authentik, Authelia, Zitadel, Google, Microsoft Entra…), with accounts prepared by email or created on first sign-in, and roles taken from the provider's groups.
- **Passkeys**: sign in without a password, with your phone, computer or security key (fingerprint, face, PIN); several passkeys per account.
- **Interface**: light and dark themes, desktop and mobile, in English and French (picked from your browser, changeable in Settings).

| Ports | Port editor |
|:---:|:---:|
| ![Ports](docs/img/ports-en.png) | ![Port editor](docs/img/port-editor-en.png) |
| **Live logs** | **Settings** |
| ![Live logs](docs/img/logs-en.png) | ![Settings](docs/img/settings-en.png) |

## Why this panel

frp is a single binary configured by TOML files. Running it by hand works well, but day-to-day management stays manual: editing a file over SSH for every new port, restarting the right unit, checking which port is already taken, finding the right log. FRP Manager keeps that model and adds a layer on top of it:

| Task | Manual frp | FRP Manager |
|---|---|---|
| Add or change a tunnel | edit `frpc.toml`, restart frpc | guided form, TOML validated before saving, save and restart in one click |
| See what is exposed | read the configs | list of ports with their path, per client |
| Several frps/frpc instances | one unit and one file per instance to track | detected automatically, one card each |
| Filter who reaches frps ports | write nftables or iptables rules | rules by IP, network or AS, tested before applying |
| Follow logs | `journalctl -fu frpc` over SSH | live logs in the browser |
| Update frp | download, check, replace the binaries, restart | one click, SHA-256 checked, services restarted |

The panel does not replace frp nor change its configuration format: files stay plain TOML that you can still edit by hand, and removing the panel leaves frp running as before.

## Security

The panel controls exposed ports and runs as root, so it is locked down by default:

- **Mandatory initial setup**: no page or API route answers until an administrator account exists (password of 12 characters or more).
- **Password hashing** with argon2id; older hashes (scrypt, SHA-256) are accepted once and rehashed automatically.
- **Listens on `127.0.0.1`** in a new install; opening it to the network is an explicit choice.
- **Brute-force protection**: attempts limited per IP, with a growing lockout.
- **Least privilege**: every API route and WebSocket checks the account's permissions; an account that manages users can't grant more than it has, and only a full administrator configures single sign-on.
- **Passkeys (WebAuthn)**: added only after confirming the password, user verification required, passkeys tied to the panel's domain, signature counter checked.
- **OpenID Connect**: authorization code flow with PKCE, `state` and `nonce`; the ID token's issuer, audience and expiry are checked; accounts are linked by provider and subject, or by an email the provider has verified.
- **Web hardening**: CSRF token on every change, HttpOnly / SameSite=Strict / Secure cookies, Content-Security-Policy, X-Frame-Options, Referrer-Policy.
- **System hardening**: no shell commands built from user input, allow-listed service and container names, configs confined to frp's directories, safe archive extraction.
- **Supply chain**: frp archives and panel updates checked against their published SHA-256; third-party mirrors (ghproxy, ghfast, gh-proxy) can be disabled and are never used without a checksum.

> **Docker warning**: the container uses `pid: host`, `privileged: true` and `nsenter` to drive systemd on the host, plus the Docker socket. **That is root-equivalent access to the host**: anyone who controls the panel controls the machine. Read [the Docker section of the installation guide](docs/installation.md#docker-and-portainer) before deploying it.

Threat model and vulnerability reporting: [SECURITY.md](docs/SECURITY.md).

## Documentation

- [Website](https://frp-manager.org): overview, live demo and the full documentation
- [Installation, configuration and uninstall](docs/installation.md): requirements, Docker images and their privileges, versions and pre-releases, configuration keys, port ranges and TCP + UDP, reverse proxy, lost password, file structure
- [Security policy](docs/SECURITY.md)
- [Changelog](CHANGELOG.md)

## Contributing

Bug reports, ideas, translations and pull requests are welcome: see [CONTRIBUTING.md](docs/CONTRIBUTING.md) and the [code of conduct](docs/CODE_OF_CONDUCT.md). Addresses worth blocking can be shared through the firewall's **Publish** button or on the [`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists) branch.

## License

[Apache 2.0](LICENSE).

## Acknowledgements

[fatedier/frp](https://github.com/fatedier/frp), the project this panel is built around.

## Development

FRP Manager is developed with the help of an AI coding assistant ([Claude](https://claude.ai)). New versions ship as pre-releases first, to be tested before being promoted to a release.

[![Star History Chart](https://api.star-history.com/chart?repos=Gogowwww/frp-manager&type=date&legend=bottom-right)](https://www.star-history.com/?repos=Gogowwww%2Ffrp-manager&type=date&legend=bottom-right)
