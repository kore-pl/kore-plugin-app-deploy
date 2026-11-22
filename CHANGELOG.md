# Changelog

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
