package apop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// DockerClient 的控制面实现：停止 / 启动 / 读日志 / 采样资源占用。
//
// 与部署面一样走固定 argv（不经 shell）、名字与 ID 都来自已校验的部署记录，
// 没有任何一段来自外部拼接的命令字符串。
var _ Controller = (*DockerClient)(nil)

// StopContainers 执行 docker stop（先 SIGTERM，docker 自己在 10s 后 SIGKILL）。
func (c *DockerClient) StopContainers(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := c.run(append([]string{"stop"}, ids...)...)
	return err
}

// StartContainers 执行 docker start（对已在运行的容器是幂等成功）。
func (c *DockerClient) StartContainers(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := c.run(append([]string{"start"}, ids...)...)
	return err
}

// ContainerLogs 读取容器最近 tail 行日志；stdout 与 stderr 合并（应用把错误打到
// stderr 是最常见的情形，只读 stdout 会让用户看不到失败原因）。
func (c *DockerClient) ContainerLogs(name string, tail int) (string, error) {
	if tail <= 0 {
		tail = DefaultLogTail
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	cmd, err := c.command(ctx, "logs", "--tail", strconv.Itoa(tail), name)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", opErrorf(pluginapi.E_UPSTREAM, "docker logs 超时（%s），已终止", c.timeout)
	}
	if runErr != nil {
		return "", c.execError(runErr, buf.String())
	}
	return buf.String(), nil
}

// SampleUsage 用 docker stats 一次性采样（--no-stream，不留后台进程、不轮询）。
func (c *DockerClient) SampleUsage(names []string) ([]ContainerUsage, error) {
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"stats", "--no-stream", "--format", statsFormat}, names...)
	out, err := c.run(args...)
	if err != nil {
		return nil, err
	}
	usages, perr := parseStatsOutput(out)
	if perr != nil {
		return nil, opErrorf(pluginapi.E_UPSTREAM, "%s", perr.Error())
	}
	return usages, nil
}

// statsFormat 是 docker stats 的单行模板：名字 | CPU% | 内存用量/限额。
const statsFormat = "{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}"

// parseStatsOutput 解析 docker stats 的逐行输出。单行缺字段或数字不可解析时跳过该行
// —— 资源占用是展示性数据，一行异常不该让整个列表查询失败。
func parseStatsOutput(data []byte) ([]ContainerUsage, error) {
	usages := make([]ContainerUsage, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			return nil, fmt.Errorf("docker stats 输出行不含 3 个字段: %q", truncateRunes(line, 120))
		}
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		usage := ContainerUsage{Name: name}
		if cpu, err := parsePercent(parts[1]); err == nil {
			usage.CPUPercent = cpu
		}
		used, limit, err := parseMemUsage(parts[2])
		if err != nil {
			return nil, fmt.Errorf("docker stats 内存字段不可解析（%q）: %w", truncateRunes(parts[2], 80), err)
		}
		usage.MemUsedBytes = used
		usage.MemLimitBytes = limit
		usages = append(usages, usage)
	}
	return usages, nil
}

// parsePercent 解析 "0.15%" 这类百分比串（去掉尾随 % 与空白）。
func parsePercent(raw string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(raw), "%"), 64)
}

// parseMemUsage 解析 "12.34MiB / 128MiB" 为用量与限额（字节）。
func parseMemUsage(raw string) (int64, int64, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("缺少 “用量 / 限额” 分隔符")
	}
	used, err := parseBytes(parts[0])
	if err != nil {
		return 0, 0, err
	}
	limit, err := parseBytes(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return used, limit, nil
}

// memUnits 是 docker stats 人读单位的字节换算（docker 一律按 1024 进位）。
var memUnits = map[string]float64{
	"b": 1, "kb": 1 << 10, "kib": 1 << 10,
	"mb": 1 << 20, "mib": 1 << 20,
	"gb": 1 << 30, "gib": 1 << 30,
	"tb": 1 << 40, "tib": 1 << 40,
}

// parseBytes 解析 "512KiB" / "1.5GiB" / "0B" 这类紧凑字节串。
func parseBytes(raw string) (int64, error) {
	text := strings.TrimSpace(raw)
	idx := len(text)
	for idx > 0 {
		ch := text[idx-1]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			idx--
			continue
		}
		break
	}
	number := strings.TrimSpace(text[:idx])
	unit := strings.ToLower(strings.TrimSpace(text[idx:]))
	if unit == "" {
		unit = "b"
	}
	factor, ok := memUnits[unit]
	if !ok {
		return 0, fmt.Errorf("未知内存单位 %q", unit)
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, fmt.Errorf("数值 %q 不可解析", number)
	}
	if value < 0 {
		return 0, fmt.Errorf("数值 %q 为负", number)
	}
	return int64(value * factor), nil
}
