# kore-plugin-app-deploy

一键部署应用插件（**骨架**）：覆盖 WordPress / Laravel / Next.js 模板的一键部署方法面。本仓只交付协议骨架、模板白名单、目标路径校验与结构化结果，**不真拉取/部署应用、不内置任何应用模板二进制或模板下载器**（裁决 37 / 提案 §2.1 批 3）。

## 协议面

- `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`（生产级 12 之一）
- 能力声明：`system:admin`；可选依赖 `site` / `runtime-*` / `db-*`（未在 `depends_on` 中强制）
- 方法：`deploy.templates` / `deploy.create`（同时接受裸 `templates` / `create`）
- 握手的 stdout 留给协议帧，日志一律写 stderr

## 骨架口径

- `deploy.templates`：返回 `wordpress` / `laravel` / `nextjs` 三个白名单模板的元信息，不下载任何模板
- `deploy.create`：先校验模板名在白名单内，再校验目标路径在配置根目录（`KORE_APP_DEPLOY_ROOT`，缺省 `/var/lib/kore/app-deploy`）之内，返回结构化占位 `applied=false`

路径 / 模板非法返回 `E_INVALID`；未知方法返回 `E_NOTFOUND`。目标路径拒绝绝对路径、空路径、`.` / `..` 段、反斜杠与 NUL；只做字符串校验，**不执行任何文件写入**。

## 四类故障注入

`tests/fault/` 四个文件分别注入 kill -9、超时、磁盘满、cgroup 超限，断言方法级返回结构化错误，且 `Error.Code` 与故障类别一一对应：

| 故障 | 文件 | 期望错误码 |
| --- | --- | --- |
| kill -9 | `kill_test.go` | `E_UPSTREAM` |
| 超时 | `timeout_test.go` | `E_UPSTREAM` |
| 磁盘满 | `disk_test.go` | `E_INTERNAL` |
| cgroup 超限 | `cgroup_test.go` | `E_UNSUPPORTED` |

## 构建与验收

```bash
gofmt -l . && go vet ./... && go test -timeout 200s ./...
make build && make dist
tar -tzf dist/app-deploy-0.1.0.tar.gz | head -1   # info.json
```

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n`，chmod 归一），重复构建 sha256 不变；`size_bytes` 由提交前实测包体积回填源清单。
