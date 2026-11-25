package apop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// builtinTemplates 是内置模板目录（本地常量，不联网、不下载）。
//
// 镜像与端口只在这里出现：app-deploy.deploy 只接受模板 id，不接受用户自定义镜像，
// 从根上封死 `docker run evil/image` 这条路。顺序固定，保证 templates 结果字节稳定。
var builtinTemplates = []Template{
	{
		ID:                "nginx-static",
		Name:              "Nginx 静态站点",
		Description:       "nginx:alpine 托管静态目录；把站点文件放进挂载目录即可通过 access_url 访问",
		Image:             "nginx:alpine",
		EstimatedMemoryMB: 24,
		Ports: []PortSpec{{
			Service:       "web",
			ContainerPort: 80,
			DefaultHost:   18090,
			Protocol:      "tcp",
			Label:         "HTTP",
			AccessPrefix:  "http://",
		}},
		Volumes: []VolumeSpec{{
			Service:       "web",
			Name:          "html",
			Kind:          VolumeBind,
			ContainerPath: "/usr/share/nginx/html",
			Description:   "站点根目录（宿主机目录，可直接放静态文件）",
		}},
		Env:      []EnvField{},
		Services: []Service{{Name: "web", Image: "nginx:alpine", MemoryMB: 128, Primary: true}},
	},
	{
		ID:                "wordpress",
		Name:              "WordPress 站点",
		Description:       "wordpress:6 + mysql:8 双容器；数据库仅在内网可达，首次访问进入安装向导",
		Image:             "wordpress:6",
		EstimatedMemoryMB: 768,
		Ports: []PortSpec{{
			Service:       "web",
			ContainerPort: 80,
			DefaultHost:   18091,
			Protocol:      "tcp",
			Label:         "HTTP",
			AccessPrefix:  "http://",
		}},
		Volumes: []VolumeSpec{
			{
				Service:       "web",
				Name:          "html",
				Kind:          VolumeNamed,
				ContainerPath: "/var/www/html",
				Description:   "WordPress 程序与上传文件（docker 命名卷）",
			},
			{
				Service:       "db",
				Name:          "data",
				Kind:          VolumeNamed,
				ContainerPath: "/var/lib/mysql",
				Description:   "MySQL 数据目录（docker 命名卷）",
			},
		},
		Env: []EnvField{
			{
				Key:      "MYSQL_ROOT_PASSWORD",
				Label:    "数据库 root 口令",
				Required: false,
				Default:  "",
				Help:     "留空则由插件生成随机口令（不返回、不落盘）",
				Service:  "db",
				Secret:   true,
			},
			{
				Key:      "MYSQL_DATABASE",
				Label:    "数据库名",
				Required: false,
				Default:  "wordpress",
				Help:     "WordPress 使用的数据库名",
				Service:  "db",
			},
			{
				Key:      "MYSQL_USER",
				Label:    "数据库用户名",
				Required: false,
				Default:  "wordpress",
				Help:     "WordPress 连接数据库使用的账号",
				Service:  "db",
			},
			{
				Key:      "MYSQL_PASSWORD",
				Label:    "数据库用户口令",
				Required: false,
				Default:  "",
				Help:     "留空则由插件生成随机口令，并自动同步到 WordPress 侧",
				Service:  "db",
				Secret:   true,
			},
		},
		Services: []Service{
			{Name: "web", Image: "wordpress:6", MemoryMB: 512, Primary: true},
			{Name: "db", Image: "mysql:8", MemoryMB: 512},
		},
	},
	{
		ID:                "redis",
		Name:              "Redis 缓存",
		Description:       "redis:7-alpine + 持久卷；开启 appendonly，重启不丢数据",
		Image:             "redis:7-alpine",
		EstimatedMemoryMB: 32,
		Ports: []PortSpec{{
			Service:       "server",
			ContainerPort: 6379,
			DefaultHost:   16379,
			Protocol:      "tcp",
			Label:         "Redis",
			AccessPrefix:  "redis://",
		}},
		Volumes: []VolumeSpec{{
			Service:       "server",
			Name:          "data",
			Kind:          VolumeNamed,
			ContainerPath: "/data",
			Description:   "AOF 持久化目录（docker 命名卷）",
		}},
		Env: []EnvField{},
		Services: []Service{{
			Name:     "server",
			Image:    "redis:7-alpine",
			MemoryMB: 256,
			Cmd:      []string{"redis-server", "--appendonly", "yes"},
			Primary:  true,
		}},
	},
	{
		ID:                "postgres",
		Name:              "PostgreSQL 数据库",
		Description:       "postgres:16-alpine + 持久卷；必须提供 POSTGRES_PASSWORD",
		Image:             "postgres:16-alpine",
		EstimatedMemoryMB: 128,
		Ports: []PortSpec{{
			Service:       "db",
			ContainerPort: 5432,
			DefaultHost:   15432,
			Protocol:      "tcp",
			Label:         "PostgreSQL",
			AccessPrefix:  "postgres://",
		}},
		Volumes: []VolumeSpec{{
			Service:       "db",
			Name:          "data",
			Kind:          VolumeNamed,
			ContainerPath: "/var/lib/postgresql/data",
			Description:   "数据库数据目录（docker 命名卷）",
		}},
		Env: []EnvField{
			{
				Key:      "POSTGRES_PASSWORD",
				Label:    "数据库口令",
				Required: true,
				Default:  "",
				Help:     "postgres 镜像强制要求；不接受空口令",
				Service:  "db",
				Secret:   true,
			},
			{
				Key:      "POSTGRES_USER",
				Label:    "数据库用户",
				Required: false,
				Default:  "postgres",
				Help:     "超级用户账号",
				Service:  "db",
			},
			{
				Key:      "POSTGRES_DB",
				Label:    "数据库名",
				Required: false,
				Default:  "appdb",
				Help:     "初始化时创建的数据库",
				Service:  "db",
			},
		},
		Services: []Service{{Name: "db", Image: "postgres:16-alpine", MemoryMB: 512, Primary: true}},
	},
	{
		ID:                "node-app",
		Name:              "Node.js 应用",
		Description:       "node:20-alpine 跑一个 Node.js 服务；挂载目录里自带可访问的示例 server.js，换成自己的入口即可",
		Image:             "node:20-alpine",
		EstimatedMemoryMB: 96,
		Ports: []PortSpec{{
			Service:       "web",
			ContainerPort: 3000,
			DefaultHost:   18092,
			Protocol:      "tcp",
			Label:         "HTTP",
			AccessPrefix:  "http://",
		}},
		Volumes: []VolumeSpec{{
			Service:       "web",
			Name:          "app",
			Kind:          VolumeBind,
			ContainerPath: "/app",
			Description:   "应用目录（宿主机目录，放 server.js 与代码；自带示例入口）",
		}},
		Env: []EnvField{},
		Services: []Service{{
			Name:     "web",
			Image:    "node:20-alpine",
			MemoryMB: 256,
			Cmd:      []string{"node", "/app/server.js"},
			Primary:  true,
		}},
	},
	{
		ID:                "python-app",
		Name:              "Python Web 应用",
		Description:       "python:3.12-alpine 跑一个 Python Web 服务；自带可访问的示例 app.py，可替换为 Flask / FastAPI 入口",
		Image:             "python:3.12-alpine",
		EstimatedMemoryMB: 96,
		Ports: []PortSpec{{
			Service:       "web",
			ContainerPort: 8000,
			DefaultHost:   18093,
			Protocol:      "tcp",
			Label:         "HTTP",
			AccessPrefix:  "http://",
		}},
		Volumes: []VolumeSpec{{
			Service:       "web",
			Name:          "app",
			Kind:          VolumeBind,
			ContainerPath: "/app",
			Description:   "应用目录（宿主机目录，放 app.py 与代码；自带示例入口）",
		}},
		Env: []EnvField{},
		Services: []Service{{
			Name:     "web",
			Image:    "python:3.12-alpine",
			MemoryMB: 256,
			Cmd:      []string{"python", "/app/app.py"},
			Primary:  true,
		}},
	},
	{
		ID:                "php-site",
		Name:              "PHP 站点（Apache）",
		Description:       "php:8.3-apache 直接托管 PHP 站点；挂载目录自带可访问的示例 index.php，把站点文件放进去即可",
		Image:             "php:8.3-apache",
		EstimatedMemoryMB: 128,
		Ports: []PortSpec{{
			Service:       "web",
			ContainerPort: 80,
			DefaultHost:   18094,
			Protocol:      "tcp",
			Label:         "HTTP",
			AccessPrefix:  "http://",
		}},
		Volumes: []VolumeSpec{{
			Service:       "web",
			Name:          "html",
			Kind:          VolumeBind,
			ContainerPath: "/var/www/html",
			Description:   "站点根目录（宿主机目录；以 public/ 为根的框架需自行调整入口）",
		}},
		Env:      []EnvField{},
		Services: []Service{{Name: "web", Image: "php:8.3-apache", MemoryMB: 256, Primary: true}},
	},
	{
		ID:                "mysql-phpmyadmin",
		Name:              "MySQL + phpMyAdmin",
		Description:       "mysql:8 + phpmyadmin:5 双容器；网页端管库，数据库仅在内网可达，root 口令必须自己填写（插件不保存口令）",
		Image:             "phpmyadmin:5",
		EstimatedMemoryMB: 796,
		Ports: []PortSpec{
			{
				Service:       "web",
				ContainerPort: 80,
				DefaultHost:   18095,
				Protocol:      "tcp",
				Label:         "phpMyAdmin",
				AccessPrefix:  "http://",
			},
			{
				Service:       "db",
				ContainerPort: 3306,
				DefaultHost:   13306,
				Protocol:      "tcp",
				Label:         "MySQL",
				AccessPrefix:  "mysql://",
			},
		},
		Volumes: []VolumeSpec{{
			Service:       "db",
			Name:          "data",
			Kind:          VolumeNamed,
			ContainerPath: "/var/lib/mysql",
			Description:   "MySQL 数据目录（docker 命名卷）",
		}},
		Env: []EnvField{
			{
				Key:      "MYSQL_ROOT_PASSWORD",
				Label:    "数据库 root 口令",
				Required: true,
				Default:  "",
				Help:     "phpMyAdmin 登录用的 root 口令；插件不保存口令，忘记只能重建",
				Service:  "db",
				Secret:   true,
			},
			{
				Key:      "MYSQL_DATABASE",
				Label:    "数据库名",
				Required: false,
				Default:  "appdb",
				Help:     "初始化时创建的数据库",
				Service:  "db",
			},
		},
		Services: []Service{
			{Name: "db", Image: "mysql:8", MemoryMB: 512},
			{Name: "web", Image: "phpmyadmin:5", MemoryMB: 256, Primary: true},
		},
	},
	{
		ID:                "uptime-kuma",
		Name:              "站点监控（Uptime Kuma）",
		Description:       "louislam/uptime-kuma 开箱即用：首次访问创建管理员账号，监控数据存 docker 命名卷",
		Image:             "louislam/uptime-kuma:1",
		EstimatedMemoryMB: 192,
		Ports: []PortSpec{{
			Service:       "web",
			ContainerPort: 3001,
			DefaultHost:   18096,
			Protocol:      "tcp",
			Label:         "HTTP",
			AccessPrefix:  "http://",
		}},
		Volumes: []VolumeSpec{{
			Service:       "web",
			Name:          "data",
			Kind:          VolumeNamed,
			ContainerPath: "/app/data",
			Description:   "监控数据与账号（docker 命名卷，删卷即清空）",
		}},
		Env:      []EnvField{},
		Services: []Service{{Name: "web", Image: "louislam/uptime-kuma:1", MemoryMB: 512, Primary: true}},
	},
}

