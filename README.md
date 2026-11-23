# kore-plugin-app-deploy

一键部署应用插件（**真实部署**）：内置 4 个模板，通过本机 docker CLI（固定 argv、**不经 shell**）拉起容器；部署记录落 `<state>/deployments.json`（0600），`list` 关联 `docker ps` 查真实状态，`remove` 必须 `confirm` 且**只删本插件创建的容器**。

## 协议面

- `kore.plugin.v1`，`lifecycle=lazy`，`depth_level=production`（生产级 12 之一）
- 能力声明：`system:admin`（需要本机 docker CLI 与 daemon 访问权限）
- 方法（8 个业务方法，域必须等于插件 id `app-deploy`，PLUGIN-PROTOCOL §7.2）：
  - 模板部署：`app-deploy.templates` / `app-deploy.deploy` / `app-deploy.list` / `app-deploy.remove`
  - 用户应用包（0.3.0）：`app-deploy.upload` / `app-deploy.deployFromArtifact` / `app-deploy.upgrade` / `app-deploy.jobs`
- 握手的 stdout 留给协议帧，日志一律写 stderr
- **0.2.0 破坏性变更**：`app-deploy.create`（骨架占位）移除，由 `app-deploy.deploy` 取代；模板集合由 `wordpress/laravel/nextjs` 骨架改为 4 个可真实部署的模板（minor bump，无存量用户不做兼容层）

## 内置模板（本地常量，不联网、不下载）

| id | 镜像 | 容器 | 默认宿主端口 | 卷 | 必填 env | 内存限额 |
| --- | --- | --- | --- | --- | --- | --- |
| `nginx-static` | `nginx:alpine` | 1（web） | 18090→80 | `html`（bind，可直接放静态文件） | — | 128 MiB |
| `wordpress` | `wordpress:6` + `mysql:8` | 2（web + db） | 18091→80 | `html` / `data`（命名卷） | —（口令留空自动生成） | 512 + 512 MiB |
| `redis` | `redis:7-alpine` | 1（server） | 16379→6379 | `data`（命名卷，AOF） | — | 256 MiB |
| `postgres` | `postgres:16-alpine` | 1（db） | 15432→5432 | `data`（命名卷） | `POSTGRES_PASSWORD` | 512 MiB |

`templates` 每项返回 `{id, name, description, image, ports[], volumes[], env[{key,label,required,default,help}], estimated_memory_mb}`，另带 `services[]`（多容器模板的服务清单：镜像 + 内存限额）与 `env[].secret`（是否口令、留空是否自动生成）。

## 方法语义

```bash
# 模板目录
{"method":"app-deploy.templates","params":{}}
# → {"templates":[...],"count":4}

# 真部署（name 即容器名，必须以 kore-app- 开头）
{"method":"app-deploy.deploy",
 "params":{"template":"nginx-static","name":"kore-app-test",
           "ports":[{"container_port":80,"host_port":20000}],
           "env":{},"volumes":[{"name":"html","subdir":"site-1"}]}}
# → {"deployed":true,"id":"…","template":"nginx-static","name":"kore-app-test",
#    "container_id":"<64 位>","access_url":"http://127.0.0.1:20000/",
#    "ports":[…],"containers":[…],"created_at":"…"}

# 记录 + docker ps 的真实状态（running / exited / missing）
{"method":"app-deploy.list","params":{}}
# → {"deployments":[…],"count":1}

# 删除（confirm 必须为 true；只删记录里的容器 ID）
{"method":"app-deploy.remove","params":{"name":"kore-app-test","confirm":true}}
# → {"removed":true,"name":"kore-app-test","containers":["kore-app-test"],…}
```

容器命名：单容器模板用 `name` 本身，多容器模板的主容器用 `name`、依赖容器用 `<name>-<服务名>`（如 `kore-app-wp` / `kore-app-wp-db`）。依赖容器先启动，主容器最后启动。

### 用户应用包方法（0.3.0）

| 方法 | 入参 | 结果要点 |
| --- | --- | --- |
| `app-deploy.upload` | `{name, format: zip\|tar.gz, content_base64, sha256?}` | 解压到 `<root>/<name>`：`{name,path,files,bytes,sha256,job,outside_install}`；单包 ≤32 MiB，条目 / 解压总量 / 单文件上限 10000 / 256 MiB / 64 MiB，逐条目拒绝绝对路径、`..`、盘符、反斜杠与链接 |
| `app-deploy.deployFromArtifact` | `{name, content_base64?, format?, sha256?, site_domain?, site_port?, databases:[{engine,name}]?}` | 登记 artifact 部署记录并回 `site_hint` 与建库提示；**不启容器、不写 nginx、不连 MySQL**（vhost 属 site、建库属 db-mysql，插件不互相调用） |
| `app-deploy.upgrade` | `{name, format, content_base64, sha256?}` | 新包解到暂存 → 旧目录快照 `<state>/snapshots/<name>/<ts>` → 换入；换入失败自动回滚并在错误信息标注，结果带 `snapshot` 与 `job` |
| `app-deploy.jobs` | `{id?}` | 作业状态 `{jobs:[{id,name,action,status,steps,error?,artifact_dir?,snapshot?,started_at,finished_at?}],count}`；只读、同步推进、不起 ticker（G-19） |

artifact 部署记录使用模板标识 `artifact`（不走内置模板白名单：包内容由用户自己负责，容器仍只由模板 `deploy` 创建）。

## 安全设计

