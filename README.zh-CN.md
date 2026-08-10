# Vpsmgr Lite

[English](README.md) · [文档](docs/README.md)

轻量的 LXC 托管面板：宿主机 Ubuntu 24.04 + LXD 容器，每用户一台 Debian 13，用户通过 Web 面板管理机器（开机/关机/重启/重装），自动 NAT4 端口转发，80/443 按域名由 Traefik 转发。可选 IPv6 直通（无 NAT）。

## 安装

**最低要求：Ubuntu 24.04（物理机或 KVM）1 核心 1.5G 内存 10G 磁盘空闲 root** · amd64/arm64 已测试

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh                  # 通过预编译二进制安装
#sudo ./install.sh --local-build   # 强制本地编译
```

装完运行 `vpsmgr panel-url` 查看完整面板地址——`https://<IP>:8443/<随机路径>`。该随机路径是面板唯一入口。

## 使用

```
vpsmgr add <name> [--cpu N] [--mem NG] [--disk NG]   # 默认 1核/1G/10G
vpsmgr update <name> [--cpu N] [--mem N] [--disk NG] # 磁盘只许扩
vpsmgr reset-passwd <name>      vpsmgr list          vpsmgr show <name>
vpsmgr start|stop|restart <name>   vpsmgr del <name>   vpsmgr panel-url
```

## 配置

`/etc/vpsmgr/config.yaml`（安装时自动生成）——**默认配置不建议修改**。参考：[docs/configuration.md](docs/configuration.md)。

## 卸载

```
sudo ./uninstall.sh          # 卸载软件，保留 config/db/容器
sudo ./uninstall.sh --purge  # 连 config/db、容器、存储池、LXD 一起删
```

## 文档

技术细节在 `docs/`（英文）：[索引](docs/README.md)；另有面向 AI 编程代理的 [`AGENTS.md`](AGENTS.md)。

## 目录结构

```
install.sh / uninstall.sh / build.sh   # 安装 / 卸载 / 本地编译
scripts/   00-check 10-lxd 20-network 30-traefik 40-panel 50-image
configs/   参考配置（traefik / systemd）
docs/      技术文档（英文）
src/       Go 源码（CLI + 面板单二进制）
```