// templateIndex 是 id → 模板的只读索引，供 deploy 做白名单查找。
var templateIndex = func() map[string]*Template {
	index := make(map[string]*Template, len(builtinTemplates))
	for i := range builtinTemplates {
		index[builtinTemplates[i].ID] = &builtinTemplates[i]
	}
	return index
}()

// LookupTemplate 按 id 查内置模板；不在目录内返回 nil（调用方转 E_INVALID）。
func LookupTemplate(id string) *Template {
	return templateIndex[id]
}

// Templates 返回内置模板目录的副本（防止调用方改到全局常量）。
//
// 浅拷贝只隔离了切片头：Ports / Volumes / Env / Services 仍与全局常量共享底层数组，
// 所以要改写其中元素的调用方必须再走 cloneTemplate（见 templatesWithPortSuggestions）。
func Templates() []Template {
	out := make([]Template, len(builtinTemplates))
	copy(out, builtinTemplates)
	return out
}

// cloneSlice 深拷贝一个切片，并保持「nil 仍是 nil、空仍是非 nil 空」——
// 后者直接决定 JSON 是 [] 还是 null（协议要求 ports/volumes/env 是数组）。
func cloneSlice[T any](src []T) []T {
	if src == nil {
		return nil
	}
	out := make([]T, len(src))
	copy(out, src)
	return out
}

