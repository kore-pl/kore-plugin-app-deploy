package apop

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// nameRE 是部署名（= 容器名前缀）的合法形状：小写字母开头，其后小写字母/数字/连字符，
// 总长 1..31。封死空白、斜杠、点、下划线、分号、$()、引号、NUL、以 "-" 开头等注入形态。
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// subdirRE 是挂载子目录名的合法形状（仍落在插件状态目录内）。
var subdirRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// envKeyRE 是环境变量名的合法形状。
var envKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// shellMeta 是 env 值里一律拒绝的字符集：shell 元字符 + 控制字符。
// 部署走固定 argv（不经 shell），这里是第二道防线：防止值被下游写进脚本/配置时被解释。
const shellMeta = "`$|&;<>()*?[]{}\\\"'~!#\n\r\t\v\f\x00"

// ValidateName 校验部署名：必须匹配 nameRE，且以 kore-app- 开头（容器命名空间隔离）。
// 不访问 docker、不写文件。
func ValidateName(raw string) *pluginapi.Error {
	if raw == "" {
		return invalid("name 不能为空（容器名前缀，须以 kore-app- 开头）")
	}
	if len(raw) > 31 || !nameRE.MatchString(raw) {
		return invalid(fmt.Sprintf("name 非法 %q：须匹配 ^[a-z][a-z0-9-]{0,30}$（小写字母开头，只允许小写字母/数字/连字符，最长 31）", raw))
	}
	if strings.HasPrefix(raw, "-") || strings.HasSuffix(raw, "-") || strings.Contains(raw, "--") {
		return invalid(fmt.Sprintf("name 非法 %q：不得以连字符开头/结尾，也不得出现连续连字符", raw))
	}
	if !strings.HasPrefix(raw, ContainerPrefix) {
		return invalid(fmt.Sprintf("name 非法 %q：必须以 %s 开头，避免与其它容器冲突并便于识别", raw, ContainerPrefix))
	}
	return nil
}

// ValidateEnvValue 校验单个 env 值：长度 ≤256、不含 shell 元字符与控制字符。
func ValidateEnvValue(key, value string) *pluginapi.Error {
	if len(value) > MaxEnvValueLen {
		return invalid(fmt.Sprintf("env %s 的值长度 %d 超过上限 %d 字节", key, len(value), MaxEnvValueLen))
	}
	if strings.TrimSpace(value) != value {
		return invalid("env " + key + " 的值不能以空白开头或结尾")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return invalid("env " + key + " 的值含控制字符")
		}
		if strings.ContainsRune(shellMeta, r) {
			return invalid(fmt.Sprintf("env %s 的值含 shell 元字符 %q，已拒绝", key, r))
		}
	}
	return nil
}

// envPlan 是 env 解析结果：按服务归集的 KEY=VALUE，键有序保证 argv/file 稳定。
type envPlan struct {
	values map[string]map[string]string
	keys   map[string][]string
}

// newEnvPlan 构造按服务归集的空计划。
func newEnvPlan() *envPlan {
	return &envPlan{values: map[string]map[string]string{}, keys: map[string][]string{}}
}

// set 写入一项；服务内保持首次写入顺序（不排序，便于模板声明顺序即输出顺序）。
func (p *envPlan) set(service, key, value string) {
	if p.values[service] == nil {
		p.values[service] = map[string]string{}
	}
	if _, exists := p.values[service][key]; !exists {
		p.keys[service] = append(p.keys[service], key)
	}
	p.values[service][key] = value
}

// service 返回某服务的全部 KEY=VALUE（按声明顺序）。
func (p *envPlan) forService(service string) []string {
	out := make([]string, 0, len(p.keys[service]))
	for _, key := range p.keys[service] {
		out = append(out, key+"="+p.values[service][key])
	}
	return out
}

// get 读取某服务的某项值（不存在返回空串）。
func (p *envPlan) get(service, key string) string {
	return p.values[service][key]
}

