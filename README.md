# vpsmgr

极简小鸡面板：宿主 Ubuntu 24.04 + LXD 容器，每用户一台 Debian 13，通过宿主机端口段访问，80/443 按域名由 Traefik 转发。

## 安装

**最低要求：Ubuntu 24.04（物理机或 KVM） 1核心  1.5G内存 磁盘10G空闲 root**    
amd64 已测试 arm64 尚未测试理论支持

```
git clone https://github.com/bluebluesoda/lxc-hosting.git && cd lxc-hosting
sudo ./install.sh  # 通过预编译二进制安装
#sudo ./install.sh --local-build # 强制本地编译
```

装完运行 `vpsmgr panel-url` 查看完整面板地址，形如 `https://<IP>:8443/<path>`。该随机 path 是面板唯一入口。

## 使用

```
vpsmgr user add <name>               # 交互询问 CPU/内存/磁盘，回车用默认 1核/1024MiB/10GiB
vpsmgr user add <name> --cpu 2 --mem 2G --disk 20G
vpsmgr user update <name> [--cpu N] [--mem N] [--disk NG]   # 磁盘只许扩不许缩
vpsmgr user reset-passwd <name>      # 重置面板密码
vpsmgr user list                     # 列出用户及容器 CPU%/内存占用（瞬时）、本月上传/下载
vpsmgr user show <name>
vpsmgr user start|stop|restart <name>
vpsmgr user del <name>
vpsmgr panel-url
```

- 面板密码与 root 密码独立；改/重置密码会踢掉该用户其他登录会话
- 域名绑定后，80/443 按域名转发到容器 80/443

## 卸载

```
sudo ./uninstall.sh          # 卸载软件，保留容器与存储
sudo ./uninstall.sh --purge  # 连容器、存储池、LXD 一起删
```

## 设计要点

- 资源：用户 i=1..253，容器 IP 10.42.0.(i+1)，端口段 10000+(i-1)*50 共 50 个（第 1 个 SSH→22）
- 存储：ZFS 磁盘配额映射 ZFS quota，只许扩容。
- 网络：nftables 单表，DNAT（prerouting+output）+ MASQUERADE；reload 用 delete+apply 幂等；开机由 vpsmgr-nft.service 恢复。
- 反代：Traefik file provider 热加载，80 反代、443 SNI 直通，证书在容器内自管。
- 安全：面板挂在安装时生成的随机 path；修改操作仅 POST；会话 3 天、HttpOnly+Secure+SameSite=Lax；
- 流量统计：每容器网卡计数器由 LXD 提供，面板协程每 60s 采样，delta 累加进 SQLite
- 没有的功能：IPv6、容器隔离、限速/封禁（出站流量）、快照、Web 终端、域名归属校验、审计、多机、计费。

## 目录结构

```
install.sh / uninstall.sh / build.sh   # 安装 / 卸载 / 本地编译 bin/vpsmgr
scripts/   00-check 10-lxd 20-network 30-traefik 40-panel 50-image
configs/   参考配置（traefik / systemd）
src/       Go 源码（CLI + 面板单二进制）
```
