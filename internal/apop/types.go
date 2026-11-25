// Package apop 实现 app-deploy 的方法面：内置模板目录、名称/端口/环境变量校验、
// 通过本机 docker CLI（固定 argv、不经 shell）执行真实部署，并把部署记录落到
// 插件状态目录（0600）以便 list / remove 追踪。
//
// 依赖边界（G-17）：只依赖 kore/pkg/pluginapi 与 Go 标准库，不引入 Docker SDK，
// 也不内置 docker 二进制；CLI 由宿主机 PATH 提供，可用 KORE_DOCKER_BIN 指到绝对路径。
package apop

import (
	"fmt"
	"time"
)

// 运行期边界常量。
const (
	// PluginID 是本插件 id，同时是方法域前缀（PLUGIN-PROTOCOL §7.2）。
	PluginID = "app-deploy"
	// ContainerPrefix 是插件创建的全部容器/网络/卷的强制前缀：既是命名空间隔离，
	// 也是 remove / 清理时的识别依据（非此前缀一律不动）。
	ContainerPrefix = "kore-app-"
	// DefaultRoot / EnvRoot 是数据根目录（缺省 /var/lib/kore/app-deploy）。
	DefaultRoot = "/var/lib/kore/app-deploy"
	EnvRoot     = "KORE_APP_DEPLOY_ROOT"
	// EnvStateDir 指定状态目录；空时取 <root>/state。
	EnvStateDir = "KORE_APP_DEPLOY_STATE"
	// DefaultDockerBin / EnvDockerBin 是 docker CLI 名或绝对路径。
	DefaultDockerBin = "docker"
	EnvDockerBin     = "KORE_DOCKER_BIN"
	// DefaultTimeout 是单条 docker 命令超时：首次 docker run 可能需要拉镜像。
	DefaultTimeout = 300 * time.Second
	// DeploymentsFile / VolumesDir / StateVersion 是状态目录布局。
	DeploymentsFile = "deployments.json"
	VolumesDir      = "volumes"
	StateVersion    = 1
	// 宿主机端口范围：禁止特权端口（<1024），避免与 80/443/22 抢绑。
	MinPort = 1024
	MaxPort = 65535
	// MaxEnvValueLen / MaxEnvEntries 限制环境变量的长度与数量。
	MaxEnvValueLen = 256
	MaxEnvEntries  = 32
	// 管理标签：容器归属证据（remove 前的二次核对依据之一）。
	LabelManaged    = "kore.app-deploy.managed"
	LabelDeployment = "kore.app-deploy.deployment"
	LabelTemplate   = "kore.app-deploy.template"
	// 卷类型：bind 为宿主机目录（用户可放静态文件），volume 为 docker 命名卷（数据）。
	VolumeBind  = "bind"
	VolumeNamed = "volume"
	// 部署记录与容器状态取值。
	StatusRunning = "running"
	StatusExited  = "exited"
	StatusMissing = "missing"
)

// FaultKind 标识一次故障注入的类别（与 container / db 插件同一口径）。
type FaultKind string

// 四类故障注入类别。
const (
	FaultNone    FaultKind = ""
	FaultKill    FaultKind = "kill"
	FaultTimeout FaultKind = "timeout"
	FaultDisk    FaultKind = "disk"
	FaultCgroup  FaultKind = "cgroup"
)

// FaultError 是执行面注入的故障错误，classify 将其映射为协议错误码。
type FaultError struct {
	Kind    FaultKind
	Message string
}

