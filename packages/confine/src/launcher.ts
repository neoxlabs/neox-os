/**
 * launcher — 把 ConfinementPlan 变成真正的启动命令行.
 *
 *   结构 (由外到内, 每层收紧一点):
 *
 *     sh -c 'echo $$ > cgroup.procs; exec ..'  ← 限额: 自己加入 cgroup 再 exec
 *       └─ [ip netns exec NS]                 ← 出网: 只在有授权时才有这一层
 *            └─ unshare --mount --pid --fork [--net]  ← 隔离
 *                 └─ landlock-run --ro X --rw Y --    ← 文件系统白名单
 *                      └─ 用户程序             ← 它继承了以上全部约束
 *
 *   关键性质: landlock ruleset 跨 execve 继承, 所以用户程序**再 fork 出来的
 *   任何子进程也逃不掉**. 这是"约束"跟"配置"的区别.
 *
 *   本文件仍是纯函数 —— 只拼命令行, 不执行. 于是能在 macOS 上写测试锁住
 *   命令行的形状, 而不必真有个 Linux 内核.
 */

import type { ConfinementPlan } from '@neox-os/abi';

export interface LaunchPlan {
  argv: string[];
  /** 剥干净的环境变量. 宿主环境不继承 —— 继承等于把凭据漏进沙盒 */
  env: Record<string, string>;
}

export interface LauncherPaths {
  unshare: string;
  /** POSIX shell —— 只用来把自己放进 cgroup 然后 exec, 不跑任何用户输入 */
  sh: string;
  landlockRun: string;
  /** cgroup v2 挂载点 */
  cgroupRoot: string;
  /** iproute2. 只在有出网授权时才用到 */
  ip: string;
}

export const DEFAULT_PATHS: LauncherPaths = {
  unshare: '/usr/bin/unshare',
  sh: '/bin/sh',
  landlockRun: '/usr/local/bin/landlock-run',
  cgroupRoot: '/sys/fs/cgroup',
  ip: '/sbin/ip',
};

export function buildLaunch(
  plan: ConfinementPlan,
  body: { argv: string[]; cwd: string; env?: Record<string, string> },
  ctx: {
    pid: string;
    cgroupSlice: string;
    abiSocket: string;
    abiToken?: string;
    /** 出网用的具名网络命名空间. 空 = 完全断网 */
    netNS?: string;
    /** OS 侧代理地址. 只是"往哪走"的提示, 强制来自拓扑 */
    proxyAddr?: string;
  },
  paths: LauncherPaths = DEFAULT_PATHS,
): LaunchPlan {
  if (body.argv.length === 0) throw new Error('exec 进程必须给出 argv');
  /* 授了出网却没人把网配出来 —— **拒绝启动**, 不是警告.
   *
   * 这个洞真实存在过: 能力模型里有 net 轴、planFor 老老实实翻译成
   * netRule、plan JSON 里也躺着, 而 launcher 从头到尾没读过它一眼.
   * 授权 {net:...} 什么都不会发生, 而且一声不吭.
   *
   * 「只支持不接入不算数」的镜像面: **授权了却不生效, 也必须说出来**. */
  if (plan.net.length > 0 && !ctx.netNS) {
    throw new Error(
      '能力集里有出网授权, 但没有给出网络命名空间 —— 拒绝启动。' +
        '要么把出网真的配出来, 要么别授这条能力: 静默失效比拒绝启动糟得多',
    );
  }

  const argv: string[] = [];

  /* ── 层 1: namespace 隔离 ──
   *
   * 没有出网授权: --net 建一个匿名网络命名空间, 里面**一张网卡都没有** ——
   * 这是"真的连不上", 不是"白名单里没有".
   *
   * 有出网授权: 进入 OS 预先配好的具名 ns —— 里面只有一根 veth, 对端是 OS,
   * **没有默认路由也没有 DNS**. 强制来自拓扑: 不走代理的程序不是被规则
   * 拒绝, 是没有路可走. 这时不能再 --net (那会换成一个新的空 ns, 网又没了). */
  /* ── 层 1: 自己加入 cgroup 再 exec ──
   *
   * **必须在最外层**, 也就是在任何 namespace 之前.
   *
   * 它原来在 unshare 里面, 靠的是"unshare --mount 只复制挂载树,
   * /sys/fs/cgroup 照样看得见" —— 一个没写出来的依赖. 而 `ip netns exec`
   * 正好破坏它: 为了让 /sys/class/net 反映新的网络命名空间, 它会重新挂
   * 一个干净的 sysfs, 于是 /sys/fs/cgroup 是空的. 真机报
   * `cannot create .../cgroup.procs: Directory nonexistent`.
   *
   * 提到最外层就没有这个依赖了: cgroup 归属是进程属性, 跨 fork/exec 和
   * 所有 namespace 继承, 后面怎么隔离都带得走. */
  const procsPath = `${paths.cgroupRoot}/${ctx.cgroupSlice}/cgroup.procs`;
  const hasLimit =
    plan.cgroup.memoryMaxBytes !== undefined ||
    plan.cgroup.cpuWeight !== undefined ||
    plan.cgroup.pidsMax !== undefined;
  if (hasLimit) {
    argv.push(paths.sh, '-c', `echo $$ > ${shQuote(procsPath)} && exec "$@"`, 'neox-os');
  }

  /* ── 层 2: namespace 隔离 ── */
  if (ctx.netNS) argv.push(paths.ip, 'netns', 'exec', ctx.netNS);
  argv.push(paths.unshare, '--mount', '--pid', '--fork');
  if (!ctx.netNS) argv.push('--net');

  /* ── 层 3 的命令行先拼好, 因为层 2 要把它整个塞进 sh -c ── */
  const inner: string[] = [paths.landlockRun];
  /* 先下执行底座 —— 没有它进程连自己的可执行文件都读不到.
   * 放在能力规则之前, 顺序稳定便于比对审计. */
  for (const p of plan.runtimeFs) inner.push('--ro', p);
  /* 设备节点必须 --rw: `> /dev/null` 是写操作.
   * 下成 --ro 的现象是"起 20 个后台进程, 20 个全挂在 cannot open /dev/null",
   * 而且只在真跑命令时才暴露. */
  for (const p of plan.runtimeDevices ?? []) inner.push('--rw', p);
  for (const rule of plan.fs) {
    const flag = rule.access.includes('write') ? '--rw' : '--ro';
    inner.push(flag, rule.path);
  }
  /* 进程**自己的可执行文件**必须可读, 否则 exec 都做不到.
   *
   * 规律: **"进程存在所需的一切" 都是强制机制的前置, 不是它要申请的能力** ——
   * 执行底座 + ABI socket + 它自己的二进制. 这些不该出现在能力集里:
   * 用户授权的是"它能碰什么业务数据", 不该操心"它怎么才能跑起来". */
  inner.push('--ro', body.argv[0]!);

  /* ABI socket 必须可读写, 否则进程连 OS 都调不到 */
  inner.push('--rw', ctx.abiSocket);
  inner.push('--');
  /* ── 层 4: 用户程序 ── */
  inner.push(...body.argv);

  argv.push(...inner);

  return { argv, env: buildEnv(body.env, ctx) };
}