// validateEnv 校验用户 env 并生成注入计划：只允许模板声明的键，必填项必须齐全，
// 值过 ValidateEnvValue；secret 项留空则交给 generate 生成随机值（不落盘）。
func validateEnv(t *Template, provided map[string]string, generate func() (string, error)) (*envPlan, *pluginapi.Error) {
	if len(provided) > MaxEnvEntries {
		return nil, invalid(fmt.Sprintf("env 条目数 %d 超过上限 %d", len(provided), MaxEnvEntries))
	}
	declared := make(map[string]EnvField, len(t.Env))
	for _, field := range t.Env {
		declared[field.Key] = field
	}
	// 拒绝模板未声明的键：白名单外一律不放行，避免往容器塞任意变量。
	unknown := make([]string, 0)
	for key := range provided {
		if _, ok := declared[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		allowed := make([]string, 0, len(t.Env))
		for _, field := range t.Env {
			allowed = append(allowed, field.Key)
		}
		return nil, invalid(fmt.Sprintf("env 含模板 %s 未声明的键 %s（允许：%s）",
			t.ID, strings.Join(unknown, ", "), strings.Join(allowed, ", ")))
	}
	plan := newEnvPlan()
	missing := make([]string, 0)
	for _, field := range t.Env {
		if !envKeyRE.MatchString(field.Key) {
			return nil, internalErr("模板 " + t.ID + " 的 env 键非法: " + field.Key)
		}
		value := provided[field.Key]
		if perr := ValidateEnvValue(field.Key, value); perr != nil {
			return nil, perr
		}
		if value == "" {
			value = field.Default
		}
		if value == "" {
			// 必填优先：required 的项绝不静默生成口令，必须由调用方显式提供。
			if field.Required {
				missing = append(missing, field.Key)
				continue
			}
			if field.Secret {
				generated, err := generate()
				if err != nil {
					return nil, internalErr("生成随机口令失败: " + err.Error())
				}
				value = generated
			}
		}
		if value == "" {
			continue
		}
		service, ok := t.EnvService(field)
		if !ok {
			return nil, internalErr("模板 " + t.ID + " 的 env " + field.Key + " 指向不存在的服务 " + field.Service)
		}
		plan.set(service.Name, field.Key, value)
	}
	if len(missing) > 0 {
		return nil, invalid("请填写必填的环境变量 env: " + strings.Join(missing, ", "))
	}
	return plan, nil
}

// validatePortRequests 校验端口覆盖：容器端口必须是模板已声明的，宿主机端口必须在
// 1024..65535，同一请求内不得重复。
func validatePortRequests(t *Template, requests []PortRequest) *pluginapi.Error {
	published := make(map[int]PortSpec, len(t.Ports))
	for _, spec := range t.Ports {
		published[spec.ContainerPort] = spec
	}
	seenContainer := make(map[int]bool, len(requests))
	seenHost := make(map[int]bool, len(requests))
	for _, req := range requests {
		if _, ok := published[req.ContainerPort]; !ok {
			allowed := make([]int, 0, len(t.Ports))
			for _, spec := range t.Ports {
				allowed = append(allowed, spec.ContainerPort)
			}
			return invalid(fmt.Sprintf("ports 含模板 %s 未声明的容器端口 %d（允许：%v）", t.ID, req.ContainerPort, allowed))
		}
		if err := ValidateHostPort(req.HostPort); err != nil {
			return err
		}
		if seenContainer[req.ContainerPort] {
			return invalid(fmt.Sprintf("ports 中容器端口 %d 重复", req.ContainerPort))
		}
		if seenHost[req.HostPort] {
			return invalid(fmt.Sprintf("ports 中宿主机端口 %d 重复", req.HostPort))
		}
		seenContainer[req.ContainerPort] = true
		seenHost[req.HostPort] = true
	}
	return nil
}

// ValidateHostPort 校验宿主机端口落在非特权区间 1024..65535。
func ValidateHostPort(port int) *pluginapi.Error {
	if port < MinPort || port > MaxPort {
		return invalid(fmt.Sprintf("宿主机端口 %d 非法：必须在 %d..%d 之间（禁止 80/443/22 等特权端口）",
			port, MinPort, MaxPort))
	}
	return nil
}

// validateVolumeRequests 校验挂载覆盖：卷名必须是模板声明的，子目录名受限、
// 不得重复。宿主路径不接受用户输入，一律由插件在状态目录内派生。
func validateVolumeRequests(t *Template, requests []VolumeRequest) *pluginapi.Error {
	declared := make(map[string]VolumeSpec, len(t.Volumes))
	for _, spec := range t.Volumes {
		declared[spec.Name] = spec
	}
	seen := make(map[string]bool, len(requests))
	for _, req := range requests {
		if _, ok := declared[req.Name]; !ok {
			allowed := make([]string, 0, len(t.Volumes))
			for _, spec := range t.Volumes {
				allowed = append(allowed, spec.Name)
			}
			return invalid(fmt.Sprintf("volumes 含模板 %s 未声明的卷 %q（允许：%s）", t.ID, req.Name, strings.Join(allowed, ", ")))
		}
		if !subdirRE.MatchString(req.Subdir) {
			return invalid(fmt.Sprintf("volumes.%s 的子目录名 %q 非法：须匹配 ^[a-z0-9][a-z0-9_-]{0,30}$", req.Name, req.Subdir))
		}
		if seen[req.Name] {
			return invalid("volumes 中卷 " + req.Name + " 重复")
		}
		seen[req.Name] = true
	}
	return nil
}

// resolvePorts 把模板端口 + 用户覆盖解析为最终端口映射。
//
// 规则：显式指定的宿主机端口被占用 → E_CONFLICT（不静默改）；未指定 → 优先用模板
// default_host_port，占用则从该端口向上找第一个可用端口（≤65535），找不到 → E_CONFLICT。
func resolvePorts(t *Template, requests []PortRequest, busy map[int]string, probe func(int) bool) ([]ResolvedPort, *pluginapi.Error) {
	overrides := make(map[int]int, len(requests))
	for _, req := range requests {
		overrides[req.ContainerPort] = req.HostPort
	}
	used := make(map[int]bool, len(t.Ports))
	resolved := make([]ResolvedPort, 0, len(t.Ports))
	for _, spec := range t.Ports {
		hostPort, explicit := overrides[spec.ContainerPort]
		if explicit {
			if holder, taken := busy[hostPort]; taken {
				return nil, conflict(fmt.Sprintf("宿主机端口 %d 已被占用（%s），请换一个端口", hostPort, holder))
			}
			if used[hostPort] {
				return nil, conflict(fmt.Sprintf("宿主机端口 %d 在本请求内重复", hostPort))
			}
		} else {
			port, perr := pickPort(spec.DefaultHost, used, busy, probe)
			if perr != nil {
				return nil, perr
			}
			hostPort = port
		}
		used[hostPort] = true
		resolved = append(resolved, ResolvedPort{
			Service:       spec.Service,
			ContainerPort: spec.ContainerPort,
			HostPort:      hostPort,
			Protocol:      spec.Protocol,
			AccessURL:     spec.AccessPrefix + "127.0.0.1:" + strconv.Itoa(hostPort) + "/",
		})
	}
	return resolved, nil
}

// pickPort 从 start 向上找第一个既未被本请求占用、又未被宿主占用的端口。
func pickPort(start int, used map[int]bool, busy map[int]string, probe func(int) bool) (int, *pluginapi.Error) {
	if start < MinPort {
		start = MinPort
	}
	for port := start; port <= MaxPort; port++ {
		if used[port] {
			continue
		}
		if _, taken := busy[port]; taken {
			continue
		}
		if probe != nil && !probe(port) {
			continue
		}
		return port, nil
	}
	return 0, conflict(fmt.Sprintf("没有可用端口：从 %d 向上扫描到 %d 全部被占用", start, MaxPort))
}