// Error 实现 error 接口，输出 "<类别>: <说明>"。
func (e *FaultError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

// NewFaultError 构造给定类别的故障错误，供故障注入执行面使用。
func NewFaultError(kind FaultKind, message string) error {
	return &FaultError{Kind: kind, Message: message}
}

// OpError 是执行面带协议错误码的错误，classify 优先用它，避免二次加工时丢码。
type OpError struct {
	Code    string
	Message string
}

// Error 实现 error 接口。
func (e *OpError) Error() string { return e.Code + ": " + e.Message }

// opErrorf 构造带协议错误码的执行面错误。
func opErrorf(code, format string, args ...any) *OpError {
	return &OpError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// EnvField 是模板声明的一项环境变量（含 UI 需要的标签与帮助）。
type EnvField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Default  string `json:"default"`
	Help     string `json:"help"`
	// Service 指明该项注入哪个容器（多容器模板必填）。
	Service string `json:"service"`
	// Secret 为 true 时值不写入部署记录、不出现在 argv（走 --env-file），留空则自动生成。
	Secret bool `json:"secret"`
}

// PortSpec 是模板声明的一条对外端口。
type PortSpec struct {
	Service       string `json:"service"`
	ContainerPort int    `json:"container_port"`
	DefaultHost   int    `json:"default_host_port"`
	Protocol      string `json:"protocol"`
	Label         string `json:"label"`
	// AccessPrefix 是 access_url 的 scheme 前缀，如 "http://" / "redis://"。
	AccessPrefix string `json:"access_prefix"`
	// SuggestedHostPort 是 app-deploy.templates 现算的「此刻可用」宿主端口：
	// 从 DefaultHost 起跳过已被运行中容器占用与绑定失败的端口。0 表示未探测
	// （探测失败或模板常量本身），此时调用方按 DefaultHost 处理。
	SuggestedHostPort int `json:"suggested_host_port,omitempty"`
}

// VolumeSpec 是模板声明的一条挂载。
type VolumeSpec struct {
	Service       string `json:"service"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	ContainerPath string `json:"container_path"`
	Description   string `json:"description"`
}

// Service 是模板内的一个容器（wordpress 模板有 web + db 两个）。
type Service struct {
	Name string `json:"name"`
	// Image 只能来自本文件的常量目录：deploy 不接受用户自定义镜像。
	Image    string   `json:"image"`
	MemoryMB int      `json:"memory_mb"`
	Cmd      []string `json:"cmd,omitempty"`
	Primary  bool     `json:"primary"`
}

// Template 是 app-deploy.templates 返回的一项模板元信息。
type Template struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Description       string       `json:"description"`
	Image             string       `json:"image"`
	Ports             []PortSpec   `json:"ports"`
	Volumes           []VolumeSpec `json:"volumes"`
	Env               []EnvField   `json:"env"`
	EstimatedMemoryMB int          `json:"estimated_memory_mb"`
	Services          []Service    `json:"services"`
}

// PrimaryService 返回主容器（模板内 Primary=true 的服务）。
func (t *Template) PrimaryService() (Service, bool) {
	for _, s := range t.Services {
		if s.Primary {
			return s, true
		}
	}
	return Service{}, false
}

// Service 按名字查服务。
func (t *Template) Service(name string) (Service, bool) {
	for _, s := range t.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// templatesParams 是 app-deploy.templates 的入参（允许空对象）。
type templatesParams struct{}

// deployParams 是 app-deploy.deploy 的入参。
type deployParams struct {
	Template string            `json:"template"`
	Name     string            `json:"name"`
	Ports    []PortRequest     `json:"ports"`
	Env      map[string]string `json:"env"`
	Volumes  []VolumeRequest   `json:"volumes"`
}

// PortRequest 是一条端口覆盖：只允许模板已声明的容器端口。
type PortRequest struct {
	ContainerPort int `json:"container_port"`
	HostPort      int `json:"host_port"`
}

// VolumeRequest 是挂载目录名覆盖：仍限制在插件状态目录内，不接受任意宿主机路径。
type VolumeRequest struct {
	Name   string `json:"name"`
	Subdir string `json:"subdir"`
}

// listParams 是 app-deploy.list 的入参（允许空对象）。
type listParams struct{}

// removeParams 是 app-deploy.remove 的入参；confirm 必须为 true。
type removeParams struct {
	Name    string `json:"name"`
	Confirm bool   `json:"confirm"`
}

// ResolvedPort 是一条已解析的端口映射（含最终 access_url）。
type ResolvedPort struct {
	Service       string `json:"service"`
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	Protocol      string `json:"protocol"`
	AccessURL     string `json:"access_url"`
}

// ContainerRef 是一个容器在记录中的身份（服务名 + 容器名 + 完整 ID）。
type ContainerRef struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	ID      string `json:"id"`
}

// VolumeRef 是一条已创建的挂载（named 卷记录 docker 卷名，bind 记录宿主机路径）。
type VolumeRef struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Deployment 是一条部署记录，落盘到 <state>/deployments.json（0600）。
// 记录只存容器身份与端口，不存 env 值（真实口令永不落盘）。
type Deployment struct {
	ID          string         `json:"id"`
	Template    string         `json:"template"`
	Name        string         `json:"name"`
	ContainerID string         `json:"container_id"`
	Containers  []ContainerRef `json:"containers"`
	Ports       []ResolvedPort `json:"ports"`
	Volumes     []VolumeRef    `json:"volumes,omitempty"`
	Network     string         `json:"network,omitempty"`
	CreatedAt   string         `json:"created_at"`
	Status      string         `json:"status"`
}

// stateFile 是 deployments.json 的完整形状（显式版本，便于后续迁移）。
type stateFile struct {
	Version     int          `json:"version"`
	Deployments []Deployment `json:"deployments"`
}

// TemplatesResult 是 app-deploy.templates 的结构化结果。
type TemplatesResult struct {
	Templates []Template `json:"templates"`
	Count     int        `json:"count"`
}

// DeployResult 是 app-deploy.deploy 的结构化结果。
type DeployResult struct {
	Deployed    bool           `json:"deployed"`
	ID          string         `json:"id"`
	Template    string         `json:"template"`
	Name        string         `json:"name"`
	ContainerID string         `json:"container_id"`
	AccessURL   string         `json:"access_url"`
	Ports       []ResolvedPort `json:"ports"`
	Containers  []ContainerRef `json:"containers"`
	CreatedAt   string         `json:"created_at"`
}

// ContainerView 是 list 中单个容器的实际状态。
type ContainerView struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	Status  string `json:"status"`
}

// DeploymentView 是 list 中的一条部署（status 为按 docker ps 计算的真实状态）。
type DeploymentView struct {
	ID          string          `json:"id"`
	Template    string          `json:"template"`
	Name        string          `json:"name"`
	ContainerID string          `json:"container_id"`
	Status      string          `json:"status"`
	Ports       []ResolvedPort  `json:"ports"`
	AccessURL   string          `json:"access_url"`
	CreatedAt   string          `json:"created_at"`
	Containers  []ContainerView `json:"containers"`
	// OutsideInstall 为 true 表示该应用的 bind 挂载落在安装目录之外
	// （ADR-0023 阶段 C 之前部署的历史应用）；插件不自动搬迁。
	OutsideInstall bool `json:"outside_install"`
	// Hint 是 outside_install=true 时的可操作提示；正常应用为空串省略。
	Hint string `json:"hint,omitempty"`
	// Usage 是这条部署的聚合资源占用概览；执行面不支持采样或无运行中容器时省略。
	Usage *UsageSummary `json:"usage,omitempty"`
}

// ListResult 是 app-deploy.list 的结构化结果。
type ListResult struct {
	Deployments []DeploymentView `json:"deployments"`
	Count       int              `json:"count"`
	// UsageError 说明本次资源占用采样为何没拿到（有值即所有部署的 usage 都为空）：
	// 展示性数据缺失要能被看见，而不是让用户以为"应用不占资源"。
	UsageError string `json:"usage_error,omitempty"`
}

// RemoveResult 是 app-deploy.remove 的结构化结果。
type RemoveResult struct {
	Removed    bool     `json:"removed"`
	Name       string   `json:"name"`
	Containers []string `json:"containers"`
	Volumes    []string `json:"volumes,omitempty"`
	Network    string   `json:"network,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// PortBinding 是一条容器端口到宿主机端口的映射；宿主机侧固定绑 127.0.0.1。
type PortBinding struct {
	ContainerPort int
	HostPort      int
	Protocol      string
}

// RunVolume 是一条 docker run 挂载。
type RunVolume struct {
	Source   string
	Target   string
	ReadOnly bool
}

// ContainerSpec 是一次 docker run 的完整描述；由 Runtime 组装、Client 转 argv。
// 镜像来自内置模板目录，不接受调用方传入。
type ContainerSpec struct {
	Name     string
	Image    string
	Cmd      []string
	EnvFile  string
	Ports    []PortBinding
	Volumes  []RunVolume
	MemoryMB int
	Network  string
	Labels   map[string]string
}

// ContainerState 是 docker ps 的一条快照。
type ContainerState struct {
	ID     string
	Name   string
	Image  string
	State  string
	Status string
	Ports  string
}

// Executor 是部署执行面：真实实现是本机 docker CLI，测试可注入故障或假数据。
type Executor interface {
	// RunContainer 执行 docker run -d，返回容器完整 ID。
	RunContainer(spec ContainerSpec) (string, error)
	// ListContainers 返回全部容器（含已停止）的快照。
	ListContainers() ([]ContainerState, error)
	// RemoveContainers 删除容器（-f，先停后删）。
	RemoveContainers(ids []string) error
	// RemoveVolumes 删除命名卷（不存在视为成功）。
	RemoveVolumes(names []string) error
	// CreateNetwork / RemoveNetwork 管理部署专用网络。
	CreateNetwork(name string) error
	RemoveNetwork(name string) error
}

// Controller 是执行面的**可选**控制能力：停止 / 启动 / 读日志 / 采样资源占用。
//
// 它与 Executor 分开是刻意的：Executor 是部署必经面（每个测试替身都必须实现），
// 而控制面只服务「部署后管理」，不值得让边界 / 故障用例的替身跟着变长。Runtime
// 用类型断言探测；不支持时返回 E_UNSUPPORTED 并写明缺什么，不做静默降级（G-16）。
type Controller interface {
	// StopContainers 优雅停止容器（先 SIGTERM 后 SIGKILL 由 docker 负责）。
	StopContainers(ids []string) error
	// StartContainers 启动已停止的容器。
	StartContainers(ids []string) error
	// ContainerLogs 读取单个容器最近的 tail 行日志（stdout + stderr 合并）。
	ContainerLogs(name string, tail int) (string, error)
	// SampleUsage 采样容器的一次性资源占用（docker stats --no-stream）。
	SampleUsage(names []string) ([]ContainerUsage, error)
}

// ContainerUsage 是单个容器的一次性资源占用采样。
type ContainerUsage struct {
	Name string
	// CPUPercent 是 docker stats 的瞬时 CPU 百分比（单核为 100%）。
	CPUPercent float64
	// MemUsedBytes / MemLimitBytes 是内存占用与限额（字节）。
	MemUsedBytes  int64
	MemLimitBytes int64
}

// UsageSummary 是一条部署（可能多容器）的聚合资源占用。
type UsageSummary struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemUsedBytes  int64   `json:"mem_used_bytes"`
	MemLimitBytes int64   `json:"mem_limit_bytes"`
}

// controlParams 是 app-deploy.stop / app-deploy.start 的入参。
type controlParams struct {
	Name string `json:"name"`
}

// logsParams 是 app-deploy.logs 的入参；Service 留空取主容器。
type logsParams struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	Tail    int    `json:"tail"`
}

