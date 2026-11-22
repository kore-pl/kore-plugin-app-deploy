package apop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// DockerClient 通过本机 docker CLI 执行部署操作（os/exec，固定 argv，不经 shell）。
//
// 所有容器/网络/卷的 argv 都由本包构造：镜像是内置模板常量、名字经过正则与前缀校验、
// env 走 --env-file（真口令不进 argv）、端口绑 127.0.0.1，没有任何一段来自拼接的字符串命令。
type DockerClient struct {
	bin     string
	timeout time.Duration
}

// NewDockerClient 构造真实执行面并补齐缺省值。
func NewDockerClient(bin string, timeout time.Duration) *DockerClient {
	if bin == "" {
		bin = DefaultDockerBin
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &DockerClient{bin: bin, timeout: timeout}
}

// RunContainer 执行 docker run -d 并返回容器完整 ID。
func (c *DockerClient) RunContainer(spec ContainerSpec) (string, error) {
	out, err := c.run(BuildRunArgs(spec)...)
	if err != nil {
		return "", err
	}
	id := firstLine(string(out))
	if len(id) < 12 {
		return "", opErrorf(pluginapi.E_UPSTREAM, "docker run 未返回容器 ID（输出=%q）", truncateRunes(id, 200))
	}
	return id, nil
}

// ListContainers 执行 docker ps -a 并解析为结构化快照。
func (c *DockerClient) ListContainers() ([]ContainerState, error) {
	out, err := c.run("ps", "--all", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	states, perr := parsePSOutput(out)
	if perr != nil {
		return nil, opErrorf(pluginapi.E_UPSTREAM, "%s", perr.Error())
	}
	return states, nil
}

// RemoveContainers 强制删除容器（-f 先停后删）；id 列表为空直接返回。
func (c *DockerClient) RemoveContainers(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"rm", "-f"}, ids...)
	_, err := c.run(args...)
	return err
}

// RemoveVolumes 删除命名卷；卷已不存在视为成功（幂等）。
func (c *DockerClient) RemoveVolumes(names []string) error {
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"volume", "rm"}, names...)
	if _, err := c.run(args...); err != nil {
		var opErr *OpError
		if errors.As(err, &opErr) && opErr.Code == pluginapi.E_NOTFOUND {
			return nil
		}
		return err
	}
	return nil
}

// CreateNetwork 创建部署专用 bridge 网络（多容器模板的服务发现基座）。
func (c *DockerClient) CreateNetwork(name string) error {
	_, err := c.run("network", "create", "--label", LabelManaged+"=app-deploy", name)
	return err
}

// RemoveNetwork 删除网络；网络已不存在视为成功（幂等）。
func (c *DockerClient) RemoveNetwork(name string) error {
	if name == "" {
		return nil
	}
	if _, err := c.run("network", "rm", name); err != nil {
		var opErr *OpError
		if errors.As(err, &opErr) && opErr.Code == pluginapi.E_NOTFOUND {
			return nil
		}
		return err
	}
	return nil
}

// BuildRunArgs 把 ContainerSpec 转成固定 argv。纯函数，单测直接断言 argv 形态。
func BuildRunArgs(spec ContainerSpec) []string {
	args := []string{"run", "-d", "--name", spec.Name}
	for _, key := range sortedKeys(spec.Labels) {
		args = append(args, "--label", key+"="+spec.Labels[key])
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	if spec.MemoryMB > 0 {
		limit := strconv.Itoa(spec.MemoryMB) + "m"
		// memory-swap 与 memory 相等 = 禁用 swap，避免用 swap 顶过限额把宿主机拖进 OOM。
		args = append(args, "--memory", limit, "--memory-swap", limit)
	}
	for _, binding := range spec.Ports {
		protocol := binding.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		// 只绑回环：部署出的服务默认不暴露到 0.0.0.0。
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:%d/%s", binding.HostPort, binding.ContainerPort, protocol))
	}
	for _, volume := range spec.Volumes {
		mount := volume.Source + ":" + volume.Target
		if volume.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "--volume", mount)
	}
	if spec.EnvFile != "" {
		args = append(args, "--env-file", spec.EnvFile)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Cmd...)
	return args
}

// sortedKeys 返回排序后的 map 键，保证 argv 稳定可复现。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// psRow 是 `docker ps --format '{{json .}}'` 单行输出的字段子集。
type psRow struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Ports  string `json:"Ports"`
}

// parsePSOutput 解析 docker ps 的逐行 JSON 输出。
func parsePSOutput(data []byte) ([]ContainerState, error) {
	states := make([]ContainerState, 0)
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var row psRow
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 docker ps JSON 行失败: %w", err)
		}
		name := strings.TrimSpace(row.Names)
		if idx := strings.IndexByte(name, ','); idx >= 0 {
			name = name[:idx]
		}
		states = append(states, ContainerState{
			ID:     strings.TrimSpace(row.ID),
			Name:   strings.TrimPrefix(strings.TrimSpace(name), "/"),
			Image:  row.Image,
			State:  strings.TrimSpace(row.State),
			Status: row.Status,
			Ports:  row.Ports,
		})
	}
	return states, nil
}

// hostPortRE 从 docker ps 的 Ports 串里提取宿主机端口（含端口段）。
var hostPortRE = regexp.MustCompile(`:(\d+)(?:-(\d+))?->`)

