// Package apop 实现 app-deploy 的方法面：模板白名单、目标路径约束与结构化结果。
// 本包只做逻辑校验与执行面调用，不拉取应用、不部署应用、不下载任何模板（骨架口径）。
package apop

import "fmt"

// allowedTemplates 是允许的一键部署模板白名单（提案 §2.1 批3 app-deploy）。
var allowedTemplates = map[string]bool{
	"wordpress": true,
	"laravel":   true,
	"nextjs":    true,
}

// FaultKind 标识一次故障注入的类别。
type FaultKind string

const (
	FaultNone    FaultKind = ""
	FaultKill    FaultKind = "kill"
	FaultTimeout FaultKind = "timeout"
	FaultDisk    FaultKind = "disk"
	FaultCgroup  FaultKind = "cgroup"
)

// FaultError 是执行面注入的故障错误，Call 将其映射为协议错误码。
type FaultError struct {
	Kind    FaultKind
	Message string
}

// Error 实现 error 接口，输出 "<类别>: <说明>"。
func (e *FaultError) Error() string { return fmt.Sprintf("%s: %s", e.Kind, e.Message) }

// NewFaultError 构造给定类别的故障错误。
func NewFaultError(kind FaultKind, message string) *FaultError {
	return &FaultError{Kind: kind, Message: message}
}

// Executor 是部署操作的执行面；骨架实现为 noop，测试可注入故障执行器。
type Executor interface {
	Templates() ([]Template, error)
	Create(template, target string) (bool, error)
}

// Template 是 app-deploy.templates 返回的一项模板元信息。
type Template struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
}

// templatesParams / createParams 是两个方法的显式入参形状。
type templatesParams struct{}

type createParams struct {
	Template string `json:"template"`
	Target   string `json:"target"`
}

// TemplatesResult 是 app-deploy.templates 的结构化结果。
type TemplatesResult struct {
	Templates []Template `json:"templates"`
	Count     int        `json:"count"`
}

// CreateResult 是 app-deploy.create 的结构化结果；applied=false 表示骨架口径未真部署。
type CreateResult struct {
	Template string `json:"template"`
	Target   string `json:"target"`
	Applied  bool   `json:"applied"`
}
