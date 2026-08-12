# Configuration

The config file is at `/etc/vpsmgr/config.yaml` (auto-generated at install,
root-only read/write). Use the `VPSMGR_CONFIG` env var for another path.

**The default config is not meant to be changed.** Panel settings take effect
on `systemctl restart vpsmgr-panel`; changes under `net` require re-running
`vps install` to regenerate and push the firewall rules.

Reference template: `configs/config.yaml.example`.
Full field docs live in `src/internal/cfg/cfg.go`.

## Example

```yaml
panel:
  listen: ":5231"              # panel listen address — a FRESH install picks a random free port in 2000-9999
  cert: /etc/vpsmgr/panel.crt  # HTTPS certificate
  key: /etc/vpsmgr/panel.key   # private key
  db: /etc/vpsmgr/vpsmgr.db    # SQLite database
  public_ip: AUTO              # NIC IPv4 used by the firewall / routing; on NAT-ing clouds (AWS/Alibaba) this is a private address
  display_ip: AUTO             # public IPv4 for DISPLAY ONLY (panel URL, SSH hints); auto-fetched from ipv4.ip.sb when public_ip is private; empty = fall back to public_ip
  session_days: 3              # login session lifetime (days)
  url_path: AUTO               # random secret path, the only panel entrance; do not change after first install

net:
  subnet: "10.115.0.0/24"      # container subnet 10.<n>.0.0/24 — only the second octet is settable, at install
  gateway: "10.115.0.1"
  v4_forward: true             # IPv4 inbound policy (false = IPv6-only containers)
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

## Port scheme (fixed)

The port layout is fixed at install and immutable — there is no config knob:

- **Panel port**: random free port in `2000-9999`, chosen on a fresh install
  and stored in `panel.listen`. Change it in the config only if you know why.
- **SSH port**: each container gets one random port in `30000-31999` (TCP, DNAT
  to container `:22`). Shown as `ssh -p <port>`.
- **User ports**: each container owns a whole-hundred block of 100 ports in
  `10000-29999`, assigned deterministically (`10000+(idx-1)*100` .. `+99`), DNAT
  to the container (TCP and UDP). Displayed compactly as e.g. `107xx`.
- **Capacity**: at most **200** containers (200 blocks × 100 ports = 20000).

## IPv4 inbound policy (`v4_forward`)

`net.v4_forward` controls whether containers receive **shared IPv4 inbound**.
Asked at install (default `true`), but only offered when IPv6 pass-through is
enabled — with IPv6 off, IPv4 forwarding is mandatory (containers would
otherwise be unreachable).

- `true` (default): containers get the random SSH port + user port block (DNAT),
  and the domain proxy (traefik) is available.
- `false`: containers are **IPv6-only**. No SSH DNAT, no port-block DNAT, and
  traefik is stopped (domains are kept but not served; adding a domain is
  rejected until re-enabled). Containers still reach IPv4 outbound via the NAT4
  masquerade.

Toggle at runtime with `vps v4-forward on|off` — the rules are refreshed and
traefik started/stopped. The SSH/user ports stay recorded in the DB, so turning
it back on restores everything. The user panel hides IPv4 inbound info and shows
"v4 SSH unavailable" while off.

## Port 25 (SMTP) is always blocked

The host nftables ruleset drops **port 25 for all forwarded traffic, both
directions, TCP and UDP** (containers ⇄ internet, container ⇄ container). This
is a permanent anti-spam measure: there is no user toggle, and the rule only
goes away when the panel is uninstalled.

## Field notes

- `panel.listen`, `panel.public_ip`, `net.ext_if` only affect display/on how
  the panel listens.
- `url_path`, `net.subnet`, `lxd.pool` etc. are bound to existing data — do not
  change them after first install.
- `net.ipv6_subnet` must be a **global** (non-ULA) IPv6 CIDR with an explicit
  prefix length — a bare address is rejected, never silently assumed `/64`.
  Valid range is `/48`..`/80` (see [ipv6.md](ipv6.md)).
- `installed_version` / `uninstalled_version` are **auto-written** metadata,
  not to be edited by hand. `vps install` writes the binary version that
  installed (or adopted/upgraded) the config to `installed_version`; a
  non-purging `uninstall.sh` records the version being removed in
  `uninstalled_version` before deleting the binary. Both survive reinstall
  (`/etc/vpsmgr` is kept), so a future release that makes breaking changes can
  detect which version a config/db came from and migrate or warn.
- **v0.3 upgrade gate:** v0.3 makes breaking changes, so it refuses to adopt a
  config/db recorded as originating from an older release. `vps install`
  and `vps serve` both abort (and `install.sh` aborts early) when
  `installed_version` / `uninstalled_version` is missing or older than 0.3.0.
  A config with no recorded version is treated as too old — it predates the
  metadata fields. Existing 0.2.x installs must stay on v0.2.x until a
  migration path exists.
