# 应用一键部署插件（kore-plugin-app-deploy）

kore-plugin-app-deploy 是 kore 的 `app-deploy` 插件，独立仓库、独立版本线、独立 CI。当前版本 `0.4.0`，协议 `kore.plugin.v1`。

## 这是什么

本插件提供真实的一键应用部署：内置 9 个模板（含 Node.js / Python / PHP / MySQL+phpMyAdmin / Uptime Kuma），通过宿主机的 docker CLI（固定 argv、不经 shell）拉起容器。部署记录落 `<state>/deployments.json`（0600），`list` 关联 `docker ps` 查询真实运行状态并附带资源占用概览；部署后可直接停止、启动、查看日志；`remove` 必须显式 `confirm=true`，且只删除本插件创建的容器。

## 快速入口

- [安装](Installation)：插件包获取与安装步骤
- [配置](Configuration)：配置项、能力声明与环境变量
- [使用方法](Usage)：协议方法与典型调用
- [常见问题](FAQ)：边界、权限与降级行为
- [变更记录](Changelog)：语义化版本历史

## 相关链接

- 源码：https://github.com/kore-pl/kore-plugin-app-deploy
- 内核与协议：https://github.com/kore-pl/kore
- 索引：https://github.com/kore-pl/kore-plugins
