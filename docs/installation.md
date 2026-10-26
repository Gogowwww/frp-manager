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

## Requirements

- Linux with **systemd** (install script or `.deb` package), or **Docker**:
  in standalone mode, the container needs neither systemd nor frp on the host
- Nothing else: the panel is a single program with no dependency
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
sudo curl -fsSL https://apt.frp-manager.org/repository.key -o /etc/apt/keyrings/frp-manager.asc
echo "deb [signed-by=/etc/apt/keyrings/frp-manager.asc] https://apt.frp-manager.org stable main" \
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

The image exists in two modes:

- **Standalone** (`docker-compose.yml`, recommended): the image ships `frps`
  and `frpc`, and the panel runs them itself, inside the container. Nothing is
  installed on the host, no `privileged`, no `pid: host`, no Docker socket.
- **Host** (`docker-compose.host.yml`): the panel drives frp installed **on
  the host** (systemd services, journal, `/usr/local/bin`) through `nsenter`.
  This is how the image worked before the standalone mode; installs made that
  way keep working unchanged.

```bash
curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.yml
docker compose up -d
```

**Portainer**: *Stacks → Add stack → Repository*, URL
`https://github.com/Gogowwww/frp-manager`, compose path `docker-compose.yml`
(standalone) or `docker-compose.host.yml` (host), then *Deploy the stack*.

> **Stack deployed from the repository before the standalone mode**: its
> compose path `docker-compose.yml` now leads to the standalone mode. To keep
> driving the host's frp, change the compose path to `docker-compose.host.yml`
> before redeploying the stack.

| Image | Contents |
|---|---|
| `ghcr.io/gogowwww/frp-manager:latest` | latest stable release (recommended) |
| `ghcr.io/gogowwww/frp-manager:X.Y.Z` | a specific version, to pin it or roll back |
| `ghcr.io/gogowwww/frp-manager:dev` | latest pre-release, to test before everyone else |

Images are published for `amd64`, `arm64` (Raspberry Pi 4/5, ARM servers) and
`armv7` from version 0.0.54 on; Docker picks the right one automatically.
Older versions only exist for `amd64`. They contain `frps` and `frpc`
(official fatedier/frp archive, SHA-256 checked at build time; the frp version
is the `FRP_VERSION` argument of the `Dockerfile`).

The panel picks its mode on its own: standalone in a container that sees
nothing of the host, host mode as soon as it finds the host's mounts
(`/host/usr/local/bin`, `/run/systemd`) or `pid: host`.
`FRP_MANAGER_MODE=standalone` or `FRP_MANAGER_MODE=host` forces it.

### Standalone mode

**What the container does.** The panel starts `frps` and `frpc` as its own
processes: start, stop and restart from the dashboard, **Start on boot**
switch (the program starts with the container), automatic restart after an
unexpected exit (after 2 s, then up to 60 s if it keeps failing), clean stop
of frps and frpc on `docker stop`. On the very first launch, the panel creates
`frps.toml` (listening port picked at random between 10000 and 32767, or
`FRPS_BIND_PORT`; random token) and `frpc.toml`. A configuration deleted from
the panel does not come back.

**Data.** Everything that must last is in the single `/data` volume:

| Folder | Seen by the panel as | Contents |
|---|---|---|
| `/data/panel` | `/etc/frp-manager` | panel configuration, accounts, certificates |
| `/data/frp` | `/etc/frp` | `frps*.toml` / `frpc*.toml`, edited from the panel |
| `/data/log` | `/var/log/frp` | frp logs and the programs' output |
| `/data/lib` | `/var/lib/frp-manager` | state, frp updated from the panel (`bin/`) |

Every `frps*.toml` or `frpc*.toml` file in `/data/frp` is an instance (one
card in the dashboard). To use a folder of the host instead of the named
volume: `- ./frp-manager-data:/data`.

**Logs.** The **Console** source shows what the program writes (its output,
`/var/log/frp/<instance>.console.log`, capped at 5 MB), the **File** source
the file set by `log.to` in its configuration.

**frp updates.** **Settings → Updates** installs the new version in
`/data/lib/bin`: it replaces the image's version and survives container
recreation. A newer image that brings a newer frp takes over again; to go
back to the image's version, delete `/data/lib/bin`.

**Network.** The provided configuration uses the host network: frps listens
directly on the machine's ports, and the tunnels' ports (`remotePort`) do not
have to be published one by one. Without the host network, remove
`network_mode`, publish the panel, frps's port and the tunnels' ports, set
`FRPS_BIND_PORT` to the published port and `FRP_MANAGER_HOST=0.0.0.0` (see the
comments of `docker-compose.yml`).

