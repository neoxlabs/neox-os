//go:build !linux

package confine

import "errors"

// 非 Linux 上 seccomp 不存在. 提供同名符号只是为了让代码在 macOS 上
// 编得过、测得了纯函数部分 —— **不是降级实现**.
// 任何真正的调用都会拿到明确错误, 而不是"假装成功".

var ErrUnotifyUnsupported = errors.New("seccomp user notification 只在 Linux 上可用")

type Decision int

const (
	DecisionAllow Decision = iota
	DecisionDeny
)

type Request struct {
	PID   uint32
	Call  string
	Path  string
	Axis  string
	Flags uint64
}

type Supervisor struct {
	OnEvent func(Request, Decision)
}

func InstallSelfFilter() (int, error)                       { return -1, ErrUnotifyUnsupported }
func NewSupervisor(int, func(Request) Decision) *Supervisor { return &Supervisor{} }
func (s *Supervisor) Serve() error                          { return ErrUnotifyUnsupported }
func (s *Supervisor) Close() error                          { return nil }
func SendFD(int, int) error                                 { return ErrUnotifyUnsupported }
func RecvFD(int) (int, error)                               { return -1, ErrUnotifyUnsupported }
