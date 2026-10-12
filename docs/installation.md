# Installation, configuration and uninstall

**English** · [Français](installation.fr.md)

- [Requirements](#requirements)
- [Install script](#install-script)
- [Install with apt](#install-with-apt)
- [Docker and Portainer](#docker-and-portainer)
- [From a Python version (0.0.57 and earlier)](#from-a-python-version-0057-and-earlier)
- [First launch](#first-launch)
- [Reaching the panel from another machine](#reaching-the-panel-from-another-machine)
- [Versions and pre-releases](#versions-and-pre-releases)
- [Panel configuration](#panel-configuration)
- [Lost password](#lost-password)
- [File structure](#file-structure)
- [Uninstall](#uninstall)
- [History: go-mmproxy removal](#history-go-mmproxy-removal)

## Requirements

- Linux with **systemd**
- Nothing else: the panel is a single program with no dependency (install
  script or `.deb` package), or **Docker**
- Architectures: `amd64`, `arm64`, `armv7`

No need to install frp first: the script downloads `frps`/`frpc` and creates
their systemd services. frp is then updated from **Settings → Updates**, when
you decide: nothing is updated automatically.

## Install script

```bash
curl -LO https://github.com/Gogowwww/frp-manager/releases/latest/download/frp-manager.zip
unzip frp-manager.zip && cd frp-manager
sudo bash install.sh
```

The script installs the `frp-manager` program for your architecture in
`/opt/frp-manager`, creates the `frp-manager` systemd service and starts it.
It also prepares frp:

- `frps` / `frpc` binaries in `/usr/local/bin` (latest version, SHA-256
  checked);
- default configs `/etc/frp/frps.toml` and `/etc/frp/frpc.toml`, only if they
  don't exist yet. For `frps.toml`, the script offers a listening port picked
  at random between 10000 and 32767 (Enter to accept it, `7000` for the
  standard port, or any other number): bots scanning the Internet target port
  7000. Without a terminal, the random port is used;
  `FRPS_BIND_PORT=7000 sudo -E bash install.sh` (or `random`, or another
  number) answers without asking. The chosen port is shown at the end: open it
  in the firewall and set it as `serverPort` on clients;
- `frps.service` and `frpc.service`, created but neither enabled nor started:
  you configure them, then start them from the panel.

Running `install.sh` again from a newer archive updates the panel and keeps
its configuration. The panel's **Update** button does the same without the
command line.

## Install with apt

FRP Manager has its own APT repository (Debian, Ubuntu, Raspberry Pi OS;
`amd64`, `arm64`, `armhf`). Add it once, then the panel installs and updates
like any other package:

```bash
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL https://forgejo.gogow.fr/api/packages/administrator/debian/repository.key -o /etc/apt/keyrings/frp-manager.asc
echo "deb [signed-by=/etc/apt/keyrings/frp-manager.asc] https://forgejo.gogow.fr/api/packages/administrator/debian stable main" \
  | sudo tee /etc/apt/sources.list.d/frp-manager.list
sudo apt update
sudo apt install frp-manager
```

Updates then come with `sudo apt update && sudo apt upgrade` (the panel's
**Update** button reminds you). To follow pre-releases, replace `stable` with
`testing` in `/etc/apt/sources.list.d/frp-manager.list` (`dev`: one package per
development commit, for testing only).

The package installs `/usr/bin/frp-manager` and the `frp-manager` service, then
does what `install.sh` does: `frps` and `frpc` services (created, not started),
default configurations with a random frps port
(`sudo FRPS_BIND_PORT=7000 apt install frp-manager` for the standard port), frp
download, panel start. An installation made with `install.sh` is taken over by
the package, configuration included.

`sudo apt remove frp-manager` stops and removes the panel but keeps its
configuration; `sudo apt purge frp-manager` deletes it too. frp, its
configurations and its services stay in place. To remove the repository:
`sudo rm /etc/apt/sources.list.d/frp-manager.list /etc/apt/keyrings/frp-manager.asc`.

## Docker and Portainer

```bash
curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.yml
docker compose up -d
```

**Portainer**: *Stacks → Add stack → Repository*, URL
`https://github.com/Gogowwww/frp-manager`, compose path `docker-compose.yml`,
then *Deploy the stack*.

| Image | Contents |
|---|---|
| `ghcr.io/gogowwww/frp-manager:latest` | latest stable release (recommended) |
| `ghcr.io/gogowwww/frp-manager:X.Y.Z` | a specific version, to pin it or roll back |
| `ghcr.io/gogowwww/frp-manager:dev` | latest pre-release, to test before everyone else |

Images are published for `amd64`, `arm64` (Raspberry Pi 4/5, ARM servers) and
`armv7` from version 0.0.54 on; Docker picks the right one automatically.
Older versions only exist for `amd64`.

The container drives frp on the host through `pid: host` and `nsenter`,
which gives it root-equivalent access to the host: read
[docker.md](docker.md) before deploying it.

## From a Python version (0.0.57 and earlier)

Up to 0.0.57, the panel was a Python application; since 0.0.58, it is a single Go
program. Nothing special to do: the old panel's **Update** button installs the
new version as usual. On restart, that version downloads the Go program for
your architecture (SHA-256 checked), installs it in `/opt/frp-manager`, points
the `frp-manager` service to it and restarts the panel.

The configuration does not change: accounts, passwords, settings, webhooks,
firewall and single sign-on are kept; you only need to sign in again once.
Running `install.sh` from the new archive does the same and also removes the
Python environment that is no longer needed (`/opt/frp-manager/venv`). With
Docker, `docker compose pull && docker compose up -d` is enough.

## First launch

As long as no password exists, the panel only shows an **initial setup** page:
choose the administrator username and a password of at least 12 characters.
No other page and no API route answers before that.

The HTTPS certificate is self-signed: your browser shows a warning on first
access. For a valid certificate, put the panel behind a reverse proxy (nginx,
Caddy) with Let's Encrypt.

## Reaching the panel from another machine

A new install only listens on **`127.0.0.1`**: the panel cannot be reached
from the network until you decide so.

| Method | How |
|---|---|
| SSH tunnel (recommended) | `ssh -L 8765:127.0.0.1:8765 user@server`, then `https://127.0.0.1:8765` |
| Local reverse proxy | nginx or Caddy listens on the network and forwards to `127.0.0.1:8765` (allow the WebSocket upgrade on `/ws/`) |
| Listen on the network | set `bind_host` to `0.0.0.0` in **Settings → Network access** (or in the config file), or `FRP_MANAGER_HOST=0.0.0.0` with Docker, then restart the panel |

If you open the panel to the network, filter access by IP (firewall, VPN).
An install older than 0.0.51 keeps its current listening address.

nginx example (excerpt):

```nginx
location / {
    proxy_pass https://127.0.0.1:8765;
    proxy_set_header Host $host;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

Behind a TLS reverse proxy with `ssl_enabled: false`, add
`"cookie_secure": true` to the config file so the session cookie stays
*Secure*.

## Versions and pre-releases

Every new version first ships as a **pre-release**, to be tested before it is
offered to everyone. Numbers follow each other and a validated pre-release
becomes the final release **with the same number**, without being rebuilt.

| | Pre-release | Release |
|---|---|---|
| GitHub page | marked *Pre-release* | *Latest* |
| Docker image | `:X.Y.Z` + `:dev` | `:X.Y.Z` + `:latest` |
| Offered to stable panels | no | yes |
| Offered to pre-release panels (install script) | yes | yes |

- **Stable panel**: it only sees releases.
- **Pre-release panel, install script**: the "Update" button offers the most
  recently published version, pre-release or release.
- **Pre-release panel under Docker**: change the image tag (`:dev` or
  `:latest`).

When the installed pre-release becomes a final release, the panel switches
back to the stable channel on its own.

## Panel configuration

Everything is set from the **Settings** page. The underlying file is
`/etc/frp-manager/frp-manager.json`, readable by root only:

| Key | Description | Default |
|---|---|---|
| `bind_host` | Listening address | `127.0.0.1` (new install) |
| `bind_port` | Panel port | `8765` |
| `username` | Login username | `admin` |
| `password_hash` | Hashed password (argon2id or scrypt, managed by the interface) | created on first launch |
| `secret_key` | Session signing key, drawn at random on first start | generated |
| `session_timeout` | Session length in seconds | `3600` |
| `ssl_enabled` | HTTPS with a self-signed certificate | `true` |
| `cookie_secure` | *Secure* cookie even without HTTPS on the panel side (TLS reverse proxy) | `false` |
| `download_mirrors` | Third-party mirrors as a fallback to github.com to download frp | `true` |
| `nicknames` | Instance nicknames (managed by the interface) | `{}` |

`bind_host`, `bind_port` and `ssl_enabled` apply the next time `frp-manager`
restarts.

Environment variables:

| Variable | Effect |
|---|---|
| `FRP_MANAGER_HOST` | Listening address, takes precedence over `bind_host` |
| `FRP_MANAGER_PORT` | Port of a new install (written to `bind_port` on first start) |
| `FRP_MANAGER_NO_MIRRORS=1` | Forbids third-party download mirrors |
| `FRP_MANAGER_CONFIG` | Other location for the configuration file |

### Download mirrors

If github.com does not respond, frp can be downloaded through third-party
mirrors (`mirror.ghproxy.com`, `ghfast.top`, `gh-proxy.com`). These services
see the file go through and could tamper with it: it is a supply-chain risk.
The panel therefore only accepts an archive from a mirror if its SHA-256
matches the one published with the frp release, and says so in the install
log. If github.com is reachable for you, disable the mirrors
(**Settings → Network access**).

## Webhook notifications

**Settings → Notifications** sends a message to an address of your choice when something happens on the
machine. Each webhook has its own destination, format, events and language, so you can send outages to
Discord, updates to ntfy and security events to your own service.

| Format | Address to enter |
| --- | --- |
| Discord | the webhook address of a channel (Settings, Integrations); sent as a coloured card |
| Slack / Mattermost | an Incoming Webhook address |
| Telegram | `https://api.telegram.org/bot<token>` and the chat ID |
| ntfy | the topic address, `https://ntfy.sh/my-topic` or your own server; priority follows severity |
| Gotify | `https://gotify.example.org/message?token=…` |
| JSON (generic) | any address; body `{event, level, title, message, host, time, panel_version, instance, data}`, or your own template |
| Plain text | any address; title, message and machine name as text |

Events: instance stopped or started (checked every 15 seconds, and confirmed by a second reading so a quick
restart does not raise an alert), address locked out after failed sign-ins, sign-in, configuration or
firewall changed, panel or frp update available (checked every 6 hours, announced once per version), frp
updated, panel started. Optional settings: limit to some instances, a delay between two sends of the same
event, the message language, extra HTTP headers, and a signing secret.

The generic format supports a JSON template with the variables `{{event}}`, `{{title}}`, `{{message}}`,
`{{level}}`, `{{host}}`, `{{time}}`, `{{instance}}`, `{{subject}}` and `{{version}}`, escaped for JSON:
`{"content": "{{title}}: {{message}}"}`. With a signing secret, each request carries
`X-FRPManager-Signature: sha256=<HMAC-SHA256 of the body>`:

```python
import hashlib, hmac
expected = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
ok = hmac.compare_digest(expected, request.headers["X-FRPManager-Signature"])
```

Sends are queued and retried three times on a network error, a 5xx or a 429; the last ones are listed on the
page. The address, secret and headers are stored in the configuration file (readable by root only) and are
never sent back to the browser: leave a field empty when editing to keep the saved value. Redirections are not
followed.

## Lost password

```bash
sudo /opt/frp-manager/frp-manager --reset-password     # .deb package: sudo frp-manager --reset-password
sudo systemctl restart frp-manager
```

With Docker: `docker exec -it frp-manager frp-manager --reset-password`,
then `docker restart frp-manager`.

## File structure

```
Repository
  panel/                   The panel: a Go program (go build ./panel)
    *.go                   HTTP server, API, firewall, updates…; *_test.go: tests
    install.sh             Install script
    templates/             Interface, embedded in the program at build time
      index.html, login.html, setup.html
      partials/icons.html  SVG icons (embedded, no CDN)
      assets/
        css/app.css        Light and dark themes
        js/                Application (ES modules, no build step)
        locales/fr.js, en.js  Interface texts
  docs/                    Documentation, security policy, contributing guide
  website/                 Project website and live demo (website/demo/)
  packaging/               .deb package, move from a Python version
  Dockerfile, docker-compose.yml, go.mod

/opt/frp-manager/          Installed panel (script); /usr/bin/frp-manager (.deb package)
/etc/frp-manager/          Panel configuration + SSL certificates
/etc/frp/                  frps/frpc configurations (TOML)
/var/log/frp/              frp logs
/var/lib/frp-manager/      State (installed versions)
/etc/systemd/system/       frp-manager.service, frps.service, frpc.service
```

The panel only writes frp configurations in the directories where it looks
for them: `/etc/frp`, `/usr/local/etc/frp`, `/opt/frp`, `/root/frp`.

## Uninstall

```bash
sudo systemctl disable --now frp-manager
sudo rm /etc/systemd/system/frp-manager.service
sudo rm -rf /opt/frp-manager /etc/frp-manager
sudo systemctl daemon-reload
```

With the `.deb` package: `sudo apt purge frp-manager`.

frp configurations (`/etc/frp/`) and binaries (`/usr/local/bin/`) are kept.

## History: go-mmproxy removal

Version 0.0.26 removed the "Real IP" option (go-mmproxy). If you used it, the
panel automatically points the affected tunnels back to their real service on
first start, restarts frpc, then removes the relays, the routing rules and the
`go-mmproxy` binary. To pass on visitors' IP addresses, use the **PROXY
protocol** option with a service that supports it (nginx, HAProxy…).