**Firewall.** nftables is part of the image. The panel needs the `NET_ADMIN`
capability (`cap_add: [NET_ADMIN]`, already in `docker-compose.yml`); without
it everything else works and the **Firewall** page says what is missing. With
the host network, the rules filter the machine's traffic, exactly like the
standard install, and stay in place if the container stops. ufw is not in the
image: the panel only uses it through its API (reading and adding rules), and
in a container on the host network it would drive the host's iptables next to
the host's own ufw, with a separate rule state (`/etc/ufw`) and about 45 MB
more (Python).

**Privileges.** `NET_ADMIN` on the host network lets the container change the
machine's network and firewall rules: anyone who controls the panel can cut
or open network access. That is much less than the host mode, which gives
root over the whole machine.

### Host mode

```bash
curl -O https://raw.githubusercontent.com/Gogowwww/frp-manager/main/docker-compose.host.yml
docker compose -f docker-compose.host.yml up -d
```

The panel runs in the container but drives frp **on the host**: `frps`/`frpc`
systemd services, logs, binaries in `/usr/local/bin`, nftables firewall rules.
It does not ship systemd; it runs commands inside the host with `nsenter`:

```
nsenter -t 1 -m -u -i -n -p -- systemctl restart frpc
```

`-t 1` targets process 1 **of the host**, visible thanks to `pid: host`; the
`-m -u -i -n -p` options move the command into its namespaces (mounts,
hostname, IPC, network, processes). The command then runs on the host's file
system and services, with the container's capabilities.

#### Implications: root-equivalent access to the host

With the provided configuration (`pid: host`, `privileged: true`,
`network_mode: host`, Docker socket mounted), **the container isolates
nothing**:

- `privileged: true` grants every Linux capability and device access, and
  disables the default seccomp and AppArmor profiles;
- `pid: host` + `nsenter -t 1` allow running any command as root on the host;
- the Docker socket (`/var/run/docker.sock`) alone allows creating a container
  that mounts the host's `/`: that is root access too.

In practice:

1. Anyone logged into the panel controls the host. This is already true for
   the standard install (the service runs as root): Docker does not reduce
   that risk.
2. A remotely exploitable flaw in the panel would give root on the host. Hence
   the mandatory initial setup, listening on `127.0.0.1` by default and the
   protections described in [SECURITY.md](SECURITY.md).
