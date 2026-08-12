# Vpsmgr Lite

**Warning ⚠️ Under active development — breaking changes may occur.**

[简体中文](README.zh-CN.md) · [Docs](docs/README.md)

A toy LXC hosting panel for small machines (≤ 4 GB RAM, small VPS): one Debian
13 container per user, managed from a web panel (start/stop/restart/reinstall),
with automatic NAT4 port forwarding and 80/443 per-domain proxying by Traefik.
Optional IPv6 pass-through (no NAT). The panel is a single small Go binary and
the container image stays slim — storage and memory are treated as scarce.

Install notes: with IPv6 enabled, the installer asks whether to keep shared IPv4
inbound (default yes; `no` makes containers IPv6-only). Port 25 (SMTP) is always
blocked for containers, both directions — anti-spam, no toggle.

## Install

**Minimum: Ubuntu 24.04 (bare metal or KVM), 1 core, 1.5G RAM, 10G free disk, and root access**

Both amd64 and arm64 are supported; testing has primarily been done on amd64.

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh                  # install the stable prebuilt binary
#sudo ./install.sh --local-build   # force a local build from source
```

To enable IPv6 pass-through, make sure the host has been assigned an entire
routed prefix. Ask your provider, or use the check script in this repository
for an informal test.

**Be sure the entire IPv6 prefix works before installing with IPv6 support.**

```
bash check-ipv6-support.sh # IPv6 test script
```

Run `vps panel-url` after installation to get the full panel address —
`https://<IP>:<port>/<random-path>` (the port is a random free one in
2000-9999). This random path is the panel's only entry point.

## Optional: extra OS images

The default is Debian 13. To let users reinstall their container with a
RHEL-family system, run (once, as root) the optional image builder — it is NOT
run by `install.sh` so small boxes stay lean:

```
sudo bash scripts/60-rhel-image.sh          # Alma 9
sudo bash scripts/60-rhel-image.sh rocky     # Rocky 9
```

Reinstall then offers the built images as a choice (always, even with only the
default). The image is slimmed and the base image deleted, same as the Debian
one.

## Usage

```
vps add <name> [--cpu N] [--mem NG] [--disk NG]   # default 1 core / 1G / 10G; cpu = whole cores (>=1) or a decimal 0.1..0.9; password is auto-generated and shown once
vps update <name> [--cpu N] [--mem N] [--disk NG] # disk can only grow
vps reset-passwd <name>
vps list
vps show <name>
vps start|stop|restart <name>
vps del <name>
vps panel-url
vps v4-forward on|off   # shared IPv4 inbound: off = IPv6-only containers
```

Users can set a custom **init script** in their panel — it runs as root inside
their container after a reinstall (output at `/var/log/vpsmgr-init.log`), for
cloud-provider-style first-boot automation.

Admins can set a per-user monthly **traffic quota** (GiB, upload + download);
a container that exceeds it is rate-limited to **1Mbps** both directions. The
limit is applied live via LXD (tc qdiscs) — no container restart.

## Config

`/etc/vpsmgr/config.yaml` (auto-generated at install) — **the defaults are not
meant to be changed**. Reference: [docs/configuration.md](docs/configuration.md).

## Uninstall

```
sudo ./uninstall.sh          # remove software, keep config/db/containers
sudo ./uninstall.sh --purge  # also delete config/db, containers, pool, LXD
```

## Documentation

Technical detail lives in `docs/` (English): [index](docs/README.md), plus
[`AGENTS.md`](AGENTS.md) for AI coding agents.
