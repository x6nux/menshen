package core

import "fmt"

// OpError 是该给操作者看的失败：参数不对，或者没有权限。
//
// 管理操作被 TG 面板与 Mini App 两个界面共用；其余 error 都是内部故障，
// 界面上只回一句笼统的失败、细节进日志，不把库错误原样亮给人看。
type OpError struct {
	Denied bool // true = 没有权限（HTTP 403），否则是参数问题（400）
	Msg    string
}

func (e *OpError) Error() string { return e.Msg }

// Bad 构造一个参数类的 OpError。
func Bad(format string, a ...any) error {
	return &OpError{Msg: fmt.Sprintf(format, a...)}
}

// Denied 构造一个权限类的 OpError。
func Denied(msg string) error { return &OpError{Denied: true, Msg: msg} }
