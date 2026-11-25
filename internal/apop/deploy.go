package apop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callDeploy 处理 app-deploy.deploy：校验 → 组容器规格 → docker run → 写部署记录。
//
// 失败一律回滚（删已建容器与网络、不写记录），不留半拉部署。
func (r *Runtime) callDeploy(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p deployParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if perr := ValidateName(p.Name); perr != nil {
		return nil, perr
	}
	tpl := LookupTemplate(p.Template)
	if tpl == nil {
		return nil, invalid("模板不在内置目录内: " + p.Template + "（允许：" + templateIDs() + "）")
	}
	if perr := validatePortRequests(tpl, p.Ports); perr != nil {
		return nil, perr
	}
	if perr := validateVolumeRequests(tpl, p.Volumes); perr != nil {
		return nil, perr
	}
	envPlan, perr := validateEnv(tpl, p.Env, randomSecret)
	if perr != nil {
		return nil, perr
	}
	if perr := deriveEnv(tpl, p.Name, envPlan); perr != nil {
		return nil, perr
	}

	if err := ensureStateDir(r.opts.StateDir); err != nil {
		return nil, internalErr("创建状态目录失败: " + err.Error())
	}
	st, perr := loadState(r.opts.StateDir)
	if perr != nil {
		return nil, perr
	}
	if findDeployment(st, p.Name) >= 0 {
		return nil, conflict("同名部署已存在: " + p.Name + "（先 app-deploy.remove 再部署）")
	}
	states, perr := r.snapshot()
	if perr != nil {
		return nil, perr
	}
	if perr := checkContainerNamesFree(tpl, p.Name, states); perr != nil {
		return nil, perr
	}
	busy := busyFromStates(states)
	ports, perr := resolvePorts(tpl, p.Ports, busy, r.opts.PortProbe)
	if perr != nil {
		return nil, perr
	}
	volumeRefs, perr := r.prepareVolumes(tpl, p.Name, p.Volumes)
	if perr != nil {
		return nil, perr
	}
	deploymentID, err := randomID(8)
	if err != nil {
		return nil, internalErr("生成部署 id 失败: " + err.Error())
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)

	network := ""
	if len(tpl.Services) > 1 {
		network = p.Name + "-net"
		if err := r.exec.CreateNetwork(network); err != nil {
			return nil, classify(err)
		}
	}
	created := make([]ContainerRef, 0, len(tpl.Services))
	for _, svc := range startOrder(tpl) {
		spec := buildContainerSpec(tpl, p.Name, svc, ports, volumeRefs, network, deploymentID)
		id, runErr := r.runService(spec, envPlan.forService(svc.Name))
		if runErr != nil {
			return nil, r.failWithRollback(classify(runErr), created, network)
		}
		created = append(created, ContainerRef{Service: svc.Name, Name: spec.Name, ID: id})
	}

	record := Deployment{
		ID:          deploymentID,
		Template:    tpl.ID,
		Name:        p.Name,
		ContainerID: primaryContainerID(tpl, created),
		Containers:  created,
		Ports:       ports,
		Volumes:     volumeRefs,
		Network:     network,
		CreatedAt:   createdAt,
		Status:      StatusRunning,
	}
	// 落记录是「读-改-写」：必须持状态锁重读最新快照再插入。上面第 46 行的
	// loadState 只用于快速失败，它的快照可能已被并发写者推进——直接拿旧快照
	// upsert 会把别人的新增整段覆盖（8 并发只剩 1 条）。
	// 锁只覆盖读-改-写，不覆盖前面的 docker run（否则并发部署被完全串行化）。
	saveErr := func() *pluginapi.Error {
		unlock, perr := lockState(r.opts.StateDir)
		if perr != nil {
			return perr
		}
		defer unlock()
		fresh, perr := loadState(r.opts.StateDir)
		if perr != nil {
			return perr
		}
		if findDeployment(fresh, p.Name) >= 0 {
			return conflict("同名部署已存在: " + p.Name + "（并发同名请求已先落记录，本次已回滚）")
		}
		upsertDeployment(&fresh, record)
		return saveState(r.opts.StateDir, fresh)
	}()
	if saveErr != nil {
		return nil, r.failWithRollback(saveErr, created, network)
	}
	return encode(DeployResult{
		Deployed:    true,
		ID:          record.ID,
		Template:    record.Template,
		Name:        record.Name,
		ContainerID: record.ContainerID,
		AccessURL:   accessURL(tpl, ports),
		Ports:       ports,
		Containers:  created,
		CreatedAt:   createdAt,
	})
}

// snapshot 读取 docker 的容器快照。
func (r *Runtime) snapshot() ([]ContainerState, *pluginapi.Error) {
	states, err := r.exec.ListContainers()
	if err != nil {
		return nil, classify(err)
	}
	return states, nil
}

