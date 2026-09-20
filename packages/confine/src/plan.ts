/**
 * plan — 把 Capability 翻译成内核可执行的约束规则.
 *
 *   这是"能力从问询变强制"的关键一步.
 *
 *   旧 (agent IDE): ctx.can('write', p) 返回 false → **指望进程自己不写**.
 *                   进程直接 import fs 就绕过去了. 检查形同虚设.
 *   新 (OS):        能力在 spawn 时翻译成 landlock ruleset + seccomp filter
 *                   + netns 规则, 由内核在 syscall 边界上执行. 进程调不调
 *                   can() 都一样, 它**做不到**没授权的事.
 *
 *   本文件是**纯函数**, 没有任何 syscall —— 所以能在 macOS 上测.
 *   施加规则是 launcher.ts 的事, 那部分 Linux-only.
 */

import type { Capability, ConfinementPlan, EnforcementRequirement } from '@neox-os/abi';

import { splitHostPort } from './scope.js';

export interface PlanOptions {
  /** 卷根 — 所有 read/write scope 都相对它解析 */
  volumeRoot: string;
  /**
   * 执行底座的实际路径列表.
   *   必须按运行平台提供实际存在的路径, 因为目录布局随平台变化:
   *   arm64 上没有 /lib64 (那是 x86-64 的布局), 而 landlock-run 对
   *   不存在的路径会直接报错. 纯函数不猜环境, 所以这里只收不查,
   *   由 resolveRuntimeFs() 负责生成.
   */
  runtimeFs?: string[];
  /** 必须可写的设备节点. 精简容器里可能没有 /dev/tty, 所以也按本机给 */
  runtimeDevices?: string[];
  memoryMaxBytes?: number;
  cpuWeight?: number;
  pidsMax?: number;
}

/**
 * 缺省是拒绝: 空能力集翻译出来是"什么都不能碰、完全断网、不能起子进程".
 * 注意它仍然 requires 全套机制 —— **越是什么都不给, 越需要内核真的能拦住**.
 */
export function planFor(caps: readonly Capability[], opts: PlanOptions): ConfinementPlan {
  const fsRules = new Map<string, Set<'read' | 'write'>>();
  const net: { host: string; port?: string }[] = [];
  let allowSubprocess = false;

  for (const cap of caps) {
    switch (cap.axis) {
      case 'read':
      case 'write': {
        const abs = resolveInVolume(opts.volumeRoot, cap.scope);
        const set = fsRules.get(abs) ?? new Set();
        set.add(cap.axis);
        /* Landlock 将 write 和 read 视为独立权限, 所以可写路径必须同时授予 read. */
        if (cap.axis === 'write') set.add('read');
        fsRules.set(abs, set);
        break;
      }
      case 'net': {
        const [host, port] = splitHostPort(cap.scope);
        net.push(port === undefined ? { host } : { host, port });
        break;
      }
      case 'proc':
        allowSubprocess = true;
        break;
      case 'secret':
        /* 凭据不走文件系统, 由 OS 通过 ABI 通道按次发放.
         * 这里刻意什么都不做 —— 凭据不该以"某个路径可读"的形式存在. */
        break;
    }
  }

  return {
    runtimeFs: opts.runtimeFs ?? [...DEFAULT_RUNTIME_FS],
    runtimeDevices: opts.runtimeDevices ?? [...DEFAULT_RUNTIME_DEVICES],
    fs: [...fsRules.entries()]
      .map(([path, access]) => ({ path, access: [...access].sort() as ('read' | 'write')[] }))
      .sort((a, b) => a.path.localeCompare(b.path)),
    net,
    allowSubprocess,
    cgroup: {
      /* 内存必须有缺省上限; 不写入 undefined 会让 memory.max 保持 max,
       * 等于完全没有施加限额.
       *
       * 额度跟"能不能起进程"绑在一起, 吃内存的从来不是 agent 自己:
       *   没有 proc  512MB. 它是个小程序, 读已经按 24KB 分片了
       *   有 proc    2GB. 一次 go build / tsc 单进程就能上 GB,
       *              卡在 512MB 会被 OOM killer 砍掉, 而报出来的是
       *              一句没头没尾的 "signal: killed" */
      memoryMaxBytes:
        opts.memoryMaxBytes ?? (allowSubprocess ? 2 * 1024 ** 3 : 512 * 1024 * 1024),
      cpuWeight: opts.cpuWeight,
      /* pids.max 是**资源上限**, 不是"禁不禁止起子进程"的开关.
       * 它数的是**任务(线程)**不是进程 —— 设成 1 会让任何多线程运行时
       * 连启动都做不到 (Go/Java/Node 全挂). 真机跑真 agent 时撞上过.
       *
       * **不再试图禁止起子进程**: landlock/netns/cgroup 全部随 fork+exec
       * 继承, 子进程跟父进程关在同一个笼子里 —— 起进程不构成逃逸.
       * 所以 proc 能力管的是配额宽窄: 4096 够跑 go test / npm ci,
       * 同时仍然拦得住 fork 炸弹. */
      pidsMax: opts.pidsMax ?? (allowSubprocess ? 4096 : 64),
    },
    requires: REQUIRED_FEATURES,
  };
}

