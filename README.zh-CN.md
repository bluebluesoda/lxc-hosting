# Vpsmgr Lite

[English](README.md)

轻量的 LXC 虚拟机管理面板：宿主机 Ubuntu 24.04 + LXD 容器，每用户一台 Debian 13，用户通过 Web 面板进行基础管理（重启、重装等），自动 NAT4 端口转发，80/443 按域名由 Traefik 转发。

## 安装

**最低要求：Ubuntu 24.04（物理机或 KVM）1 核心 1.5G 内存 磁盘 10G 空闲 root**
amd64 已测试 · arm64 已测试

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh                  # 通过预编译二进制安装
#sudo ./install.sh --local-build   # 强制本地编译
```

装完运行 `vpsmgr panel-url` 查看完整面板地址，形如 `https://<IP>:8443/<path>`。该随机 path 是面板唯一入口。

## 使用

```
vpsmgr add <name>               # 交互询问 CPU/内存/磁盘，回车用默认 1核/1024MiB/10GiB
vpsmgr add <name> --cpu 2 --mem 2G --disk 20G
vpsmgr update <name> [--cpu N] [--mem N] [--disk NG]   # 磁盘只许扩不许缩
vpsmgr reset-passwd <name>      # 重置面板密码
vpsmgr list                     # 列出用户及容器 CPU%/内存占用（瞬时）、本月上传/下载
vpsmgr show <name>
vpsmgr start|stop|restart <name>
vpsmgr del <name>
vpsmgr panel-url
```

- 面板密码与 root 密码独立；改/重置面板密码会踢掉该用户其他登录会话
- 域名绑定后，80/443 按域名转发到容器的 80/443

## 配置

**默认配置不建议修改**

配置文件在 `/etc/vpsmgr/config.yaml`（安装时自动生成，仅 root 可读写）。可用环境变量 `VPSMGR_CONFIG` 指定其它路径。面板设置改完执行 `systemctl restart vpsmgr-panel` 生效；`net` 段改动需执行 `vpsmgr install` 重新生成并下发防火墙规则。

```
panel:
  listen: ":8443"              # 面板监听地址（改端口或绑 IP 如 "127.0.0.1:8443"）
  cert: /etc/vpsmgr/panel.crt  # HTTPS 证书
  key: /etc/vpsmgr/panel.key   # 私钥
  db: /etc/vpsmgr/vpsmgr.db    # SQLite 数据库
  public_ip: AUTO              # 对外 IP，影响面板 URL 与 SSH 提示；自动检测
  session_days: 3              # 登录会话有效期（天）
  url_path: AUTO               # 随机 secret path，面板唯一入口；首次安装生成后勿改

net:
  subnet: "10.42.0.0/24"       # 容器子网
  gateway: "10.42.0.1"
  port_base: 10000             # 端口段起始
  ports_per_user: 50           # 每用户端口数（首个映射容器 22）
  ext_if: AUTO                 # 外网网卡，自动检测默认路由

lxd:
  image: "vpsmgr/debian-sshd"
  image_fallback: "images:debian/13"
  pool: vpsmgr
  bridge: lxdbr0
```

- `panel.listen`、`panel.public_ip`、`net.ext_if` 等仅影响面板展示/监听；`url_path`、`net.subnet`、`net.port_base`、`lxd.pool` 等与已有数据绑定，勿随意修改
- 参考模板：`configs/config.yaml.example`；完整字段说明见 `src/internal/cfg/cfg.go`

## 卸载

```
sudo ./uninstall.sh          # 卸载软件，保留容器与存储
sudo ./uninstall.sh --purge  # 连容器、存储池、LXD 一起删
```

## 设计要点

- 资源：用户 i=1..253，容器 IP 10.42.0.(i+1)，端口段 10000+(i-1)*50 共 50 个（第 1 个 SSH→22）
- 存储：ZFS 磁盘配额映射 ZFS quota，只许扩大。
- 网络：nftables 单表，DNAT（prerouting+output）+ MASQUERADE；reload 用 delete+apply 幂等；开机由 vpsmgr-nft.service 恢复。
- 反代：Traefik file provider 热加载，80 反代、443 SNI 直通，证书在容器内自管。
- 安全：面板挂在安装时生成的随机 path；修改操作仅 POST；会话 3 天、HttpOnly+Secure+SameSite=Lax；路径外一律裸 404（无指纹）。
- 流量统计：每容器网卡计数器由 LXD 提供，面板协程每 60s 采样，增量累加到 SQLite。
- 未实现：IPv6、容器隔离、限速/封禁（出站流量）、快照、Web 终端、域名归属校验、审计、多机、计费。

## 目录结构

```
install.sh / uninstall.sh / build.sh   # 安装 / 卸载 / 本地编译 bin/vpsmgr
scripts/   00-check 10-lxd 20-network 30-traefik 40-panel 50-image
configs/   参考配置（traefik / systemd）
src/       Go 源码（CLI + 面板单二进制）
```