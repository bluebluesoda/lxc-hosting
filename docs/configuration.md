# Configuration

The config file is at `/etc/vpsmgr/config.yaml` (auto-generated at install,
root-only read/write). Use the `VPSMGR_CONFIG` env var for another path.

**The default config is not meant to be changed.** Panel settings take effect
on `systemctl restart vpsmgr-panel`; changes under `net` require re-running
`vpsmgr install` to regenerate and push the firewall rules.

Reference template: `configs/config.yaml.example`.
Full field docs live in `src/internal/cfg/cfg.go`.

## Example

```yaml
panel:
  listen: ":8443"              # panel listen address (change port or bind to e.g. "127.0.0.1:8443")
  cert: /etc/vpsmgr/panel.crt  # HTTPS certificate
  key: /etc/vpsmgr/panel.key   # private key
  db: /etc/vpsmgr/vpsmgr.db    # SQLite database
  public_ip: AUTO              # NIC IPv4 used by the firewall / routing; on NAT-ing clouds (AWS/Alibaba) this is a private address
  display_ip: AUTO             # public IPv4 for DISPLAY ONLY (panel URL, SSH hints); auto-fetched from ipv4.ip.sb when public_ip is private; empty = fall back to public_ip
  session_days: 3              # login session lifetime (days)
  url_path: AUTO               # random secret path, the only panel entrance; do not change after first install

net:
  subnet: "10.42.0.0/24"       # container subnet
  gateway: "10.42.0.1"
  port_base: 10000             # port range start
  ports_per_user: 50           # ports per user (first maps to container 22)
  ext_if: AUTO                 # external NIC, auto-detected from the default route
  ipv6_subnet: ""              # optional: global prefix for IPv6 pass-through, e.g. "2602:fada:6::/64"
                               # (/64 or shorter, incl. provider /80 slices like
                               # "2406:da14:1dd2:a807:753a::/80"); empty = disabled (default)

lxd:
  image: "vpsmgr/debian-sshd"
  image_fallback: "images:debian/13"
  pool: vpsmgr
  bridge: lxdbr0
  socket: "/var/snap/lxd/common/lxd/unix.socket"   # LXD daemon Unix socket (REST API)
```

## Field notes

- `panel.listen`, `panel.public_ip`, `net.ext_if` only affect display/on how
  the panel listens.
- `url_path`, `net.subnet`, `net.port_base`, `lxd.pool` etc. are bound to
  existing data — do not change them after first install.
- `net.ipv6_subnet` must be a **global** (non-ULA) IPv6 CIDR with an explicit
  prefix length — a bare address is rejected, never silently assumed `/64`.
  Valid range is `/48`..`/80` (see [ipv6.md](ipv6.md)).
- `installed_version` / `uninstalled_version` are **auto-written** metadata,
  not to be edited by hand. `vpsmgr install` writes the binary version that
  installed (or adopted/upgraded) the config to `installed_version`; a
  non-purging `uninstall.sh` records the version being removed in
  `uninstalled_version` before deleting the binary. Both survive reinstall
  (`/etc/vpsmgr` is kept), so a future release that makes breaking changes can
  detect which version a config/db came from and migrate or warn.