3. Do not expose the panel to the Internet without a reverse proxy, a VPN or
   IP filtering: see
   [Reaching the panel from another machine](#reaching-the-panel-from-another-machine).

#### Reducing privileges: current status

Replacing `privileged: true` with `cap_drop: [ALL]` plus a few capabilities,
`read_only: true` and `no-new-privileges` is a fair request. **This reduction
is not validated yet**: it could not be tested on a real Docker host, so the
provided configuration stays `privileged: true`. Here is the analysis for
anyone who wants to try.

What the panel needs, feature by feature:

| Feature | Mechanism | Capabilities likely needed |
|---|---|---|
| Enter the host namespaces | `nsenter -t 1` (setns) | `SYS_ADMIN`, `SYS_PTRACE`, `SYS_CHROOT` |
| frp services, logs | `systemctl`, `journalctl` through nsenter | nothing more (systemd socket, root) |
| Firewall | `nft` through nsenter | `NET_ADMIN` |
| Writing binaries, configs, units | host files | `DAC_OVERRIDE`, `CHOWN`, `FOWNER` |
| "Install nftables" button | host package manager | `SETUID`, `SETGID`, `CHOWN`, `FOWNER`, `DAC_OVERRIDE`, `KILL`… |

Limits:

- **`SYS_ADMIN` alone is enough to escape the container** (it is the
  capability that allows `setns` into the host). A `cap_drop` brings little
  as long as the panel has to drive the host: it mostly stops opportunistic
  attacks, not an attacker who controls the panel.
- Docker's default seccomp profile only allows `setns` with `SYS_ADMIN`;
  AppArmor (`docker-default`) may block some operations as well. You may need
  `security_opt: [apparmor:unconfined]`.
- `read_only: true` needs a `tmpfs` on `/tmp` (temporary downloads and
  extractions); the written directories are already volumes.
- The Docker socket stays root access: remove it if you have no frp
  containers to manage.

Candidate configuration, **not verified**, to try on a test machine:

```yaml
    # instead of privileged: true
    cap_drop: [ALL]
    cap_add: [SYS_ADMIN, SYS_PTRACE, SYS_CHROOT, NET_ADMIN, DAC_OVERRIDE, CHOWN, FOWNER, SETUID, SETGID, KILL]
    security_opt:
      - no-new-privileges:true
    read_only: true
    tmpfs:
      - /tmp
```

Check afterwards: starting/stopping an frp service from the panel, live logs,
installing frp (Settings → Updates), applying a firewall rule, deleting an instance.
If you validate a reduced configuration that works, a pull request is welcome.

**Safer alternative**: the standard install (`install.sh`) without Docker,
with the panel on `127.0.0.1` and access through an SSH tunnel or a VPN.

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

## Passkeys

Each account that signs in with a password can add **passkeys**
(Settings → My account → Passkeys; the password is asked again): the device
then signs you in on its own (fingerprint, face, PIN, security key), without
typing a username or password. The password keeps working.

- Browsers only allow passkeys on a **domain name with a valid HTTPS
  certificate**, for example behind a reverse proxy with Let's Encrypt
  (`https://panel.example.com`), or on `http://localhost`. The panel's
  self-signed certificate and access by IP address (`https://192.0.2.10:8765`)
  are refused: the Passkeys card says so.
- A passkey is tied to the domain it was created on: one created on
  `panel.example.com` only works there.
- Behind a reverse proxy, forward the original `Host` header
  (`proxy_set_header Host $host;`, as above) or `X-Forwarded-Host`.
- Lost device: delete the passkey from Settings → My account after signing in
  with your password, or ask an administrator to remove the account's passkeys
  (Users → account → *Remove their passkeys*). Sessions opened with a deleted
  passkey are closed.

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
| `anonymous_stats` | Anonymous statistics sent once a day (see below) | `false` |
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

### Anonymous statistics

Off by default. If you turn it on (box in the initial setup, or
**Settings → Panel access → Anonymous statistics**), the panel sends once a
day, to `https://frp-manager.org/api/ping`, exactly this and nothing
else (shown in Settings before you turn it on):

```json
{"version": "0.4.0", "arch": "arm64", "install": "apt"}
```

- `version`: panel version; `arch`: `amd64`, `arm64` or `arm`; `install`:
  `script`, `apt` or `docker`.
- No identifier, no name, no address, no frp configuration. The website only
  keeps daily counters per version, architecture and install method; it does
  not store IP addresses (only used in memory, hashed, for 24 h, to limit
  abuse).
- It helps to know which versions are still in use, so as to plan
  compatibility and updates. Turning it off stops the sending right away.

## Ports: TCP + UDP and port ranges

On the **Ports** page, the port editor offers two shortcuts for services
that need more than one tunnel:

- **TCP + UDP** (when creating a port only) opens the same port in both
  protocols, for games or DNS for instance. The panel creates two ordinary
  tunnels, `<name>-tcp` and `<name>-udp`, with the same ports and options. The
  public port is required, otherwise the server would pick a different one
  for each. Afterwards, each one is edited or deleted on its own.
- **Port range** (`tcp` and `udp`): type a range (`30000-30010`) or a list
  (`6000-6006,6007`) in the local port, and as many ports in the public port,
  which is then required (1,000 ports at most). The range stays a single
  entry in the list, and the panel writes it as an frp template:

```toml
{{- range $_, $v := parseNumberRangePair "30000-30010" "30000-30010" }}
[[proxies]]
name = "game-{{ $v.First }}"
type = "udp"
localIP = "127.0.0.1"
localPort = {{ $v.First }}
remotePort = {{ $v.Second }}
{{- end }}
```

frpc pairs the ports in order (first local port with the first public port,
and so on) and creates one tunnel per port, named `game-30000`,
`game-30001`… The panel refuses a name that one of these tunnels would
already use, since frpc does not start with two tunnels of the same name.
`parseNumberRangePair` exists since **frp 0.56**: an older frpc refuses this
configuration, so update frp first (**Settings → Updates**).

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

```go
mac := hmac.New(sha256.New, []byte(secret)) // crypto/hmac, crypto/sha256
mac.Write(body)
expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
ok := hmac.Equal([]byte(expected), []byte(r.Header.Get("X-FRPManager-Signature")))
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

The owner's passkeys are kept: they still sign in.

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
                           (also on the website); img/: screenshots
  internal/                Shared code (public address rule)
  tools/                   Repository tools: blocklists (firewall community lists),
                           releasenotes (release notes)
  packaging/               .deb package, move from a Python version
  Dockerfile, docker-compose.yml (Docker, standalone), docker-compose.host.yml
  (Docker, host mode), go.mod

/opt/frp-manager/          Installed panel (script); /usr/bin/frp-manager (.deb package)
/etc/frp-manager/          Panel configuration + SSL certificates
/etc/frp/                  frps/frpc configurations (TOML)
/var/log/frp/              frp logs
/var/lib/frp-manager/      State (installed versions)
/etc/systemd/system/       frp-manager.service, frps.service, frpc.service

Docker, standalone mode: a single /data volume, seen by the panel as the
folders above (/data/panel, /data/frp, /data/log, /data/lib); the image's
frps and frpc in /opt/frp-manager/bin
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

With Docker: `docker compose down` stops and removes the container; in
standalone mode, `docker compose down -v` also erases the `/data` volume (frp
and panel configurations included).
