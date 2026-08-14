# <div align="center">⚠️ 此仓库已归档 / This repository has been archived</div>

<div align="center">

# 🚚 **项目已迁移到新仓库** / **The project has moved**

## **👉 [https://github.com/bluebluesoda/vpsmgr](https://github.com/bluebluesoda/vpsmgr)**

</div>

---

## 变更原因 / Why the move

本项目已从 **LXD** 运行时迁移到 **Incus 7 LTS**，这是一次**破坏性重构**（存储池、网桥、服务命名、权限模型、IPv6 直通方式全部变更），v0.3 及更早的部署**无法原地升级**。为了给新架构一个干净的历史起点，我们开立了新仓库 `vpsmgr`。

The project's runtime was migrated from **LXD to Incus 7 LTS** — a breaking rewrite (storage pool, bridge, service names, privilege model, and IPv6 pass-through all changed), and deployments on v0.3 or earlier **cannot be upgraded in place**. A new repository `vpsmgr` was created to give the new architecture a clean history.

## 你需要做什么 / What you need to do

- **新用户 / New users**：直接访问新仓库 [bluebluesoda/vpsmgr](https://github.com/bluebluesoda/vpsmgr) 获取最新版本。
- **老用户 / Existing users**：请在 v0.3.x 上继续使用，或在 `vpsmgr` 新仓库按文档**全新安装**（v0.3 → v1.0 无升级路径）。
- **Issues / PRs**：请提交到新仓库 [bluebluesoda/vpsmgr](https://github.com/bluebluesoda/vpsmgr)。

---

*本仓库（lxc-hosting）已归档，仅保留历史。This repository is archived for historical reference only.*
