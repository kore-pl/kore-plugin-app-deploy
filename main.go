// Package main 是 kore 的 app-deploy 插件：一键部署应用的方法面骨架。
//
// app-deploy 只做协议握手 + 参数校验 + 结构化结果，不真拉取/部署应用、
// 不内置任何应用模板二进制或模板下载器（裁决37 / 提案 §2.1 批3）。
//
// 协议面（kore.plugin.v1）：UDS → kore.hello 握手 → 会话循环读 request、
// 按 method 分发并回 response；stdout 留给协议，日志一律写 stderr。
package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
)

// main 按退出码区分失败：0 优雅退出、1 启动或协议失败。
func main() {
	logger := newLogger(os.Getenv("KORE_LOG_LEVEL"))
	if err := run(logger); err != nil {
		logger.Error("插件退出", "err", err)
		os.Exit(1)
	}
}

// run 依次完成建连、hello 握手与会话循环；收到 kore.shutdown 后返回 nil。
func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	conn, err := net.Dial("unix", cfg.socket)
	if err != nil {
		return fmt.Errorf("连接主壳 socket %s: %w", cfg.socket, err)
	}
	defer func() { _ = conn.Close() }()
	if err := handshake(conn, cfg); err != nil {
		return err
	}
	logger.Info("hello 完成", "plugin_id", cfg.id, "version", cfg.version)
	if cfg.dataWarn != "" {
		logger.Warn("数据目录回退旧默认", "detail", cfg.dataWarn)
	}
	runtime := apop.NewRuntime(apop.Options{
		Root:      cfg.root,
		StateDir:  cfg.stateDir,
		DockerBin: cfg.dockerBin,
	}, nil)
	return serve(conn, cfg, runtime, logger)
}
