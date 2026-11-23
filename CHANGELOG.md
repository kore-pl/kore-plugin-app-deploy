# Changelog

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
