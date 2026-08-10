# Vpsmgr Lite

[简体中文](README.zh-CN.md) · [Docs](docs/README.md)

A lightweight LXC hosting panel: one Debian 13 container per user. Users
manage their machine from a web panel (start/stop/restart/reinstall), with
automatic NAT4 port forwarding and 80/443 per-domain proxying by Traefik.
Optional IPv6 pass-through (no NAT).

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

Run `vpsmgr panel-url` after installation to get the full panel address —
`https://<IP>:8443/<random-path>`. This random path is the panel's only entry
point.

## Usage

```
vpsmgr add <name> [--cpu N] [--mem NG] [--disk NG]   # default 1 core / 1G / 10G
vpsmgr update <name> [--cpu N] [--mem N] [--disk NG] # disk can only grow
vpsmgr reset-passwd <name>
vpsmgr list
vpsmgr show <name>
vpsmgr start|stop|restart <name>
vpsmgr del <name>
vpsmgr panel-url
```

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