// cloneTemplate 深拷贝一个模板，让调用方可以安全改写端口/环境变量等元素。
func cloneTemplate(tpl Template) Template {
	out := tpl
	out.Ports = cloneSlice(tpl.Ports)
	out.Volumes = cloneSlice(tpl.Volumes)
	out.Env = cloneSlice(tpl.Env)
	out.Services = cloneSlice(tpl.Services)
	for i, svc := range tpl.Services {
		out.Services[i].Cmd = cloneSlice(svc.Cmd)
	}
	return out
}

// templatesWithPortSuggestions 返回模板目录的深拷贝，并为每条端口填入「此刻可用」的
// 建议宿主端口（原则②自动发现：不让人自己去查端口有没有被占）。
//
// 占用判断与 deploy 的解析规则同源：运行中容器已发布的端口算占用（docker ps 只读快照），
// 再叠加真实 127.0.0.1 绑定探测。docker 不可用时降级为「只按本机探测」，模板目录照常返回
// —— 这一类模板读取不能因为 docker 没装就打不开。
func (r *Runtime) templatesWithPortSuggestions() []Template {
	templates := Templates()
	busy := map[int]string{}
	if states, err := r.exec.ListContainers(); err == nil {
		busy = busyFromStates(states)
	}
	for i := range templates {
		templates[i] = cloneTemplate(templates[i])
		used := make(map[int]bool, len(templates[i].Ports))
		for j := range templates[i].Ports {
			picked, perr := pickPort(templates[i].Ports[j].DefaultHost, used, busy, r.opts.PortProbe)
			if perr != nil {
				continue
			}
			templates[i].Ports[j].SuggestedHostPort = picked
			used[picked] = true
		}
	}
	return templates
}

// callTemplates 处理 app-deploy.templates：返回内置模板目录 + 现算的可用端口建议。
// 不联网、不拉镜像；只读 docker ps 快照（失败即忽略）与本机端口探测。
func (r *Runtime) callTemplates(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	if len(raw) > 0 && string(raw) != "null" {
		var p templatesParams
		if perr := decodeParams(raw, &p); perr != nil {
			return nil, perr
		}
	}
	templates := r.templatesWithPortSuggestions()
	return encode(TemplatesResult{Templates: templates, Count: len(templates)})
}
