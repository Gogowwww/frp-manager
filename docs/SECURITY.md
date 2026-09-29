# Security policy

**English** · [Français](SECURITY.fr.md)

FRP Manager drives network services exposed to the Internet (frps, frpc),
firewall rules and, depending on the installation, the host system with root
privileges. A flaw in the panel can therefore have serious consequences:
please report it responsibly.

## Reporting a vulnerability

- **Do not open a public issue.**
- Use GitHub's private reporting: the repository's **Security** tab, **Report a
  vulnerability** button
  ([direct link](https://github.com/Gogowwww/frp-manager/security/advisories/new)).
- Include the panel version (at the top of the sidebar), the install method
  (script or Docker), the steps to reproduce and the impact you have in mind.

This project is maintained on a volunteer basis: expect an acknowledgement
within 7 days and, for a confirmed flaw, a fix released as a pre-release as
soon as possible. The report is credited in the release notes if you wish.

## Supported versions

| Version | Security fixes |
|---|---|
| Latest release (`:latest`) | yes |
| Latest pre-release (`:dev`) | yes |
| Older versions | no: please update |

## Threat model (summary)

**What the panel protects**

- Access to the interface and the API: username and password required (initial
  setup enforced on first launch), password hashed with argon2id (scrypt as a
  fallback), login attempts limited per IP, HttpOnly / SameSite=Strict / Secure
  (over HTTPS) sessions, CSRF token on every request that changes something.
- The administrator's browser: strict Content-Security-Policy for scripts
  (nonce), X-Frame-Options, data escaped everywhere (Jinja2 autoescape, DOM
  built without `innerHTML`).
- The system: no command passed to a shell with user input, service and
  container names allow-listed, configurations validated (TOML parsed) and
  confined to frp's configuration directories, archives extracted without
  `extractall` (paths, links and size checked).
- The supply chain: frp archives checked against the SHA-256 published with
  each fatedier/frp release; third-party mirrors (ghproxy, ghfast, gh-proxy)
  can be disabled and are never used without a checksum; the panel update
  archive is checked against its published SHA-256.

**What the panel does not protect**

- **An authenticated administrator has, by design, root-equivalent power over
  the machine**: they install binaries, write systemd units, change the
  firewall. Protecting the password means protecting the machine.
- **With Docker, `pid: host`, `privileged: true` and the Docker socket give the
  container root-equivalent access to the host** (see
  [docs/docker.md](docker.md)). Container isolation does not limit what
  the panel can do.
- The generated HTTPS certificate is self-signed: it encrypts, but does not
  authenticate the server. For access from the Internet, put the panel behind
  a reverse proxy with a valid certificate, or only reach it through a VPN or
  an SSH tunnel.
- Behind a reverse proxy, login rate limiting sees the proxy's address: filter
  access at that level too.
- frp tokens are stored in plain text in the TOML files, as frp requires; the
  configuration editor shows them to administrators.

**Recommendations**

1. Keep the panel on `127.0.0.1` (the default for a new install) and reach it
   through an SSH tunnel, a VPN or a reverse proxy.
2. If you open it to the network, create the account right away and filter
   access by IP.
3. Use a long, unique password, and a long, unique frp token per server.
4. Disable third-party mirrors (Settings → Network access) if github.com is
   reachable for you.

## Database

The panel uses no SQL database: its configuration is a JSON file
(`/etc/frp-manager/frp-manager.json`, readable by root only) and its state
another one (`/var/lib/frp-manager/state.json`). There is therefore no SQL
injection surface.
