# 应用一键部署插件（kore-plugin-app-deploy）

## 简介

本插件提供真实的一键应用部署：内置 9 个模板（含 Node.js / Python / PHP / MySQL+phpMyAdmin / Uptime Kuma），通过宿主机的 docker CLI（固定 argv、不经 shell）拉起容器。部署记录落 `<state>/deployments.json`（0600），`list` 关联 `docker ps` 查询真实运行状态并附带资源占用概览；部署后可直接停止、启动、查看日志；`remove` 必须显式 `confirm=true`，且只删除本插件创建的容器。

## 协议面

协议 `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`，能力声明 `system:admin`。共 11 个业务方法，域必须等于插件 id `app-deploy`：模板部署 `templates` / `deploy` / `list` / `remove`，用户应用包 `upload` / `deployFromArtifact` / `upgrade` / `jobs`，部署后管理 `stop` / `start` / `logs`。握手 stdout 留给协议帧，日志一律写 stderr。

## 核心能力

模板均为本地常量，不联网、不下载：`ports[]` 从 0.4.0 起带 `suggested_host_port`，部署时从默认端口向上找第一个可用端口，且只绑 `127.0.0.1`。用户应用包单包 ≤ 32 MiB，逐条目拒绝绝对路径、`..`、盘符、反斜杠与链接；`upgrade` 先快照旧目录再换入，失败自动回滚。控制面 `stop` / `start` / `logs` 是可选能力，仅按部署记录中的容器名操作，不支持时返回 `E_UNSUPPORTED`。

## 安全边界

模板白名单确保镜像只来自源码常量，`deploy` 只接受模板 id；名称必须以 `kore-app-` 开头且匹配 `^[a-z][a-z0-9-]{0,30}$`；环境变量只接受模板声明的键，真口令走 0600 `--env-file` 且不进 argv；删除需 `confirm:true` 加部署记录且复核完整容器 ID，绝不删别人的容器；每个容器都带 `--memory` 与等值 `--memory-swap` 防 OOM；bind 卷只落在 `<state>/volumes/` 内。

## 构建与验证

日常验证用本地 `make check`（gofmt → go vet → go test -timeout 200s）；发布前 `make dist` 连跑两次核对 sha256 一致（归档 `--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n`）。真 docker E2E 默认跳过，`make e2e` 开启后缺 docker 会失败而非跳过。状态目录由 `KORE_APP_DEPLOY_STATE` 指定，缺省 `<root>/state`；非 Linux 平台返回 `E_UNSUPPORTED`。

## 相关链接

- [源码](https://github.com/kore-pl/kore-plugin-app-deploy)
- [kore 内核与协议](https://github.com/kore-pl/kore)
- [插件索引](https://github.com/kore-pl/kore-plugins)
