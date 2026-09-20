//go:build linux

package confine

// ── 真机状态: 可用性不稳, **没有接进主路径** ──
//
// Lima VM (6.8 内核, arm64) 的结果:
//
//	unotify-demo /agentwork /bin/cat /agentwork/site/notes.md
//	  → intercepted=1, 已拦截, 裁决 0.125us
//	unotify-demo /etc /bin/cat /etc/hostname
//	  → intercepted=0, **一次都未拦截**, 进程正常跑完
//
// 两次是同一份代码、同一个二进制. strace 确认两次都是 31 次 openat,
// stub 也确认了"过滤器已装、监听 fd 已交出" —— 也就是说
// **filter 在, 通知却没到用户态, 内核放行了**. 那个方向是 fail-open.
//
// 原因还没查清. 但结论是明确的:
//
//	· 它**不在 wired 里**, 所以探测如实报成"探到但没接入",
//	  ReqSyscallNotify 不算被满足, 系统不依赖它
//	· 主路径的文件强制靠 landlock —— 那个带对照组验过真拦
//	· **不要在查清之前把它塞进 wired**. 一旦塞进去, 上面那种
//	  "filter 在但不拦"的情况就变成一个静默的 fail-open 洞
//
// 这正是"只支持不接入不算数"那条规矩存在的理由: 它这次挡住了一个洞.

// seccomp user notification —— 把人类决策接到系统调用上.
//
// 这是 Neox OS 最想要的那个能力, 也是别人没做的那件事:
//
//	现在所有 agent 产品的审批都发生在**工具调用边界** —— 我们包装过的
//	write_file / bash 才会问你. 但 agent 一旦跑起 npm install、一个
//	python 脚本、一个编译产物, 里面发生的一切都在审批视野之外.
//
//	下沉到 syscall 之后: 内核**冻结**那次调用, 把决定权交给 OS,
//	OS 去问人 (可能在两百公里外、手机锁屏), 半小时后回来, 进程继续.
//
// 为什么传统系统做不了、我们能做:
//
//	传统负载被冻结几秒就是故障; agent 负载被冻结半小时无所谓 ——
//	它本来就在等模型、等人、等 IO.
//	而且模型一次往返 500ms–5s, 拦一次 syscall 才 10–100µs,
//	差 4–5 个数量级, 仲裁开销在 agent 的时间尺度上根本不存在.
//
// 边界 (必须写清楚, 否则会以为它能管一切):
//
//   - **只拦有语义的调用**: openat/connect/execve/unlinkat 这类.
//     绝不拦 read/write/stat 这种热路径 —— 那会把进程拖垮.
//   - **路径类判定优先交给 Landlock**. 内核按 inode 判, 没有竞态.
//     unotify 只处理"无法提前静态描述"的决策.
//   - 放行时**不用 FLAG_CONTINUE 处理指针参数**: 我们校验过的路径字符串
//     可能在放行后被另一个线程改掉 (经典 TOCTOU). 见 respondAllow 的注释.

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ── seccomp 结构体 (linux/seccomp.h) ────────────────────────

type seccompData struct {
	NR                 int32
	Arch               uint32
	InstructionPointer uint64
	Args               [6]uint64
}

type seccompNotif struct {
	ID    uint64
	PID   uint32
	Flags uint32
	Data  seccompData
}

type seccompNotifResp struct {
	ID    uint64
	Val   int64
	Error int32
	Flags uint32
}

const (
	seccompSetModeFilter         = 1
	seccompFilterFlagNewListener = 1 << 3
	userNotifFlagContinue        = 1

	ioWrite = 1
	ioRead  = 2
)

// ioc 复刻内核的 _IOC 宏. 用运行时 sizeof 而不是硬编码大小 ——
// 结构体填充在不同架构上可能不同, 硬编码就是等着踩.
func ioc(dir, typ, nr, size uintptr) uintptr {
	return (dir << 30) | (size << 16) | (typ << 8) | nr
}

var (
	ioctlNotifRecv    = ioc(ioRead|ioWrite, '!', 0, unsafe.Sizeof(seccompNotif{}))
	ioctlNotifSend    = ioc(ioRead|ioWrite, '!', 1, unsafe.Sizeof(seccompNotifResp{}))
	ioctlNotifIDValid = ioc(ioWrite, '!', 2, unsafe.Sizeof(uint64(0)))
)

// ── cBPF 过滤器 ─────────────────────────────────────────────

type sockFilter struct {
	Code uint16
	JT   uint8
	JF   uint8
	K    uint32
}

type sockFprog struct {
	Len    uint16
	_      [6]byte
	Filter *sockFilter
}

const (
	bpfLD  = 0x00
	bpfW   = 0x00
	bpfABS = 0x20
	bpfJMP = 0x05
	bpfJEQ = 0x10
	bpfK   = 0x00
	bpfRET = 0x06

	retAllow     = 0x7fff0000
	retUserNotif = 0x7fc00000
	retKillProc  = 0x80000000
)

