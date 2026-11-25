# Changelog

## 0.4.0 (2026-11-25)

- feat(templates): 内置模板 4 → **9 个**，新增 `node-app`（node:20-alpine）、`python-app`（python:3.12-alpine）、`php-site`（php:8.3-apache）、`mysql-phpmyadmin`（mysql:8 + phpmyadmin:5 双容器）、`uptime-kuma`。每条模板带对外端口/环境变量/数据卷默认值与中文说明；既有 4 个模板**保持原位**（老用户看到的顺序与默认端口不变），新模板追加在后
- feat(templates): `ports[]` 新增 `suggested_host_port`——读取时按 `docker ps` 已发布端口 ∪ 本机回环绑定探测现算出「此刻可用」的端口（从默认端口向上找第一个可用值），供前端标「已自动检测」；docker 不可用时降级为只按本机探测，模板目录照常返回。建议值写在深拷贝上，不污染全局模板常量
- feat(deploy): bind 挂载写入**示例入口**避免「部署成功但打开是 403/白页/容器立刻退出」：`node-app` 落 `server.js`（内置 http 服务，零依赖）、`python-app` 落 `app.py`（标准库 http.server）、`php-site` 落 `index.php`；已有同名文件**绝不覆盖**（nginx 的 `index.html` 旧行为不变，改为表驱动）
- feat(control): 新增 `app-deploy.stop` / `app-deploy.start` / `app-deploy.logs`——部署后管理闭环（停止/启动/看日志）。只按部署记录里的容器名操作，不改写记录；日志 stdout+stderr 合并、`tail` 缺省 200 / 上限 2000、单次 ≤64 KiB 且标记 `truncated`
- feat(list): 每条部署带资源占用概览 `usage{cpu_percent,mem_used_bytes,mem_limit_bytes}`（一次 `docker stats --no-stream` 聚合）；采样失败列表照常返回并回 `usage_error`，不静默
- feat(capability): `method_capabilities` 8 → 11 条——`stop`/`start` → `system:admin`，`logs` → `system:read`；控制面为执行面**可选接口**，不支持时回 `E_UNSUPPORTED` 并写明缺什么（不静默降级）
- fix(templates): `mysql-phpmyadmin` 的 `MYSQL_ROOT_PASSWORD` 设为**必填**（phpMyAdmin 登录要用；插件不保存口令，随机生成会让用户永远登不进去）；内部连接变量 `PMA_HOST`/`PMA_PORT`/`PMA_DB_NAME` 与随机会话密钥 `PMA_BLOWFISH_SECRET` 由插件派生，不进 argv
- test: 模板目录 9 条逐项断言（镜像/服务/端口/卷/env 完整性与顺序稳定）、端口建议两条来源（容器占用 + 探测失败）与副本隔离、docker 不可用时降级、新增模板示例入口与「不覆盖用户文件」、`mysql-phpmyadmin` 必填口令与内部变量、`stop`/`start`/`logs` 闭环与错误分类、`usage` 聚合与采样失败可见、方法级能力声明 11 条一致性
- build: 版本 0.3.3 → 0.4.0（新增方法 + 模板集合变化，dist sha 变化必须 bump），`info.json` / `manifest.json` / README 发布产物版本同步

## 0.3.3 (2026-11-24)

- fix(upgrade): 修复升级快照目录**同秒并发互相覆盖**的 P2 竞态。根因：快照目录名只有秒级时间戳 `<state>/snapshots/<name>/<UTC秒>`，同一应用同一秒两次 upgrade 撞同一路径，`os.MkdirAll` 对已存在目录返回 nil（不报错），两次 `copyTree` 互相覆盖、后写覆盖先写。首次调用 `snapshotStampFn()` 生成 `<UTC秒>` 前缀后撞名追加 `-2/-3…` 后缀（上限 64 次，超出结构化报错），用 `os.Mkdir` 原子占名——已存在则原子返回 EEXIST 而非静默成功。状态锁边界不变：仍只护 deployments/jobs JSON，快照目录占名的原子性由文件系统保证，重 IO 的 copyTree 留在锁外，不把毫秒级 JSON 临界区拖成秒级、避免撞 5s 锁超时
- test(boundary): 新增 `TestUpgradeSameSecondSnapshotsIsolated` 缺陷回归——注入固定 `snapshotStampFn` 把「同秒」从概率事件变确定性，两次串行 upgrade 断言快照路径两两不同、第一次快照 v1 不被第二次 v2 覆盖、第二次快照保留 v2、快照根恰好 2 个独立子目录。修复前稳定 FAIL（撞同一路径），修复后 PASS；全仓 `go test -race ./...` 五包全绿、`make check` exit 0
- build: 版本 0.3.2 → 0.3.3（运行时行为变更，dist sha 变化必须 bump），`info.json` / `manifest.json` / README 发布产物版本同步

