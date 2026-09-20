//go:build !linux

package confine

import (
	"errors"
	"fmt"
	"net"
)

// 非 Linux 上没有 netns —— 明确报"做不到", 不给一个假的实现.
//
// 这跟 fail-closed 是一回事: 拿不到强制就拒绝, 而不是返回一个
// 什么都不拦的对象让调用方以为配好了.

const ProxyPort = 3128

type NetNamespace struct {
	Name      string
	HostAddr  string
	GuestAddr string
}

func (n *NetNamespace) ProxyAddr() string {
	return net.JoinHostPort(n.HostAddr, fmt.Sprint(ProxyPort))
}

var errNoNetns = errors.New("这个平台没有网络命名空间, 给不了受控出网")

func SetupNetNamespace(pid string, ipBin string) (*NetNamespace, error) {
	return nil, errNoNetns
}

// 非 Linux 上没有命名空间可建 —— 调用方会退回匿名断网 ns.
func SetupLoopbackOnlyNamespace(pid string, ipBin string) (*NetNamespace, error) {
	return nil, errNoNetns
}

func (n *NetNamespace) Teardown(ipBin string) {}

func SweepOrphanNetNamespaces(ipBin string) int { return 0 }
