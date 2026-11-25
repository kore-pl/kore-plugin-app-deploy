# 使用方法 — 应用一键部署插件

## 协议面

协议 `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`，能力声明 `system:admin`。共 11 个业务方法，域必须等于插件 id `app-deploy`：模板部署 `templates` / `deploy` / `list` / `remove`，用户应用包 `upload` / `deployFromArtifact` / `upgrade` / `jobs`，部署后管理 `stop` / `start` / `logs`。握手 stdout 留给协议帧，日志一律写 stderr。

## 核心能力

模板均为本地常量，不联网、不下载：`ports[]` 从 0.4.0 起带 `suggested_host_port`，部署时从默认端口向上找第一个可用端口，且只绑 `127.0.0.1`。用户应用包单包 ≤ 32 MiB，逐条目拒绝绝对路径、`..`、盘符、反斜杠与链接；`upgrade` 先快照旧目录再换入，失败自动回滚。控制面 `stop` / `start` / `logs` 是可选能力，仅按部署记录中的容器名操作，不支持时返回 `E_UNSUPPORTED`。

## 典型流程

1. 握手：调用 `kore.ping` 确认插件存活（`kore.plugin.v1`）。
2. 读操作：调用本插件的只读方法（列表 / 详情 / 探测 / 查询），确认返回结构。
3. 写操作：确认 `info.json` 已声明对应能力，按方法要求提供显式确认参数（如 `confirm: true`）。
4. 失败处理：错误以结构化 `{"code","message"}` 返回；按 `code` 分流，不解析中文文案。

## 安全边界

模板白名单确保镜像只来自源码常量，`deploy` 只接受模板 id；名称必须以 `kore-app-` 开头且匹配 `^[a-z][a-z0-9-]{0,30}$`；环境变量只接受模板声明的键，真口令走 0600 `--env-file` 且不进 argv；删除需 `confirm:true` 加部署记录且复核完整容器 ID，绝不删别人的容器；每个容器都带 `--memory` 与等值 `--memory-swap` 防 OOM；bind 卷只落在 `<state>/volumes/` 内。