// archAndSyscalls 本机的 AUDIT_ARCH 值和要拦的调用号.
//
// 校验 arch 不是形式主义: 同一台机器上 32 位进程的调用号完全不同,
// 不校验就会把 open 的号当成别的调用放行 —— 这是 seccomp 最经典的坑.
func archAndSyscalls() (arch uint32, openat uint32, err error) {
	switch runtime.GOARCH {
	case "arm64":
		return 0xc00000b7, 56, nil // AUDIT_ARCH_AARCH64, __NR_openat
	case "amd64":
		return 0xc000003e, 257, nil // AUDIT_ARCH_X86_64, __NR_openat
	default:
		return 0, 0, fmt.Errorf("unotify 暂不支持 %s", runtime.GOARCH)
	}
}

// buildFilter 生成过滤器: 只把 openat 交给用户态裁决, 其余一律放行.
//
// 刻意只拦一个调用 —— 拦得越多, 被拖垮的风险越大.
// 要扩到 connect/execve/unlinkat 时在这里加, 但每加一个都要量开销.
func buildFilter() ([]sockFilter, error) {
	arch, openat, err := archAndSyscalls()
	if err != nil {
		return nil, err
	}
	off := func(f string) uint32 {
		switch f {
		case "nr":
			return 0
		case "arch":
			return 4
		}
		return 0
	}
	return []sockFilter{
		// arch 不匹配 → 直接杀进程. 放行是不可接受的 (调用号语义全变了)
		{bpfLD | bpfW | bpfABS, 0, 0, off("arch")},
		{bpfJMP | bpfJEQ | bpfK, 1, 0, arch},
		{bpfRET | bpfK, 0, 0, retKillProc},
		// nr == openat → 交给用户态
		{bpfLD | bpfW | bpfABS, 0, 0, off("nr")},
		{bpfJMP | bpfJEQ | bpfK, 1, 0, openat},
		{bpfRET | bpfK, 0, 0, retAllow},
		{bpfRET | bpfK, 0, 0, retUserNotif},
	}, nil
}

// InstallSelfFilter 给**自己**装过滤器并返回监听 fd.
//
// 由 stub 进程调用: 自缚 → 把 fd 交给监督者 → exec 目标程序.
// 过滤器跨 execve 继承, 所以目标程序和它 fork 出的一切都受管.
func InstallSelfFilter() (int, error) {
	// no_new_privs 是装 seccomp 的前提, 也顺带堵死 setuid 提权
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return -1, fmt.Errorf("no_new_privs: %w", err)
	}
	filter, err := buildFilter()
	if err != nil {
		return -1, err
	}
	prog := sockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	fd, _, errno := unix.Syscall(unix.SYS_SECCOMP,
		seccompSetModeFilter, seccompFilterFlagNewListener,
		uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return -1, fmt.Errorf("seccomp(SET_MODE_FILTER): %w", errno)
	}
	runtime.KeepAlive(filter)
	return int(fd), nil
}

// ── 监督者 ──────────────────────────────────────────────────

// Decision 监督者对一次被冻结的调用的裁决
type Decision int

const (
	// DecisionAllow 放行
	DecisionAllow Decision = iota
	// DecisionDeny 拒绝, 进程收到 EACCES
	DecisionDeny
)

// Request 一次被冻结的系统调用
type Request struct {
	// PID 是**目标进程在监督者所在 pid namespace 里的号**
	PID  uint32
	Call string
	Path string
	// Axis 由调用标志推出的能力轴 (read/write).
	//
	// 必须真的解析 flags: 早先这里写死 "read", 于是一次
	// open(O_WRONLY) 会被当成读来裁决 —— 用读权限放行了一次写.
	// 这是**判错方向**, 不是判得粗.
	Axis string
	// Flags 原始 flags, 供上层做更细的判断 (如 O_CREAT 单独提示)
	Flags uint64
}

// openat flags —— arm64 与 x86_64 取值相同 (asm-generic)
const (
	oWRONLY = 0o1
	oRDWR   = 0o2
	oCREAT  = 0o100
	oTRUNC  = 0o1000
	oAPPEND = 0o2000
)

// axisOfOpenFlags 由 open 标志推能力轴.
//
// 任何可能改变文件内容的标志都算写 —— 包括 O_CREAT/O_TRUNC/O_APPEND.
// 只有纯 O_RDONLY 才算读. 宁可判严, 不可判松.
func axisOfOpenFlags(flags uint64) string {
	if flags&(oWRONLY|oRDWR|oCREAT|oTRUNC|oAPPEND) != 0 {
		return "write"
	}
	return "read"
}

// Supervisor 从监听 fd 上收通知, 交给 decide 裁决, 再把结果送回内核.
type Supervisor struct {
	fd     int
	decide func(Request) Decision
	// OnEvent 可选: 每次裁决后回调, 用于写审计日志
	OnEvent func(Request, Decision)

	stopped atomic.Bool
}

func NewSupervisor(listenerFD int, decide func(Request) Decision) *Supervisor {
	return &Supervisor{fd: listenerFD, decide: decide}
}

var ErrListenerClosed = errors.New("seccomp listener closed")

