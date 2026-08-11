# Architecture

vpsmgr is a lightweight LXC hosting panel: one Debian 13 container per user,
managed from a web panel and a CLI. Everything ships as a single Go binary
(`vpsmgr` = CLI + embedded web panel); LXD, nftables and Traefik provide the
plumbing.

## Components

```
install.sh / uninstall.sh / build.sh   # lifecycle + local build
scripts/  00-check 10-lxd 20-network 30-traefik 40-panel 50-image
configs/  reference configs (traefik / systemd)
src/      Go source (single binary: CLI + panel)
```

- **Go binary** — CLI commands (`vpsmgr add/show/del/...`) and the HTTPS panel
  (`vpsmgr-panel.service`). The web templates are embedded (`//go:embed`).
- **LXD** (snap) — runs the containers. Storage pool `vpsmgr` (ZFS), bridge
  `lxdbr0` (10.42.0.0/24). The panel talks to the daemon over its **Unix-socket
  REST API** (`internal/lx`, one reusable HTTP connection, no `lxc` process
  spawn per call); only `lxc exec` (provisioning scripts, the readiness probe,
  the per-container `df` probe) still shells out to the CLI.
- **nftables** — one table `inet vpsmgr`: DNAT (prerouting+output) for port
  ranges, MASQUERADE for NAT4. Reload is idempotent (delete+apply). Restored
  on boot by `vpsmgr-nft.service`.
- **UFW** — vpsmgr manages its firewall through its own nftables table, so the
  installer **disables UFW** when it is active: UFW's default-DROP policy runs
  before LXD's `table inet lxd` rules and silently kills container IPv4 (no
  DHCP, no DNS, no forwarded traffic), which makes both the image build and
  every container's network fail. This is a known LXD issue:
  <https://canonical.com/lxd/docs/latest/howto/network_bridge_firewalld/>.
  If you keep UFW, you must add these rules yourself (see `00-check.sh`);
  note that UFW's `route allow` is IPv4-only:

  ```sh
  ufw allow in on lxdbr0 to any port 67 proto udp   # DHCP
  ufw allow in on lxdbr0 to any port 53             # DNS
  ufw route allow in on lxdbr0 from 10.42.0.0/24    # container forwarding
  ```
- **Traefik** — file provider, hot-reloads `/etc/traefik/dynamic`. Port 80
  proxies per domain; 443 SNI passthrough (TLS is managed inside the container).
- **SQLite** — users, domains, sessions, traffic counters. Located at
  `/etc/vpsmgr/vpsmgr.db`.

## Users and resources

- User `i` (1..253): container IP `10.42.0.(i+1)`, port range
  `10000 + (i-1)*50`, 50 ports, the first maps to container SSH port 22.
- Quotas: CPU cores, memory (MiB), disk (GiB). Disk maps onto the ZFS quota
  and can only grow, never shrink.

## Storage

- The pool is ZFS. On first install, `10-lxd.sh` either adopts an existing
  pool, uses a spare whole-disk block device, or (no spare disk) creates a
  **sparse loop-file pool** sized to a share of the free space on `/`:
  80% by default, 90% when ≥ 20 GiB free. The loop file only allocates blocks
  as the pool actually fills.
- Containers are ZFS clones of the image: the image's blocks are shared
  (copy-on-write), so a well-provisioned image costs one copy no matter how
  many containers. Because LXD's `refquota` counts inherited blocks, image
  bloat also eats into every container's disk quota — images must stay slim.

## Image (`vpsmgr/debian-sshd`)

Built by `50-image.sh` from `images:debian/13` (fallback `images:debian/trixie`):

- Installs `openssh-server` plus universal user tooling: `curl`, `wget`,
  `ca-certificates`, `less`, `bind9-dnsutils`, `openssh-client`, `unzip`,
  `nano`. (`ca-certificates` is essential — without it all HTTPS fails.)
- Slims the image before publishing: `apt-get clean`, removes
  `/var/lib/apt/lists/*`, logs and tmp. Without this the image balloons
  ~50 MiB+ in apt lists alone.
- Deletes the Debian base image afterwards — it is only a build intermediate;
  the runtime fallback for containers is the remote `images:debian/13`.
- `git` / `python3` are deliberately **not** baked in (heavy / opinionated).

## Per-container provisioning

`mgr.Provision` runs inside every new or reinstalled container and:

1. Sets a **random hostname** (`vps-<8 hex>`, crypto/rand) — never the
   username, so users cannot identify each other from prompts/logs/banners on
   the internal network. Re-rolled on every reinstall.
2. Disables cloud-init hostname resets (`preserve_hostname: true`).
3. Sets the root password and ensures sshd is enabled/running.

## Security model

- Panel lives behind a random secret `url_path`; everything off-path returns a
  bare, headerless 404 (no fingerprint, no auth cost).
- Mutating actions are POST-only; sessions are 3-day HttpOnly+Secure+
  SameSite=Lax cookies; a per-IP login rate limiter.
- Containers are LXD-unprivileged with `security.nesting=true`.

## Traffic accounting

Per-container NIC counters come from LXD. A background goroutine in the panel
samples every 60 s and accumulates deltas into SQLite (`traffic` table). The
panel reads totals from the DB — it never blocks on `lxc` for traffic.

## Install / uninstall lifecycle

- `uninstall.sh` without `--purge` removes the software but **keeps**
  `/etc/vpsmgr` (config/db/certs) and `/etc/traefik`, so a reinstall adopts
  the previous users/domains/settings. `--purge` removes those plus
  containers, the storage pool and the LXD snap.
- `install.sh` detects an existing `/etc/vpsmgr/config.yaml` and adopts it
  (users/domains survive). `00-ipv6-ask.sh` reuses a previously configured
  `ipv6_subnet` instead of re-asking.
- `install.sh --local-build` prints the current git branch and waits 10 s
  (Ctrl-C to abort) before starting, and always rebuilds rather than reusing
  an installed stable binary.

See [ipv6.md](ipv6.md) for the optional IPv6 pass-through feature.
