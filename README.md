# Vpsmgr Lite

[简体中文](README.zh-CN.md)

A lightweight LXC virtual-machine management panel: host Ubuntu 24.04 + LXD containers, one Debian 13 VM per user. Users manage their machine from a web panel (start/stop/restart/reinstall), with automatic NAT4 port forwarding and 80/443 traffic proxied by Traefik per domain.

## Installation

**Minimum: Ubuntu 24.04 (bare metal or KVM), 1 core, 1.5G RAM, 10G free disk, root**  
amd64 tested · arm64 tested

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh                  # install via prebuilt binary
#sudo ./install.sh --local-build   # force a local build from source
```

After install run `vpsmgr panel-url` to see the full panel address, like `https://<IP>:8443/<path>`. That random path is the only entrance to the panel.

## Usage

```
vpsmgr add <name>               # interactive CPU/memory/disk prompts, Enter keeps defaults 1 core/1024MiB/10GiB
vpsmgr add <name> --cpu 2 --mem 2G --disk 20G
vpsmgr update <name> [--cpu N] [--mem N] [--disk NG]   # disk can only grow, never shrink
vpsmgr reset-passwd <name>      # reissue the panel password
vpsmgr list                     # users with container CPU%/memory (instant) and this month's up/down traffic
vpsmgr show <name>
vpsmgr start|stop|restart <name>
vpsmgr del <name>
vpsmgr panel-url
```

- The panel password and the root password are independent; changing/resetting the panel password kicks the user's other sessions.
- Once a domain is bound, 80/443 is forwarded per domain to the container's 80/443.

## Configuration

**The default config is not meant to be changed.**

The config file is at `/etc/vpsmgr/config.yaml` (auto-generated at install, root-only read/write). Use the `VPSMGR_CONFIG` env var for another path. Panel settings take effect on `systemctl restart vpsmgr-panel`; changes under `net` require re-running `vpsmgr install` to regenerate and push the firewall rules.

```
panel:
  listen: ":8443"              # panel listen address (change port or bind to e.g. "127.0.0.1:8443")
  cert: /etc/vpsmgr/panel.crt  # HTTPS certificate
  key: /etc/vpsmgr/panel.key   # private key
  db: /etc/vpsmgr/vpsmgr.db    # SQLite database
  public_ip: AUTO              # external IP, affects panel URL and SSH hints; auto-detected
  session_days: 3              # login session lifetime (days)
  url_path: AUTO               # random secret path, the only panel entrance; do not change after first install

net:
  subnet: "10.42.0.0/24"       # container subnet
  gateway: "10.42.0.1"
  port_base: 10000             # port range start
  ports_per_user: 50           # ports per user (first maps to container 22)
  ext_if: AUTO                 # external NIC, auto-detected from the default route

lxd:
  image: "vpsmgr/debian-sshd"
  image_fallback: "images:debian/13"
  pool: vpsmgr
  bridge: lxdbr0
```

- `panel.listen`, `panel.public_ip`, `net.ext_if` only affect display/on how the panel listens; `url_path`, `net.subnet`, `net.port_base`, `lxd.pool` etc. are bound to existing data — do not change them.
- Reference template: `configs/config.yaml.example`; full field docs in `src/internal/cfg/cfg.go`.

## Uninstall

```
sudo ./uninstall.sh          # remove the software, keep containers and storage
sudo ./uninstall.sh --purge  # also delete containers, storage pool and LXD
```

## Design notes

- Resources: user i=1..253, container IP 10.42.0.(i+1), port range 10000+(i-1)*50, 50 ports (first one maps to SSH 22).
- Storage: ZFS disk quota maps onto the ZFS quota; disk can only grow.
- Network: a single nftables table, DNAT (prerouting+output) + MASQUERADE; reload via idempotent delete+apply; restored on boot by vpsmgr-nft.service.
- Reverse proxy: Traefik file provider hot-reloads, 80 proxies, 443 SNI passthrough, the certificate is managed inside the container.
- Security: the panel sits behind a random path generated at install; state-changes are POST only; 3-day sessions with HttpOnly+Secure+SameSite=Lax; bare 404 for anything off-path (no fingerprint).
- Traffic accounting: per-container NIC counters come from LXD; a panel goroutine samples every 60s and accumulates deltas into SQLite.
- Not implemented: IPv6, container isolation, rate-limit/block (egress), snapshots, web terminal, domain ownership verification, audit, multi-host, billing.

## Layout

```
install.sh / uninstall.sh / build.sh   # install / uninstall / local build of bin/vpsmgr
scripts/   00-check 10-lxd 20-network 30-traefik 40-panel 50-image
configs/   reference configs (traefik / systemd)
src/       Go source (single binary: CLI + panel)
```