## 0.3.2 (2026-11-24)

- fix(state): `deployments.json` / `artifact-jobs.json` 的「读-改-写」加跨进程状态锁，修复并发丢更新。缺陷实测：8 个不同应用并发 `deploy` 全部成功但 `deployments.json` 只剩 **1 条**；8 个并发 `upload` 后 `artifact-jobs.json` 只剩 **3 条**（last-writer-wins，旧快照覆盖别人的新增）。`deploy` / `remove` / `deployFromArtifact` / `upsertJob` 的 load→改→save 全部进临界区；`deploy` 的锁只覆盖落记录窗口，不包住 `docker run`（并发部署不被串行化），同名并发请求在锁内重读后回 `E_CONFLICT` 并回滚容器
- fix(lock): 锁固定在**旁路**文件 `<state>/.state.lock` 上，不加在会被 rename 替换的状态文件上（rename 后 inode 更替，锁会静默失效——site 清单锁的教训）。一把锁同时管两个状态文件：二者会被同一次调用先后写入，分锁会引入加锁顺序问题；unix 走 `flock`（EINTR/EWOULDBLOCK 重试、5s 上限、进程消亡由内核释放），非 unix 退化为进程内 `sync.Mutex` 并创建同名锁文件
- test(boundary): 补缺陷回归断言——`TestConcurrentDeployDistinctApps` 断言 8 并发后 `deployments.json` **恰好 8 条**、逐条与对应应用的部署 id / 容器 id / 宿主端口 / 创建时间严格对应（无串扰）、`artifact-jobs.json` 完整且未被 deploy 凭空写入；`TestConcurrentUploadsKeepPayloadsIsolated` 断言 8 并发 upload 后作业记录**恰好 8 条**且应用名无重复。修复前两条断言稳定 FAIL（1/8、3/8），修复后 `-race` 全绿
- build: 版本 0.3.1 → 0.3.2，`info.json` / `manifest.json` / README 发布产物版本同步

## 0.3.1 (2026-11-23)

- test(fault): 补齐 G-20 故障注入——`upload` / `deployFromArtifact` / `upgrade` / `jobs` 四个新方法此前零 fault 覆盖。新增 `tests/fault/artifact_test.go`：数据根只读（upload 回 `E_INTERNAL`、不留应用目录、作业只记 failed）、状态路径被占用（回 `E_INTERNAL`、宿主状态文件原样）、部署记录损坏（回 `E_INTERNAL` 且拒绝自动重置、不得记为 succeeded）、升级暂存失败（旧版本目录与内容原样、无 `.staging-*` 残留）、作业记录损坏（回 `E_INTERNAL`、文件不被覆盖）；另加「无记录 = count 0」反面对照避免假通过
- build: 版本 0.3.0 → 0.3.1（仅测试与版本，无运行时行为变更），`info.json` / `manifest.json` 同步

## 0.3.0 (2026-11-23)

- feat: **用户自己的应用包**——`app-deploy.upload {name, format=zip|tar.gz, content_base64, sha256?}` 解压到 `$KORE_PLUGIN_DATA/apps/<name>`（复用 ResolveDataDirs）；逐条目护栏：绝对路径 / `..` 穿越 / 盘符 / 反斜杠 / 符号链接与硬链接 / 条目数与解压总量上限，解压先落暂存目录再整体 rename
- feat: `app-deploy.deployFromArtifact` —— 登记 artifact 部署记录（bind 指向包目录）并返回 `site_hint`（域名/端口/根目录）与 `databases` 声明提示；**不写 nginx、不连 MySQL**（vhost 属 site、建库属 db-mysql 写域，G-13 禁止插件互调）
- feat: `app-deploy.upgrade {name, format, content_base64}` —— 新包解到同根暂存 → 旧目录快照到 `$KORE_PLUGIN_DATA/snapshots/<name>/<ts>` → 目录换入；换入失败**回滚到旧目录**
- feat: `app-deploy.jobs {id?}` —— 分阶段作业状态（pending/running/succeeded/failed + steps），同步推进并落盘 `artifact-jobs.json`，供 UI 轮询；**不起 ticker**（G-19）
- feat(capability): `method_capabilities` 8 条——`templates`/`list`/`jobs` → `system:read`，`deploy`/`upload`/`deployFromArtifact`/`upgrade`/`remove` → `system:admin`
- test: 路径穿越（zip/tar/绝对路径/盘符）逐条拒绝、坏包与缺包、上传→部署登记+边界 hint、升级快照与注入失败回滚、作业状态机成功/失败

## 0.2.2 (2026-11-23)

