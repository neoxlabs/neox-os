/**
 * init — Neox OS 的 PID 1.
 *
 *   这是"启动"这件事的落点. 镜像的 ENTRYPOINT 就是它, 容器里它是 1 号进程.
 *
 *   PID 1 的职责跟普通进程不同, 这三件不做会出真事故:
 *
 *     1. **收僵尸**. PID 1 是所有孤儿进程的父亲. 不 reap 就会攒僵尸,
 *        最后 pid 耗尽. (这也是为什么很多镜像要塞 tini —— 我们自己是
 *        init, 就自己做.)
 *     2. **转发信号**. docker stop 发 SIGTERM 给 PID 1. 不处理的话
 *        默认行为是**直接死**, 进程来不及落盘. 必须捕获 → 有序关机.
 *     3. **启动即自检**. confined 模式下内核不支持强制约束 → **不启动**,
 *        而不是启动了再说. 一个自称能隔离而实际没有的 OS 最危险.
 *
 *   跟 Linux 内核的关系: 我们不写内核. Linux 提供 namespace / cgroup /
 *   landlock / seccomp 这些**机制**, Neox OS 提供**策略** —— 谁能拿到
 *   什么、什么时候换出、事件怎么流、人怎么被找到. 这是 Android 跟 Linux
 *   的同一种关系.
 */

import { NeoxOs } from '@neox-os/init';
import { probeEnforcement } from '@neox-os/confine';
import type { EnforcementRequirement, OsMode } from '@neox-os/abi';

export interface BootOptions {
  mode: OsMode;
  volumeRoot: string;
  /** 自检要求的内核机制. 缺省是全套 */
  require?: EnforcementRequirement[];
  log?: (line: string) => void;
  /** 测试注入: 不真的挂信号处理器 */
  installSignalHandlers?: boolean;
}

export interface BootResult {
  os: NeoxOs;
  shutdown: (reason: string) => Promise<void>;
}

const ALL: EnforcementRequirement[] = ['fs-enforce', 'resource-limit', 'net-isolate'];

/**
 * 开机.
 *   顺序是刻意的: 自检 → 建 OS → 装信号 → 收僵尸.
 *   自检不过就在建 OS 之前抛 —— 半启动状态不存在.
 */
export async function boot(opts: BootOptions): Promise<BootResult> {
  const log = opts.log ?? ((l: string) => process.stdout.write(`${l}\n`));
  const required = opts.require ?? ALL;

  log(`[boot] Neox OS 启动 · mode=${opts.mode} volume=${opts.volumeRoot}`);

  /* ── 1. 启动自检 ── */
  if (opts.mode === 'confined') {
    const probe = probeEnforcement(required);
    if (!probe.usable) {
      /* fail-closed: 不降级到 dev, 不打个 warning 继续. 直接不启动. */
      log(`[boot] ✗ 内核无法强制约束: ${probe.reason}`);
      throw new Error(`boot aborted: ${probe.reason}`);
    }
    log(`[boot] ✓ 内核自检通过: ${probe.mechanisms.join(', ')}`);
  } else {
    log('[boot] ⚠ dev 模式 — 进程不受内核约束, 只能跑 inproc, 不要用于生产');
  }

  /* ── 2. 建 OS ── */
  const os = new NeoxOs({ mode: opts.mode, volumeRoot: opts.volumeRoot });
  log(`[boot] ✓ OS 就绪 · ABI ${os.abiVersion}`);

  /* ── 3. 有序关机 ── */
  let shuttingDown = false;
  const shutdown = async (reason: string): Promise<void> => {
    if (shuttingDown) return; /* 连按两次 Ctrl-C 不该走两遍关机 */
    shuttingDown = true;
    log(`[boot] 关机中 (${reason}) — 停 ${os.proc.list().length} 个进程`);
    os.shutdown(reason);
    /* 给进程一点时间落盘. 真实现里这里等各进程 ack 或超时. */
    await new Promise((r) => setTimeout(r, 50));
    log('[boot] 已关机');
  };

  if (opts.installSignalHandlers !== false) {
    /* docker stop → SIGTERM. 不接管的话 PID 1 的默认行为是直接死. */
    for (const sig of ['SIGTERM', 'SIGINT'] as const) {
      process.on(sig, () => {
        void shutdown(sig).then(() => process.exit(0));
      });
    }

    /* ── 4. 收僵尸 ──
     * PID 1 是孤儿进程的父亲. Node 会自动 reap 自己 spawn 的子进程,
     * 但**孤儿进程**要靠这里. 不做的话 pid 会被慢慢耗尽. */
    process.on('SIGCHLD', reapZombies);
  }

  return { os, shutdown };
}

/**
 * 收僵尸.
 *   Node 没有暴露 waitpid, 所以生产实现需要一个 native 小模块或者外挂 tini.
 *   这里留出明确的接缝并**大声记录**, 而不是假装做过了 ——
 *   悄悄不做才是事故的来源.
 */
let reapWarned = false;
function reapZombies(): void {
  if (reapWarned) return;
  reapWarned = true;
  process.stdout.write(
    '[boot] ⚠ SIGCHLD: 需要 native waitpid 才能收孤儿僵尸; ' +
      '在补上之前镜像里必须用 tini 作为 PID 1 的外壳\n',
  );
}
