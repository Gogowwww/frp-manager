# Docker: how it works, privileges and risks

**English** · [Français](docker.fr.md)

## What the container does

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

## Implications: root-equivalent access to the host

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
   IP filtering.

## Opening the panel to the network

A new install only listens on `127.0.0.1`. Three options:

- **SSH tunnel** (nothing to open): `ssh -L 8765:127.0.0.1:8765 user@server`,
  then `https://127.0.0.1:8765`;
- **Local reverse proxy** (nginx, Caddy) listening on the network and
  forwarding to `127.0.0.1:8765`; allow the WebSocket upgrade on `/ws/`;
- **Listen on the network**: uncomment `FRP_MANAGER_HOST=0.0.0.0` in
  `docker-compose.yml` (or set `bind_host` to `0.0.0.0` in Settings), restart,
  create the account right away and filter access.

An existing install (older than 0.0.51) keeps its listening address.

## Reducing privileges: current status

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
installing frp (Updates page), applying a firewall rule, deleting an instance.
If you validate a reduced configuration that works, a pull request is welcome.

**Safer alternative**: the standard install (`install.sh`) without Docker,
with the panel on `127.0.0.1` and access through an SSH tunnel or a VPN.