/** 单引号包裹 —— 路径由 OS 生成不含用户输入, 但仍然不留注入面 */
function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

/**
 * 环境变量白名单.
 *   宿主环境**一律不继承**. Neox 踩过的账: 长驻进程继承了 env 就摘不掉,
 *   临时约束变成永久约束 (已知陷阱 E3).
 */
/** OS 注入的凭据与身份, 应用不许覆盖 */
const RESERVED_ENV = new Set(['NEOX_ABI_SOCKET', 'NEOX_ABI_TOKEN', 'NEOX_PID']);

function buildEnv(
  extra: Record<string, string> | undefined,
  ctx: { pid: string; abiSocket: string; abiToken?: string; proxyAddr?: string },
): Record<string, string> {
  const env: Record<string, string> = {
    PATH: '/usr/local/bin:/usr/bin:/bin',
    HOME: '/work',
    LANG: 'C.UTF-8',
    /* 进程通过这个 socket 调 ABI. 这是它跟 OS 之间**唯一**的通道 */
    NEOX_ABI_SOCKET: ctx.abiSocket,
    /* 一次性凭据. **走 env 不走命令行** ——
     * 命令行在 /proc/<pid>/cmdline 里对同机进程可见. */
    NEOX_ABI_TOKEN: ctx.abiToken ?? '',
    NEOX_PID: ctx.pid,
  };
  /* 出网走 OS 的代理. 大小写两套都给 —— curl 认小写、Java/.NET 认大写,
   * 只给一套的话总有一半程序连不出去, 而报出来的是"连接超时",
   * 排查的人根本想不到是环境变量的大小写. */
  if (ctx.proxyAddr) {
    const u = `http://${ctx.proxyAddr}`;
    for (const k of ['http_proxy', 'https_proxy', 'HTTP_PROXY', 'HTTPS_PROXY']) env[k] = u;
    /* npm 不认通用的 proxy 变量, 要自己那一套 */
    env.npm_config_proxy = u;
    env.npm_config_https_proxy = u;
    /* **本机回环必须直连, 其余一律走代理.**
     *
     * 这里原来是清空 no_proxy, 理由是"镜像里带着的 NO_PROXY 一旦命中就是直连,
     * 而直连在这里等于连不上". 那条理由对**外部主机**成立, 对 127.0.0.1 不成立
     * —— 回环在 netns 里是通的, 直连才是对的, 走代理反而是错的.
     *
     * 少了这一条, agent 一验证自己起的服务就撞墙, 而且只在**批过一次出网之后**
     * 才发作: 代理回一个没有 body 的 502, 跟真实原因隔着两层.
     *
     * 只放回环 —— 授权是按主机名授的, 别的绕过代理就等于绕过授权. */
    const LOOPBACK_DIRECT = '127.0.0.1,localhost,::1';
    env.no_proxy = LOOPBACK_DIRECT;
    env.NO_PROXY = LOOPBACK_DIRECT;
  }
  for (const [k, v] of Object.entries(extra ?? {})) {
    /* 只保护**具体的保留名**, 不是整个 NEOX_ 前缀.
     *
     * 早先按前缀一刀切, 结果应用自己的 NEOX_MODEL / NEOX_TASK 也被剥掉了 ——
     * 症状是"开关不生效"且完全没有报错. **管得太宽跟管得太松一样是缺陷.** */
    if (RESERVED_ENV.has(k)) continue;
    env[k] = v;
  }
  return env;
}