// ControlResult 是 stop / start 的结果。
type ControlResult struct {
	Name       string   `json:"name"`
	Action     string   `json:"action"`
	Status     string   `json:"status"`
	Containers []string `json:"containers"`
}

// LogsResult 是 app-deploy.logs 的结果：逐行返回，便于前端等宽展示与裁剪。
type LogsResult struct {
	Name      string   `json:"name"`
	Service   string   `json:"service"`
	Container string   `json:"container"`
	Tail      int      `json:"tail"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// Options 是运行期配置：由 main 从环境变量组装，测试用零值 + 注入项。
type Options struct {
	// Root 是数据根目录；空串取 DefaultRoot。
	Root string
	// StateDir 是状态目录；空串取 <Root>/state。
	StateDir string
	// DockerBin 是 docker CLI 名或绝对路径；空串取 DefaultDockerBin。
	DockerBin string
	// Timeout 是单条 docker 命令超时；<=0 取 DefaultTimeout。
	Timeout time.Duration
	// PortProbe 判断宿主机端口是否可用（true 可用）；nil 时用真实绑定探测。
	PortProbe func(port int) bool
	// GOOS 为空时取 runtime.GOOS；测试可注入 "darwin" 验证 E_UNSUPPORTED。
	GOOS string
}

// normalize 填充默认值。
func (o Options) normalize() Options {
	if o.Root == "" {
		o.Root = DefaultRoot
	}
	if o.StateDir == "" {
		o.StateDir = o.Root + "/" + "state"
	}
	if o.DockerBin == "" {
		o.DockerBin = DefaultDockerBin
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.PortProbe == nil {
		o.PortProbe = probePort
	}
	return o
}

// EnvService 返回一项 env 归属的服务名；未声明时归主服务。
func (t *Template) EnvService(field EnvField) (Service, bool) {
	if field.Service == "" {
		return t.PrimaryService()
	}
	return t.Service(field.Service)
}
