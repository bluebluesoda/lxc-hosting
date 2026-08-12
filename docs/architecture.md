# Architecture

vpsmgr is a lightweight LXC hosting panel: one Debian 13 container per user,
managed from a web panel and a CLI. Everything ships as a single Go binary
(`vpsmgr` = CLI + embedded web panel); LXD, nftables and Traefik provide the
plumbing.

## Design goals

vpsmgr is a toy for small machines (≤ 4 GB RAM, small VPS). Storage and memory
are treated as scarce, which drives every choice in this document:

- the panel is a single static Go binary — the only new service vpsmgr adds
  besides LXD and Traefik;
- the storage pool is sparse (a loop file that only grows as it fills) and the
  published image is slimmed and the base image deleted, so disk is only
  consumed by what containers actually use, and clones share the image's
  blocks;
- container tooling stays minimal (`git` / `python3` are deliberately absent);
- live stats are batched into a handful of REST calls per refresh, and traffic
  sampling runs every 60 s, keeping idle CPU/RAM use low.

"Lightweight" refers to the panel and this storage/memory discipline, not to a
zero-overhead platform: LXD (snap), nftables and Traefik are the minimal
plumbing that makes the panel possible.

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
  the per-container `df` probe) still shells out to the CLI. Fractional CPU
  quotas (0.1..0.9) are enforced as `limits.cpu=1` plus
  `limits.cpu.allowance=<n>ms/100ms` — a one-core pin with a time slice.
- **nftables** — one table `inet vpsmgr`: DNAT (prerouting+output) for port
  ranges, MASQUERADE for NAT4. Reload applies the whole ruleset as **one atomic
  batch** (`delete table` + rules in a single `nft -f`, with an idempotent
  `nft add table` first to cover boot where the table is absent), so a bad rule
  can never leave tenants without NAT. Restored on boot by
  `vpsmgr-nft.service`.
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
- Add/Del/Reinstall are serialized by a per-process mutex, and `mgr.Add` rolls
  back the container, IPv6 route, nft rules and DB record on any post-launch
  failure. `mgr.Del` refuses to drop the DB row when the container cannot
  actually be removed (it would orphan the container and let `NextFreeIdx`
  reuse its IP for a new user); a fresh add also refuses a name/IP already
  claimed by a live LXD instance, so orphaned containers cannot cause bridge
  IP conflicts.
- Quotas: CPU (whole cores ≥ 1, or a fraction 0.1..0.9 of one core), memory
  (MiB), disk (GiB). Disk maps onto the ZFS quota and can only grow, never
  shrink.

## Storage

- The pool is ZFS. On first install, `10-lxd.sh` either adopts an existing
  pool, uses a spare whole-disk block device, or (no spare disk) creates a
  **sparse loop-file pool** sized to a share of the free space on `/`:
  80% by default, 90% when ≥ 20 GiB free. The loop file only allocates blocks
  as the pool actually fills. On very small hosts, cap the ZFS ARC
  (`zfs.arc_max`) so container memory keeps priority over the pool's cache.
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

## Optional RHEL-family image (`vpsmgr/alma-sshd`)

`60-rhel-image.sh` (NOT part of `install.sh`, run by the admin only when
wanted) builds an Alma 9 image — `rocky` builds Rocky 9 instead — with the same
hygiene: sshd + universal tooling via `dnf`, `dnf clean all` + cache/log
removal, base image deleted after publishing.

The reinstall dialog in the user panel enumerates every local `vpsmgr/*-sshd`
image (always offering Debian 13 first) and the user picks one; `mgr.Reinstall`
validates that a picked non-default image still exists. Containers run from
either base with the same light provisioning (random hostname, root password,
sshd enabled — the service is `sshd` on RHEL and `ssh` on Debian).

## Per-container provisioning

`mgr.Provision` runs inside every new or reinstalled container and:

1. Sets a **random hostname** (`vps-<8 hex>`, crypto/rand) — never the
   username, so users cannot identify each other from prompts/logs/banners on
   the internal network. Re-rolled on every reinstall.
2. Disables cloud-init hostname resets (`preserve_hostname: true`).
3. Sets the root password and ensures sshd is enabled/running.

## Container isolation on the bridge

All containers share the single `lxdbr0` L2 segment (10.42.0.0/24), so any
tenant can `nmap` the subnet and see every live container IP. To make sure a
scan does **not** reveal usernames:

- LXD's dnsmasq must NOT serve instance-name DNS: by default it publishes
  `<instance>.lxd` records (instance name = username) that turn into
  `username.lxd` PTR answers — this is independent of the randomized in-guest
  hostname, so it would leak usernames anyway. `10-lxd.sh` therefore sets
  `dns.mode=none` on `lxdbr0` (DHCP and upstream forwarding still work; the
  `search lxd` suffix is dropped and reverse lookups fall back to the random
  guest hostname or the upstream resolver).
- The in-guest hostname is already randomized (see above), so nothing
  username-derived is ever advertised on the wire.

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