// busyFromStates 汇总正在运行的容器已发布的宿主机端口。
func busyFromStates(states []ContainerState) map[int]string {
	busy := make(map[int]string, len(states))
	for _, state := range states {
		if state.State != StatusRunning {
			continue
		}
		for _, port := range ParseHostPorts(state.Ports) {
			busy[port] = "容器 " + state.Name + " 已发布"
		}
	}
	return busy
}

// checkContainerNamesFree 拒绝与已有容器重名的部署（含 wordpress 的 -db 派生名）。
func checkContainerNamesFree(tpl *Template, deployment string, states []ContainerState) *pluginapi.Error {
	existing := make(map[string]bool, len(states))
	for _, state := range states {
		existing[state.Name] = true
	}
	for _, svc := range tpl.Services {
		name := containerName(deployment, svc)
		if existing[name] {
			return conflict("容器名已被占用: " + name + "（先删除同名容器或换一个 name）")
		}
	}
	return nil
}

// startOrder 返回启动顺序：非主容器（数据库等依赖）先起，主容器最后。
func startOrder(tpl *Template) []Service {
	ordered := make([]Service, 0, len(tpl.Services))
	for _, svc := range tpl.Services {
		if !svc.Primary {
			ordered = append(ordered, svc)
		}
	}
	for _, svc := range tpl.Services {
		if svc.Primary {
			ordered = append(ordered, svc)
		}
	}
	return ordered
}

// containerName 派生容器名：主容器用部署名本身，其余用 <部署名>-<服务名>。
// 部署名已强制以 kore-app- 开头，因此全部容器名天然带前缀。
func containerName(deployment string, svc Service) string {
	if svc.Primary {
		return deployment
	}
	return deployment + "-" + svc.Name
}

// buildContainerSpec 把一个服务组装成 docker run 规格（镜像来自模板常量）。
func buildContainerSpec(tpl *Template, deployment string, svc Service, ports []ResolvedPort, volumes []VolumeRef, network, deploymentID string) ContainerSpec {
	spec := ContainerSpec{
		Name:     containerName(deployment, svc),
		Image:    svc.Image,
		Cmd:      svc.Cmd,
		MemoryMB: svc.MemoryMB,
		Network:  network,
		Labels: map[string]string{
			LabelManaged:    "true",
			LabelDeployment: deploymentID,
			LabelTemplate:   tpl.ID,
		},
	}
	for _, port := range ports {
		if port.Service != svc.Name {
			continue
		}
		spec.Ports = append(spec.Ports, PortBinding{
			ContainerPort: port.ContainerPort,
			HostPort:      port.HostPort,
			Protocol:      port.Protocol,
		})
	}
	for idx, vspec := range tpl.Volumes {
		if vspec.Service != svc.Name || idx >= len(volumes) {
			continue
		}
		spec.Volumes = append(spec.Volumes, RunVolume{Source: volumes[idx].Name, Target: vspec.ContainerPath})
	}
	return spec
}

// runService 写 0600 环境变量文件（真口令不进 argv）→ docker run → 立即删除该文件。
func (r *Runtime) runService(spec ContainerSpec, envPairs []string) (string, error) {
	envFile, err := r.writeEnvFile(envPairs)
	if err != nil {
		return "", err
	}
	if envFile != "" {
		defer func() { _ = os.Remove(envFile) }()
		spec.EnvFile = envFile
	}
	return r.exec.RunContainer(spec)
}

