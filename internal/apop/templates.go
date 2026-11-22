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
func Templates() []Template {
	out := make([]Template, len(builtinTemplates))
	copy(out, builtinTemplates)
	return out
}

// callTemplates 处理 app-deploy.templates：返回内置模板目录，不联网、不访问 docker。
func (r *Runtime) callTemplates(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	if len(raw) > 0 && string(raw) != "null" {
		var p templatesParams
		if perr := decodeParams(raw, &p); perr != nil {
			return nil, perr
		}
	}
	templates := Templates()
	return encode(TemplatesResult{Templates: templates, Count: len(templates)})
}