// Serve 主循环. 目标进程退出后 RECV 会返回 ENOENT/EPIPE, 此时返回.
func (s *Supervisor) Serve() error {
	for {
		if s.stopped.Load() {
			return nil
		}
		// **先 poll 再 RECV**.
		// 直接阻塞在 ioctl 上是关不掉的: 关 fd 不会把阻塞中的 ioctl 唤醒,
		// 监督者会永远挂在那里 (真机第一次跑就撞上, 进程超时才被杀).
		// poll 带超时 → 每 200ms 有机会检查停止标志.
		pfd := []unix.PollFd{{Fd: int32(s.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(pfd, 200)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return nil // fd 已关
		}
		if n == 0 {
			continue // 超时, 回去检查停止标志
		}
		if pfd[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return nil // 目标没了
		}

		var notif seccompNotif
		if err := ioctlPtr(s.fd, ioctlNotifRecv, unsafe.Pointer(&notif)); err != nil {
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EPIPE) ||
				errors.Is(err, unix.EBADF) || errors.Is(err, unix.ECANCELED) {
				return nil // 目标没了, 正常收摊
			}
			return fmt.Errorf("NOTIF_RECV: %w", err)
		}

		// openat(dirfd, pathname, flags, mode)
		flags := notif.Data.Args[2]
		req := Request{
			PID: notif.PID, Call: "openat",
			Flags: flags, Axis: axisOfOpenFlags(flags),
		}
		// 路径是**目标地址空间里的指针**, 必须从 /proc/<pid>/mem 读出来
		if p, err := readCString(int(notif.PID), notif.Data.Args[1]); err == nil {
			req.Path = p
		}

		// **读完内存后必须再确认这次通知仍然有效**.
		// 目标可能已经死了, 而它的 pid 可能已经被别的进程复用 ——
		// 那样我们读到的就是**另一个进程**的内存. 这是 unotify 的头号陷阱.
		if err := s.idValid(notif.ID); err != nil {
			continue // 已失效, 不回复 (内核会自己清理)
		}

		d := s.decide(req)
		if s.OnEvent != nil {
			s.OnEvent(req, d)
		}

		resp := seccompNotifResp{ID: notif.ID}
		if d == DecisionAllow {
			// 放行. 用 FLAG_CONTINUE 让内核照常执行这次调用.
			//
			// 已知边界: 对**指针参数**而言这是有竞态的 —— 我们校验的是
			// 刚才读到的那个路径字符串, 放行后目标的另一个线程可以把它改掉,
			// 内核执行的会是改后的路径.
			// 因此: 路径白名单必须由 Landlock 在内核里按 inode 兜底,
			// unotify 只负责"问人"这一层. 两者是叠加不是二选一.
			resp.Flags = userNotifFlagContinue
		} else {
			resp.Error = -int32(unix.EACCES)
		}
		if err := ioctlPtr(s.fd, ioctlNotifSend, unsafe.Pointer(&resp)); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue // 目标在裁决期间消失
			}
			return fmt.Errorf("NOTIF_SEND: %w", err)
		}
	}
}

func (s *Supervisor) idValid(id uint64) error {
	return ioctlPtr(s.fd, ioctlNotifIDValid, unsafe.Pointer(&id))
}

// Close 停止 Serve 并关闭监听 fd.
// 先置停止标志再关 fd —— 反过来会让 poll 拿到一个已关闭的 fd.
func (s *Supervisor) Close() error {
	s.stopped.Store(true)
	return unix.Close(s.fd)
}

func ioctlPtr(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// readCString 从目标进程的地址空间读一个 C 字符串.
//
// 走 /proc/<pid>/mem 而不是 process_vm_readv, 因为前者不需要额外能力,
// 且在目标已死时会干净地报错而不是读到垃圾.
func readCString(pid int, addr uint64) (string, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/mem", pid))
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, 0, 256)
	chunk := make([]byte, 64)
	off := int64(addr)
	for len(buf) < 4096 { // PATH_MAX 兜底, 不给恶意超长路径撑爆内存的机会
		n, err := f.ReadAt(chunk, off)
		if n == 0 {
			if err != nil {
				return "", err
			}
			break
		}
		for i := 0; i < n; i++ {
			if chunk[i] == 0 {
				return string(append(buf, chunk[:i]...)), nil
			}
		}
		buf = append(buf, chunk[:n]...)
		off += int64(n)
	}
	return string(buf), nil
}

// ── fd 传递 (stub → 监督者) ─────────────────────────────────

// SendFD 通过 unix socket 把监听 fd 交给监督者 (SCM_RIGHTS)
func SendFD(sock, fd int) error {
	rights := unix.UnixRights(fd)
	return unix.Sendmsg(sock, []byte{0}, rights, nil, 0)
}

// RecvFD 收一个 fd
func RecvFD(sock int) (int, error) {
	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unix.Recvmsg(sock, buf, oob, 0)
	if err != nil {
		return -1, err
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		return -1, fmt.Errorf("没收到 fd: %v", err)
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) == 0 {
		return -1, fmt.Errorf("解析 fd 失败: %v", err)
	}
	return fds[0], nil
}
