# Vpsmgr Lite
**警告⚠️ 仍在开发中，可能会有破坏性变更**

[English](README.md) · [文档](docs/README.md)

面向小型机器（≤ 4G 内存、小型 VPS）的玩具级 LXC 托管面板：每用户一台 Debian 13，用户通过 Web 面板管理机器（开机/关机/重启/重装），自动 NAT4 端口转发，80/443 按域名由 Traefik 转发。可选 IPv6 直通（无 NAT）。面板是一个很小的 Go 单二进制，容器镜像保持精简——存储和内存都视作稀缺资源。

## 安装

**最低要求：Ubuntu 24.04（物理机或 KVM）1 核心 1.5G 内存 10G 磁盘空闲 root**

amd64/arm64 均可，主要在amd64上进行了测试

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh                  # 安装稳定版预编译二进制
#sudo ./install.sh --local-build   # 强制本地编译
```

**如需启用IPv6直通，请确保宿主机获得整段Route**，可以询问服务商，或者使用仓库中的检查脚本进行不严谨的测试。
**务必在测试v6整段可用后再尝试以v6支持进行安装**
```
bash check-ipv6-support.sh #v6测试脚本
```


装完运行 `vpsmgr panel-url` 查看完整面板地址——`https://<IP>:8443/<随机路径>`。该随机路径是面板唯一入口。

## 可选：额外系统镜像

默认系统为 Debian 13。想让用户重装容器时选 RHEL 系系统，可以（以 root）运行一次可选的镜像构建脚本——它不会在 `install.sh` 里自动执行，小型机器保持精简：

```
sudo bash scripts/60-rhel-image.sh          # Alma 9
sudo bash scripts/60-rhel-image.sh rocky     # Rocky 9
```

之后重装弹窗会列出这些镜像供选择（即使只有默认镜像也会让用户选）。镜像构建与 Debian 一致：精简缓存、发布后删除基础镜像。

## 使用

```
vpsmgr add <name> [--cpu N] [--mem NG] [--disk NG]   # 默认 1核/1G/10G；cpu 可为整数核（≥1）或 0.1~0.9 小数；密码自动生成，仅显示一次
vpsmgr update <name> [--cpu N] [--mem N] [--disk NG] # 磁盘只许扩
vpsmgr reset-passwd <name>
vpsmgr list
vpsmgr show <name>
vpsmgr start|stop|restart <name>
vpsmgr del <name>
vpsmgr panel-url
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