// writeEnvFile 在状态目录写 0600 的 KEY=VALUE 文件；无环境变量时返回空串。
func (r *Runtime) writeEnvFile(pairs []string) (string, error) {
	if len(pairs) == 0 {
		return "", nil
	}
	if err := ensureStateDir(r.opts.StateDir); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(r.opts.StateDir, ".env-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	cleanup := func() { _ = file.Close(); _ = os.Remove(name) }
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return "", err
	}
	if _, err := file.WriteString(strings.Join(pairs, "\n") + "\n"); err != nil {
		cleanup()
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// prepareVolumes 创建挂载：named 卷交给 docker 管理，bind 卷在状态目录内建目录。
// 用户不能指定任意宿主机路径——挂载只能落在插件状态目录内，防止容器读写宿主任意位置。
func (r *Runtime) prepareVolumes(tpl *Template, deployment string, overrides []VolumeRequest) ([]VolumeRef, *pluginapi.Error) {
	subdirs := make(map[string]string, len(overrides))
	for _, override := range overrides {
		subdirs[override.Name] = override.Subdir
	}
	refs := make([]VolumeRef, 0, len(tpl.Volumes))
	for _, spec := range tpl.Volumes {
		subdir, ok := subdirs[spec.Name]
		if !ok {
			subdir = spec.Name
		}
		switch spec.Kind {
		case VolumeNamed:
			refs = append(refs, VolumeRef{Name: deployment + "-" + subdir, Kind: VolumeNamed})
		case VolumeBind:
			host := filepath.Join(r.opts.StateDir, VolumesDir, deployment, subdir)
			if err := os.MkdirAll(host, 0o755); err != nil {
				return nil, internalErr("创建挂载目录失败: " + err.Error())
			}
			if err := os.Chmod(host, 0o755); err != nil {
				return nil, internalErr("设置挂载目录权限失败: " + err.Error())
			}
			if err := seedBindVolume(tpl.ID, spec.Name, host, deployment); err != nil {
				return nil, internalErr("初始化挂载目录失败: " + err.Error())
			}
			refs = append(refs, VolumeRef{Name: host, Kind: VolumeBind})
		default:
			return nil, internalErr("模板 " + tpl.ID + " 的卷类型非法: " + spec.Kind)
		}
	}
	return refs, nil
}

// bindSeed 是写进用户可见挂载目录的示例文件：空目录会让「一键部署」成功后打开是
// 403 / 白页 / 容器直接退出（node 找不到入口就退出），用户会以为部署失败。
type bindSeed struct {
	fileName string
	render   func(deployment string) string
}

// bindSeeds 按「模板 id + "/" + 卷名」登记示例文件；未登记的卷不写入任何东西。
var bindSeeds = map[string]bindSeed{
	"nginx-static/html": {
		fileName: "index.html",
		render: func(deployment string) string {
			return strings.Join([]string{
				"<!doctype html>",
				`<html lang="zh-CN"><head><meta charset="utf-8"><title>Kore app-deploy</title></head>`,
				"<body><h1>Kore</h1>",
				"<p>nginx-static / " + deployment + "</p>",
				"<p>把站点文件放进本目录即可替换此页。</p>",
				"</body></html>",
				"",
			}, "\n")
		},
	},
	"node-app/app": {
		fileName: "server.js",
		render: func(deployment string) string {
			return strings.Join([]string{
				"// Kore 应用一键部署 · Node.js 示例入口（" + deployment + "）",
				"// 零依赖：只用 node 内置 http；改成你自己的入口后重启容器即生效。",
				"const http = require('node:http');",
				"const port = Number(process.env.PORT || 3000);",
				"const server = http.createServer((req, res) => {",
				"  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });",
				"  res.end('<h1>Kore</h1>' +",
				"    '<p>Node.js 示例已启动（' + req.url + '）。把 server.js 换成你的应用入口即可。</p>');",
				"});",
				"server.listen(port, '0.0.0.0', () => {",
				"  console.log('Kore app-deploy: Node.js 示例监听 ' + port);",
				"});",
				"",
			}, "\n")
		},
	},
	"python-app/app": {
		fileName: "app.py",
		render: func(deployment string) string {
			return strings.Join([]string{
				"# Kore 应用一键部署 · Python 示例入口（" + deployment + "）",
				"# 零依赖：只用标准库；把 app.py 换成 Flask / FastAPI 入口即可（镜像内需自带依赖）。",
				"import http.server",
				"import os",
				"",
				"",
				"class Handler(http.server.BaseHTTPRequestHandler):",
				"    def do_GET(self):",
				"        body = ('<meta charset=utf-8><h1>Kore</h1>'",
				"                '<p>Python 示例已启动（%s）。把 app.py 换成你的应用入口即可。</p>'",
				"                % self.path).encode('utf-8')",
				"        self.send_response(200)",
				"        self.send_header('Content-Type', 'text/html; charset=utf-8')",
				"        self.send_header('Content-Length', str(len(body)))",
				"        self.end_headers()",
				"        self.wfile.write(body)",
				"",
				"    def log_message(self, fmt, *args):",
				"        print('Kore app-deploy: Python 示例 ' + (fmt % args))",
				"",
				"",
				"port = int(os.environ.get('PORT', '8000'))",
				"http.server.HTTPServer(('0.0.0.0', port), Handler).serve_forever()",
				"",
			}, "\n")
		},
	},
	"php-site/html": {
		fileName: "index.php",
		render: func(deployment string) string {
			return strings.Join([]string{
				"<?php",
				"// Kore 应用一键部署 · PHP 示例首页（" + deployment + "）",
				"header('Content-Type: text/html; charset=utf-8');",
				"$path = isset($_SERVER['REQUEST_URI']) ? $_SERVER['REQUEST_URI'] : '/';",
				"echo '<meta charset=utf-8><h1>Kore</h1>';",
				"echo '<p>PHP 示例已启动（' . htmlspecialchars($path, ENT_QUOTES) . '）。把站点文件放进本目录即可替换此页。</p>';",
				"",
			}, "\n")
		},
	},
}

// seedBindVolume 给已登记的挂载目录放一个示例文件；已有同名文件时绝不覆盖。
func seedBindVolume(templateID, volumeName, dir, deployment string) error {
	seed, ok := bindSeeds[templateID+"/"+volumeName]
	if !ok {
		return nil
	}
	target := filepath.Join(dir, seed.fileName)
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	if err := os.WriteFile(target, []byte(seed.render(deployment)), 0o644); err != nil {
		return err
	}
	return os.Chmod(target, 0o644)
}

// accessURL 取主服务第一条端口映射的访问地址。
func accessURL(tpl *Template, ports []ResolvedPort) string {
	primary, ok := tpl.PrimaryService()
	if ok {
		for _, port := range ports {
			if port.Service == primary.Name {
				return port.AccessURL
			}
		}
	}
	if len(ports) > 0 {
		return ports[0].AccessURL
	}
	return ""
}

// primaryContainerID 取主容器 ID；没有主容器时退化为第一个容器。
func primaryContainerID(tpl *Template, created []ContainerRef) string {
	primary, ok := tpl.PrimaryService()
	if ok {
		for _, ref := range created {
			if ref.Service == primary.Name {
				return ref.ID
			}
		}
	}
	if len(created) > 0 {
		return created[0].ID
	}
	return ""
}

// failWithRollback 回滚已创建的资源，并把回滚告警拼进错误信息（不静默吞掉残留）。
func (r *Runtime) failWithRollback(perr *pluginapi.Error, created []ContainerRef, network string) *pluginapi.Error {
	warnings := r.rollback(created, network)
	if len(warnings) == 0 {
		return perr
	}
	perr.Message += "；回滚告警: " + strings.Join(warnings, "; ")
	return perr
}

// rollback 尽力删除已建容器与网络，返回未能清理的部分。
func (r *Runtime) rollback(created []ContainerRef, network string) []string {
	warnings := make([]string, 0, 2)
	ids := make([]string, 0, len(created))
	for _, ref := range created {
		ids = append(ids, ref.ID)
	}
	if len(ids) > 0 {
		if err := r.exec.RemoveContainers(ids); err != nil {
			warnings = append(warnings, "删除容器失败: "+err.Error())
		}
	}
	if network != "" {
		if err := r.exec.RemoveNetwork(network); err != nil {
			warnings = append(warnings, "删除网络失败: "+err.Error())
		}
	}
	return warnings
}

// deriveEnv 为多容器模板补齐内部连接变量（wordpress → mysql、phpmyadmin → mysql）。
// 内部变量不在模板 env 白名单里，用户既看不到也改不了；值来自已校验/已生成的 db 环境变量
// 与容器名（多容器模板同网段，按容器名互相解析）。
func deriveEnv(tpl *Template, deployment string, plan *envPlan) *pluginapi.Error {
	db, hasDB := tpl.Service("db")
	web, hasWeb := tpl.PrimaryService()
	if !hasDB || !hasWeb {
		return nil
	}
	switch tpl.ID {
	case "wordpress":
		plan.set(web.Name, "WORDPRESS_DB_HOST", containerName(deployment, db)+":3306")
		plan.set(web.Name, "WORDPRESS_DB_NAME", plan.get(db.Name, "MYSQL_DATABASE"))
		plan.set(web.Name, "WORDPRESS_DB_USER", plan.get(db.Name, "MYSQL_USER"))
		plan.set(web.Name, "WORDPRESS_DB_PASSWORD", plan.get(db.Name, "MYSQL_PASSWORD"))
	case "mysql-phpmyadmin":
		// phpMyAdmin 用内部容器名连库；blowfish 密钥是会话加密用，随机生成、永不落盘。
		plan.set(web.Name, "PMA_HOST", containerName(deployment, db))
		plan.set(web.Name, "PMA_PORT", "3306")
		if name := plan.get(db.Name, "MYSQL_DATABASE"); name != "" {
			plan.set(web.Name, "PMA_DB_NAME", name)
		}
		secret, err := randomSecret()
		if err != nil {
			return internalErr("生成 phpMyAdmin 会话密钥失败: " + err.Error())
		}
		plan.set(web.Name, "PMA_BLOWFISH_SECRET", secret)
	}
	return nil
}

// templateIDs 返回内置模板 id 的展示串（错误信息用）。
func templateIDs() string {
	ids := make([]string, 0, len(builtinTemplates))
	for _, tpl := range builtinTemplates {
		ids = append(ids, tpl.ID)
	}
	return strings.Join(ids, "/")
}

// randomSecret 生成 32 位十六进制随机口令（只含字母数字，天然满足 env 值约束）。
func randomSecret() (string, error) {
	return randomID(16)
}

// randomID 生成 2*n 位小写十六进制随机标识。
func randomID(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("读取随机源失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