/**
 * 需要满足哪些强制需求 —— 恒定全套, 跟能力集无关.
 *
 *   刻意不按能力裁剪: 没有出网授权时**更**需要 net-isolate, 因为断网要靠隔离,
 *   不能靠"白名单里没有"(那只是配置不是隔离). 所以这是常量, 不是函数.
 *
 *   **只列实际会施加的机制**. seccomp 尚未接入 buildLaunch, 因此不能把
 *   未执行的过滤列为启动要求; 否则系统会因未使用的机制拒绝启动.
 *   syscall-notify 要等 seccomp-unotify 真正接上再加进来.
 */
/**
 * 执行底座 — 任何进程都要能读到的路径, 否则 exec 都做不到.
 *
 *   这是**最小可执行集**, 不是方便集:
 *     /usr /bin /sbin /lib /lib64  可执行文件与动态库
 *     /etc                         解析器/时区/证书 (下面这条要认)
 *
 *   诚实的暴露面: 授 /etc 只读意味着进程能读 /etc/passwd (用户名列表).
 *   /etc/shadow 本身还有 DAC 保护 (root only), 但这仍是一处真实暴露.
 *   收窄它的正确做法是给进程一个最小 rootfs, 而不是继续加白名单 —— 待办.
 */
export const DEFAULT_RUNTIME_FS: readonly string[] = [
  '/usr', '/bin', '/sbin', '/lib', '/lib64', '/etc',
  /* /proc 是必须的: node/python/java 都要读它, 少了跑不起来.
   * 安全上可接受, 因为我们同时开了 PID namespace —— 进程在 /proc 里
   * **只看得到自己**, 看不到宿主或其他进程. 两者是配套的, 不能只留一个. */
  '/proc',
];

/**
 * 必须**可写**的设备节点, 同样恒定授予.
 *
 *   为什么不能并进 DEFAULT_RUNTIME_FS: 那些是只读的, 而 /dev/null
 *   必须能写 —— `> /dev/null` 是写操作.
 *
 *   缺少它时, 起 20 个后台进程会全部失败并报告
 *   `cannot open /dev/null: Permission denied`. shell 的作业控制、
 *   几乎每个构建脚本都要它.
 *
 *   为什么逐个列而不是整个 /dev: 整个 /dev 可写等于把裸磁盘 (/dev/vda)
 *   和物理内存 (/dev/mem) 一起给出去. 逐个授予后, /dev/null 可以写入,
 *   `head -c 1 /dev/vda` 仍然被拒.
 *
 *   这些节点只在允许执行命令后才会被访问; 文件工具模式不需要它们,
 *   而命令执行依赖它们完成 shell 作业控制和构建脚本.
 */
export const DEFAULT_RUNTIME_DEVICES: readonly string[] = [
  '/dev/null', '/dev/zero', '/dev/urandom', '/dev/random', '/dev/tty',
];

/**
 * 按本机实际情况筛出存在的底座路径.
 *   这是**唯一**需要碰文件系统的一步, 刻意从 planFor 里分出来 ——
 *   纯函数保持可测, 环境探测集中在一处.
 */
export function resolveRuntimeFs(exists: (p: string) => boolean): string[] {
  return DEFAULT_RUNTIME_FS.filter(exists);
}

const REQUIRED_FEATURES: EnforcementRequirement[] = [
  'fs-enforce',
  'resource-limit',
  'net-isolate',
];

/**
 * 把卷内路径解析成绝对路径.
 *   拒绝路径穿越, 不做 `..` 解析 —— 解析等于给自己开后门.
 *   连续斜杠不压缩 (UNC 会被压没, 是带过来的已知陷阱 D1).
 */
export function resolveInVolume(volumeRoot: string, scope: string): string {
  const root = stripTrailing(volumeRoot.replace(/\\/g, '/'));
  const rel = scope.replace(/\\/g, '/');
  if (rel.split('/').includes('..')) {
    throw new Error(`capability scope 含路径穿越, 拒绝: ${scope}`);
  }
  /* '*' 在 fs 轴上等于"整个卷" —— 跟 scopeMatches 的处理必须一致.
   * 裸 '*' 和 '/*' 都要认: 前者是能力集里最常见的写法. */
  if (rel === '*' || rel === '/*' || rel === '/' || rel === '') return root || '/';
  return `${root}${rel.startsWith('/') ? rel : `/${rel}`}`;
}

function stripTrailing(p: string): string {
  return p.length > 1 && p.endsWith('/') ? p.slice(0, -1) : p;
}
