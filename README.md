# Vpsmgr Lite

[简体中文](README.zh-CN.md) · [Docs](docs/README.md)

A lightweight LXC hosting panel: Ubuntu 24.04 host + LXD containers, one
Debian 13 VM per user. Users manage their machine from a web panel
(start/stop/restart/reinstall), with automatic NAT4 port forwarding and
80/443 per-domain proxying by Traefik. Optional IPv6 pass-through (no NAT).

## Install

**Minimum: Ubuntu 24.04 (bare metal or KVM), 1 core, 1.5G RAM, 10G free disk, root** · amd64/arm64 tested

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh                  # install via prebuilt binary
#sudo ./install.sh --local-build   # force a local build from source
```

Run `vpsmgr panel-url` after install to get the full panel address —
`https://<IP>:8443/<random-path>`. That path is the only panel entrance.

## Usage

```
vpsmgr add <name> [--cpu N] [--mem NG] [--disk NG]   # default 1 core / 1G / 10G
vpsmgr update <name> [--cpu N] [--mem N] [--disk NG] # disk can only grow
vpsmgr reset-passwd <name>      vpsmgr list          vpsmgr show <name>
vpsmgr start|stop|restart <name>   vpsmgr del <name>   vpsmgr panel-url
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

## Layout

```
install.sh / uninstall.sh / build.sh   # install / uninstall / local build
scripts/   00-check 10-lxd 20-network 30-traefik 40-panel 50-image
configs/   reference configs (traefik / systemd)
docs/      technical documentation (English)
src/       Go source (single binary: CLI + panel)
```