- feat(zero-intrusion): 消费内核注入的 `KORE_PLUGIN_DATA`（ADR-0023 阶段 C）——部署根/状态默认 `$KORE_PLUGIN_DATA/{apps,snapshots}`；显式 `KORE_APP_DEPLOY_ROOT`/`KORE_APP_DEPLOY_STATE` 优先；两者都没有时回退旧默认 `/var/lib/kore/app-deploy` 并**打 WARN**（不静默）
- feat(list): 每条部署返回 `outside_install` 与安装目录外 `hint`；**已部署应用不迁移**（仅标记，不自动搬迁）
- test: 解析优先级/旧默认 WARN/既有记录保持/outside_install 判定/随包模板只读不落盘

## 0.2.1 (2026-11-23)

- feat(protocol): `info.json` 补 `method_capabilities` 4 条——`templates`/`list` 声明 `system:read`，`deploy`/`remove` 声明 `system:admin`（§3.5 #12）
- test: 声明一致性表驱动门禁（分类断言 + 声明 ⊆ 实现反向断言 + 条数相等）

## 0.2.0 (2026-11-23)

- feat: 内置模板**真部署**——`app-deploy.deploy` 通过本机 docker CLI（固定 argv、不经 shell）拉起容器，内置 `nginx-static` / `wordpress` / `redis` / `postgres` 四个模板
- feat: `app-deploy.templates` 返回内置模板目录（`id`/`name`/`description`/`image`/`ports`/`volumes`/`env`/`estimated_memory_mb`），本地常量、不联网、不下载
- feat: `app-deploy.list` 读部署记录并关联 `docker ps -a`，输出 running / exited / missing 真实状态（只读，不改记录）
- feat: `app-deploy.remove` 强制 `confirm`，只删部署记录里的容器 ID 并清理命名卷与网络；docker 里存在但无记录的同名容器返回 `E_UNAUTHORIZED`
- feat: 部署记录原子写入 `<state>/deployments.json`（文件 0600 / 目录 0700），任一容器启动或记录写入失败即回滚已建容器与网络
- security: 模板白名单（镜像只在源码常量，不接受用户自定义镜像，额外 `image`/`cmd` 字段被忽略）
- security: `name` 强制 `^[a-z][a-z0-9-]{0,30}$` 且以 `kore-app-` 开头；派生容器名/网络名/卷名同样带前缀
- security: 宿主端口仅 1024–65535 且只绑 `127.0.0.1`；冲突检测 = `docker ps` ∪ 回环绑定探测，显式冲突 `E_CONFLICT`、未指定自动顺延
- security: env 只接受模板声明的键、必填缺失拒绝、值 ≤256 字节且拒绝 shell 元字符；真口令走 0600 `--env-file`，不进 argv、不返回、不落记录、run 完即删
- security: 每容器 `--memory` + 等值 `--memory-swap`（禁 swap）限额；bind 卷只落状态目录内，不接受任意宿主机路径
- refactor: 破坏性变更——`app-deploy.create`（骨架 `applied=false`）移除，由 `app-deploy.deploy` 取代；模板集合由 `wordpress/laravel/nextjs` 骨架改为 4 个可部署模板
- test: 单元覆盖模板完整性、name 注入拒绝、端口冲突与顺延、env 必填/元字符/白名单、非本插件容器拒绝删除、confirm 缺失拒绝、失败回滚、记录 0600/0700 与损坏拒绝
- test: 新增真实 docker E2E（nginx 单容器 curl 200 / wordpress 双容器首页 curl 200 / 端口冲突 / 外部容器护栏 / 清理无残留），`make e2e` 显式开启
- test: 四类故障注入改为覆盖 deploy / list / remove 三个方法（templates 不触达执行面）

## 0.1.1 (2026-11-23)

- refactor: 方法名统一为 app-deploy.<action>，对齐协议域校验
- build: 位级可复现打包（stage chmod + tar --mode 归一、size_bytes 收敛），二进制改嵌 manifest.json，消除同版本 sha 漂移

## 0.1.0 (2026-11-22)

- feat: 新建 app-deploy 插件骨架，复用 kore.plugin.v1 握手与会话循环
- feat: app-deploy.templates / app-deploy.create 方法级参数校验与结构化结果，模板白名单 wordpress/laravel/nextjs，目标路径限制在配置根目录内，applied=false 占位，不真拉取/部署应用
- refactor: 方法名按协议统一为 `<plugin-id>.<action>`（原 `deploy.*` → `app-deploy.*`），移除裸动词与旧域前缀兼容
- feat: 四类故障注入测试（kill -9 / 超时 / 磁盘满 / cgroup 超限），验证方法级错误返回
- build: 确定性打包（--sort=name --mtime=@0 --owner=0 --numeric-owner + gzip -9n + chmod 归一）
