# 配置 — 应用一键部署插件

## 能力声明（`info.json`）

- 协议：`kore.plugin.v1`；生命周期：`lazy`；深度等级见仓库 `info.json`。
- 能力：以 `info.json.capabilities` 与 `method_capabilities` 为准；读方法 `system:read`，写方法 `system:admin`。

## 配置优先级与存储

统一配置契约遵循 `kore/docs/PLUGIN-CONFIG.md`：`config.json` > 环境变量 > 代码默认值；配置文件位于 `<KORE_STATE_DIR>/config.json`，0600 原子写（临时文件 + fsync + rename）。保存后同一进程下一次调用立即生效（`restart_required=false`）；非法值返回 `E_INVALID` 且旧配置不变。

## 本插件可配项

模板均为本地常量，不联网、不下载：`ports[]` 从 0.4.0 起带 `suggested_host_port`，部署时从默认端口向上找第一个可用端口，且只绑 `127.0.0.1`。用户应用包单包 ≤ 32 MiB，逐条目拒绝绝对路径、`..`、盘符、反斜杠与链接；`upgrade` 先快照旧目录再换入，失败自动回滚。控制面 `stop` / `start` / `logs` 是可选能力，仅按部署记录中的容器名操作，不支持时返回 `E_UNSUPPORTED`。

## 状态目录

状态目录由环境变量指定（各插件不同，如 `KORE_APP_DEPLOY_STATE`、`KORE_STATE_DIR`），缺省一般位于 `<root>/state`。目录 0700、文件 0600；记录损坏或版本高于支持版本一律报错，不静默重置。