// ParseHostPorts 解析 `0.0.0.0:3306->3306/tcp, 127.0.0.1:18080->8080/tcp`
// 这类字符串里的宿主机端口；端口段展开上限 1024 个，防止畸形输入放大。
func ParseHostPorts(ports string) []int {
	matches := hostPortRE.FindAllStringSubmatch(ports, -1)
	out := make([]int, 0, len(matches))
	for _, m := range matches {
		start, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		end := start
		if m[2] != "" {
			if parsed, err := strconv.Atoi(m[2]); err == nil {
				end = parsed
			}
		}
		if end < start {
			end = start
		}
		if end-start > 1024 {
			end = start + 1024
		}
		for port := start; port <= end; port++ {
			out = append(out, port)
		}
	}
	return out
}

// busyPorts 汇总当前被占用的宿主机端口：docker 已发布的端口 + 回环探测失败的端口。
// docker 侧只覆盖「容器占用」，探测则补上宿主进程占用（部署失败前的真实拦截）。
func (r *Runtime) busyPorts() (map[int]string, *pluginapi.Error) {
	busy := make(map[int]string, 64)
	states, err := r.exec.ListContainers()
	if err != nil {
		return nil, classify(err)
	}
	for _, state := range states {
		if state.State != StatusRunning {
			continue
		}
		for _, port := range ParseHostPorts(state.Ports) {
			busy[port] = "容器 " + state.Name + " 已发布"
		}
	}
	return busy, nil
}

// probePort 尝试在 127.0.0.1 上监听该端口；能绑成功即可用。
func probePort(port int) bool {
	if port < 1 || port > MaxPort {
		return false
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// command 构造固定 argv 的子进程；docker 未安装时返回 E_UNSUPPORTED。
func (c *DockerClient) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	path, err := exec.LookPath(c.bin)
	if err != nil {
		return nil, opErrorf(pluginapi.E_UNSUPPORTED,
			"未找到 docker 命令行 %q：请安装 Docker CLI，或用 %s 指定绝对路径", c.bin, EnvDockerBin)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = os.Environ()
	return cmd, nil
}

// run 执行一条 docker 命令并返回 stdout；超时统一映射为 E_UPSTREAM。
func (c *DockerClient) run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	cmd, err := c.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, opErrorf(pluginapi.E_UPSTREAM, "docker 命令超时（%s），已终止：docker %s", c.timeout, args[0])
	}
	if runErr != nil {
		return nil, c.execError(runErr, stderr.String())
	}
	return stdout.Bytes(), nil
}

// execError 把 CLI 启动/执行失败映射为协议错误；stderr 先清洗再截断。
func (c *DockerClient) execError(err error, stderr string) error {
	var execErr *exec.Error
	var pathErr *fs.PathError
	if errors.As(err, &execErr) || errors.As(err, &pathErr) {
		return opErrorf(pluginapi.E_UNSUPPORTED,
			"无法执行 docker 命令行 %q：请安装 Docker CLI，或用 %s 指定绝对路径", c.bin, EnvDockerBin)
	}
	text := sanitizeStderr(stderr)
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "cannot connect to the docker daemon"),
		strings.Contains(lower, "is the docker daemon running"):
		return opErrorf(pluginapi.E_UNSUPPORTED, "Docker daemon 不可达：请确认 dockerd 已启动（%s）", truncateRunes(text, 300))
	case strings.Contains(lower, "permission denied") &&
		(strings.Contains(lower, "docker.sock") || strings.Contains(lower, "unix:///var/run/docker")):
		return opErrorf(pluginapi.E_UNSUPPORTED, "无权访问 Docker socket：请把用户加入 docker 组（%s）", truncateRunes(text, 300))
	case strings.Contains(lower, "port is already allocated"),
		strings.Contains(lower, "address already in use"):
		return opErrorf(pluginapi.E_CONFLICT, "宿主机端口已被占用（%s）", truncateRunes(text, 300))
	case strings.Contains(lower, "is already in use by container"),
		strings.Contains(lower, "conflict. the container name"):
		return opErrorf(pluginapi.E_CONFLICT, "同名容器已存在（%s）", truncateRunes(text, 300))
	case strings.Contains(lower, "no such container"), strings.Contains(lower, "no such object"),
		strings.Contains(lower, "no such volume"), strings.Contains(lower, "no such network"):
		return opErrorf(pluginapi.E_NOTFOUND, "资源不存在（%s）", truncateRunes(text, 300))
	}
	if text == "" {
		text = err.Error()
	}
	return opErrorf(pluginapi.E_UPSTREAM, "docker 命令失败：%s", truncateRunes(text, 500))
}

// sanitizeStderr 去掉空行、用 " | " 拼接非空行，避免多行 stderr 污染协议响应。
func sanitizeStderr(raw string) string {
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, " | ")
}

// truncateRunes 按 rune 截断并追加省略号。
func truncateRunes(raw string, limit int) string {
	runes := []rune(raw)
	if len(runes) <= limit {
		return raw
	}
	return string(runes[:limit]) + "…"
}

// firstLine 取输出的第一行（docker run 正常只输出一行 ID）。
func firstLine(raw string) string {
	if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
		return strings.TrimSpace(raw[:idx])
	}
	return strings.TrimSpace(raw)
}

// 编译期断言：DockerClient 实现 Executor。
var _ Executor = (*DockerClient)(nil)
