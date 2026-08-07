# vpsmgr

极简小鸡面板：宿主 Ubuntu 24.04 + LXD 容器，每用户一台 Debian 13，通过宿主机端口段访问，80/443 按域名由 Traefik 转发。不做多租户隔离、计费、滥用防护。

## 安装

前置：Ubuntu 24.04（物理机或 KVM）、2C / 2G、根分区 10G 空闲、root、可访问外网。

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh        # 本地编译 Go + 装 LXD/Traefik/面板，幂等可重跑
```

装完访问 `https://<IP>:8443`（自签证书，浏览器提示后继续）。

## 使用

```
vpsmgr user add <name>               # 交互询问 CPU/内存/磁盘，回车用默认 1核/1024MiB/10GiB
vpsmgr user add <name> --cpu 2 --mem 2G --disk 20G
vpsmgr user update <name> [--cpu N] [--mem N] [--disk NG]   # 磁盘只许扩不许缩
vpsmgr user reset-passwd <name>      # 面板密码重置为随机密码（显示一次）
vpsmgr user list                     # 列出用户及容器 CPU%/内存占用
vpsmgr user show <name>
vpsmgr user start|stop|restart <name>
vpsmgr user del <name>
vpsmgr panel-url
```

- 第 1 个用户 SSH：`ssh -p 10000 root@<宿主机IP>`
- 配额校验：CPU>=1 整数，内存>=64 MiB 整数，磁盘>=1 GiB 整数
- 面板：登录、电源、重装（数据全丢，IP/端口/域名不变）、改面板密码（>14 位）、重置 root 密码（随机 20 位显示一次）、域名增删
- 面板密码与 root 密码独立；改/重置密码会踢掉该用户其他登录会话
- 域名绑定后，80/443 按域名转发到容器 80/443，证书在容器内自签

## 卸载

```
sudo ./uninstall.sh          # 卸载软件，保留容器与存储
sudo ./uninstall.sh --purge  # 连容器、存储池、LXD 一起删
```

## 设计要点

- 资源：用户 i=1..253，容器 IP 10.42.0.(i+1)，端口段 10000+(i-1)*50 共 50 个（第 1 个 SSH→22），上限 22649 不碰临时端口；删用户后序号/端口复用。
- 存储：ZFS（空闲盘或 loop 文件），不可用退 dir（配额失效）；add 时池使用率>=90% 拒绝；磁盘配额映射 ZFS quota，只许扩容。
- 网络：nftables 单表，DNAT（prerouting+output）+ MASQUERADE；reload 用 delete+apply 幂等；开机由 vpsmgr-nft.service 恢复。
- 反代：Traefik file provider 热加载，80 反代、443 SNI 直通，证书在容器内自管。
- 安全：修改操作仅 POST 防 CSRF；会话 3 天、HttpOnly+Secure+SameSite=Lax；域名严格白名单防 YAML 注入；登录限速每 IP 每分钟 5 次；所有校验在服务端。
- 明确不做：IPv6、容器隔离、限速/封禁（出站流量）、快照、Web 终端、域名归属校验、审计、多机、计费。

## 目录结构

```
install.sh / uninstall.sh / build.sh   # 安装 / 卸载 / 本地编译 bin/vpsmgr
scripts/   00-check 10-lxd 20-network 30-traefik 40-panel 50-image
configs/   参考配置（traefik / systemd）
src/       Go 源码（CLI + 面板单二进制）
```