1. **模板白名单**：镜像只出现在源码常量里，`deploy` 只接受模板 id；`image` / `cmd` 等额外字段被忽略，无法 `docker run evil/image`。
2. **名称即边界**：`name` 必须匹配 `^[a-z][a-z0-9-]{0,30}$` **且以 `kore-app-` 开头**，封死空白/斜杠/分号/`$()`/引号/NUL；派生出的容器名、网络名、卷名同样带前缀。
3. **端口**：宿主侧只允许 1024–65535（禁止 80/443/22 等特权端口），且**只绑 `127.0.0.1`**，不暴露到 `0.0.0.0`；冲突检测 = `docker ps` 已发布端口 ∪ 回环实际绑定探测。显式端口被占 → `E_CONFLICT`（不静默改），未指定 → 从模板默认端口向上顺延到第一个可用端口。
4. **环境变量**：只接受模板声明的键（白名单），必填项缺失 → `E_INVALID`；值长度 ≤256 字节、拒绝 shell 元字符与控制字符。真口令走 **0600 `--env-file`**，不进 argv、不返回、不落部署记录，`docker run` 结束立即删除该文件。
5. **删除必须双证**：`confirm:true` + 部署记录中存在该部署；删除只按 deploy 时记录的**完整容器 ID**执行，并复核容器名前缀。docker 里存在但无记录的同名容器 → `E_UNAUTHORIZED`（绝不当"顺手清理"删掉别人的容器）。
6. **内存限额**：每个容器都带 `--memory` 与等值的 `--memory-swap`（禁 swap），防止单个部署用 swap 顶过限额把宿主机拖进 OOM。
7. **卷受限**：命名卷由 docker 管理；bind 卷只落在 `<state>/volumes/<name>/<子目录>` 内，**不接受任意宿主机路径**（`volumes` 参数只能改子目录名）。
8. **失败回滚**：任一容器启动失败或记录写入失败 → 删除已建容器与网络、不写记录，不留半拉部署。

## 状态与运行期配置

- 状态目录 `<state>`：环境变量 `KORE_APP_DEPLOY_STATE`，缺省 `<root>/state`；`KORE_APP_DEPLOY_ROOT` 缺省 `/var/lib/kore/app-deploy`。
- `deployments.json`：目录 0700、文件 0600、同目录临时文件 + `rename` 原子替换；记录损坏或版本高于插件支持版本一律报错（不静默重置，否则已有容器会失去归属记录）。
- `KORE_DOCKER_BIN`：docker CLI 名或绝对路径（缺省 `docker`，走 PATH）。
- 非 Linux 平台返回 `E_UNSUPPORTED`，不做静默降级。

## 四类故障注入

`tests/fault/` 四个文件分别注入 kill -9、超时、磁盘满、cgroup 超限，断言 `deploy` / `list` / `remove` 返回结构化错误，且 `Error.Code` 与故障类别一一对应（`templates` 是本地常量列表，不触达执行面）：

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
tar -tzf dist/app-deploy-0.3.0.tar.gz | head -1   # info.json

# 真 docker E2E（默认跳过；开启后缺 docker 会失败而不是跳过）
make e2e
docker ps -a --filter name=kore-app- --format '{{.Names}}'   # 清理后应为空
```

E2E 覆盖：`templates` 返回 4 个模板 → `deploy nginx-static` 后容器 running 且 `curl` 首页 200 → `list` 显示 running → `deploy wordpress` 双容器 running 且 `curl` 首页 200（WordPress 安装页）→ 端口冲突 `E_CONFLICT` → 非本插件容器 `E_UNAUTHORIZED` → `remove` 缺 confirm `E_INVALID` → `remove` 带 confirm 后容器/命名卷/网络/记录全部清理。

`dist` 使用确定性参数归档（`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner` + `gzip -9n`，chmod 归一），重复构建 sha256 不变；`size_bytes` 由提交前实测包体积回填源清单。

<!-- oss:begin -->
## 开源与贡献

[![CI](https://github.com/kore-pl/kore-plugin-app-deploy/actions/workflows/ci.yml/badge.svg)](https://github.com/kore-pl/kore-plugin-app-deploy/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)

本仓库是 **kore** 的 `应用一键部署插件`（id `app-deploy`），独立仓库、独立版本线、独立 CI（少触发：push main / tag / PR / 手动）。内核与协议见 [kore](https://github.com/kore-pl/kore)。

- **许可**：MIT（inbound = outbound，不要求 CLA）；提交用 `git commit -s` 签 DCO。
- **贡献**：[CONTRIBUTING.md](CONTRIBUTING.md)（构建、门禁、发版、边界）。
- **安全**：[SECURITY.md](SECURITY.md)——漏洞不要开公开 issue。
- **行为准则**：[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
- **兼容性**：`protocol: kore.plugin.v1` · `requires_kore: >=0.1.0 <0.2.0` · `lifecycle: lazy` · 深度等级 `production`。
- **验证方式**：本地 `make check` 优先（CI 只在 push main / tag / PR / 手动跑；发布前 `make dist` 连跑两次核对 sha 一致）。
- **发布产物**：`make dist` → `dist/app-deploy-0.3.0.tar.gz` + `.sha256`（位级可复现，连跑两次 sha 相同）。
- **相关仓库**：[索引 kore-plugins](https://github.com/kore-pl/kore-plugins) · [协议文档](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-PROTOCOL.md) · [SDK 指南](https://github.com/kore-pl/kore/blob/main/docs/PLUGIN-SDK.md)

<!-- oss:end -->
