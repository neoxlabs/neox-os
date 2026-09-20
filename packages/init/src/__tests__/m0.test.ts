/**
 * 行为验收 — 一个不需要人在场的进程.
 *
 *   这个文件是整个工程存在的理由. 它验证的场景在旧架构里做不出来:
 *   没有观众 = 没有 timeline = 没有轮次边界 = loop 无处依附.
 *
 *   跑通 = 四个内核对象 (Process / 事件日志 / 能力 / 决策) 撑得住.
 *   跑不通 = 对象模型还没想清楚, 此时沉没成本最小.
 */

import { describe, it, expect } from 'vitest';
import { NeoxOs } from '../os.js';
import type { ProcessContext, ProcessEntry } from '@neox-os/abi';

/* 行为验收跑在 dev 模式: 进程是同堆闭包, 内核管不着, 但 Process /
 * 事件日志 / 决策 / 预算这四件事的语义跟 confined 模式完全一致.
 * confined 模式的强制约束由 confine 包和 fail-closed 测试单独锁. */
const devOs = (opts: Record<string, unknown> = {}) => new NeoxOs({ mode: 'dev', ...opts });
const inproc = (entry: ProcessEntry) => ({ kind: 'inproc' as const, entry });

/** 等到条件成立 —— 不用固定 sleep, 避免慢机器上偶发失败 */
async function until(cond: () => boolean, timeoutMs = 2000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!cond()) {
    if (Date.now() > deadline) throw new Error('until() timed out');
    await new Promise((r) => setTimeout(r, 1));
  }
}

describe('M0 · 不需要人在场的进程', () => {
  it('全程无客户端连接, 进程照跑; 需要拍板时挂起等人; 任意设备可解决; 事后可完整重建', async () => {
    const os = devOs();

    /* ── 1. 起进程. 注意: 没有任何订阅者, 没有任何 UI 连着 ── */
    const pid = os.proc.spawn(
      {
        app: 'demo',
        name: '夜间调研',
        caps: [
          { axis: 'read', scope: '/work' },
          { axis: 'net', scope: '*.example.com' },
        ],
      },
      inproc(async (ctx: ProcessContext) => {
        ctx.emit({ step: 'started' });

        /* 能力检查: 授了的能过 */
        expect(ctx.can('read', '/work/notes.md')).toBe(true);
        /* 没授的不能 —— 而且无人在场时它给的是确定答案, 不是卡住 */
        expect(ctx.can('write', '/work/notes.md')).toBe(false);

        /* ── 需要外部决定. 进程在这里转 waiting, 不占计算 ── */
        const decision = await ctx.decide({
          urgency: 'high',
          present: {
            kind: 'choice',
            title: '选择调研方向',
            options: [
              { id: 'deep', label: '深挖单点' },
              { id: 'wide', label: '横向铺开' },
            ],
          },
        });

        ctx.emit({ step: 'resumed', chose: decision.choice, by: decision.by });
        return { direction: decision.choice };
      }),
    );

    /* ── 2. 进程自己跑到了"等人"这一步, 全程没有观众 ── */
    await until(() => os.proc.info(pid)!.state === 'waiting');

    const pending = os.decide.pending();
    expect(pending).toHaveLength(1);
    expect(pending[0]!.pid).toBe(pid);
    expect(pending[0]!.request.present.kind).toBe('choice');

    /* ── 3. 现在才有一个客户端接入. 它必须能看到之前发生的一切 ── */
    const seenByLateClient: string[] = [];
    const sub = os.log.subscribe(pid, (e) => seenByLateClient.push(e.kind));

    /* 迟到的订阅者补齐了全部历史 —— 没有断层 */
    expect(seenByLateClient).toEqual([
      'proc.state', // created
      'proc.state', // running
      'proc.output', // started
      'cap.used', // read /work/notes.md
      'cap.denied', // write /work/notes.md
      'decide.requested',
      'proc.state', // waiting
    ]);

    /* ── 4. 决策由"手机"解决 —— 跟起进程的不是同一个客户端 ── */
    os.decide.resolve(pending[0]!.did, 'wide', 'phone:liu');

    await until(() => os.proc.info(pid)!.state === 'exited');

    /* ── 5. 进程正常结束, 结果是结构化的, 不是给人看的文本 ── */
    const info = os.proc.info(pid)!;
    expect(info.outcome).toEqual({ ok: true, value: { direction: 'wide' } });

    /* 订阅者实时收到了后续 */
    expect(seenByLateClient.slice(7)).toEqual([
      'decide.resolved',
      'proc.state', // running
      'proc.output', // resumed
      'proc.state', // exited
      'proc.outcome',
    ]);
    sub.close();

    /* ── 6. 事后任意时刻接入, 从日志完整重建过程 ── */
    const full = os.log.replay(pid);
    expect(full.map((e) => e.seq)).toEqual(full.map((_, i) => i)); // seq 连续无洞
    const resolved = full.find((e) => e.kind === 'decide.resolved')!;
    expect(resolved.payload).toMatchObject({ choice: 'wide', by: 'phone:liu' });

    os.shutdown();
  });

  it('决策超时由 OS 兜底, 并且在日志里看得见是谁做的决定', async () => {
    const os = devOs();
    const pid = os.proc.spawn({ app: 'demo', caps: [] }, inproc(async (ctx) => {
      const d = await ctx.decide({
        present: { kind: 'approve', title: '要不要继续' },
        onTimeout: { afterMs: 20, choose: 'no' },
      });
      return d;
    }));

    await until(() => os.proc.info(pid)!.state === 'exited', 3000);
    const info = os.proc.info(pid)!;
    expect((info.outcome!.value as { choice: string }).choice).toBe('no');
    /* 关键: 不许静默默认. 日志里必须留下"这是超时替你做的" */
    const ev = os.log.replay(pid).find((e) => e.kind === 'decide.resolved')!;
    expect(ev.payload).toMatchObject({ by: 'timeout', choice: 'no' });

    os.shutdown();
  });

  it('预算由 OS 强制, 进程躲不掉', async () => {
    const os = devOs();
    const pid = os.proc.spawn(
      { app: 'demo', caps: [], budget: { tokens: 100 } },
      inproc(async (ctx) => {
        ctx.spend({ tokens: 60 });
        ctx.spend({ tokens: 60 }); // 超了 → 抛
        return 'never reached';
      }),
    );

    await until(() => os.proc.info(pid)!.state === 'failed');
    expect(os.proc.info(pid)!.outcome!.error!.code).toBe('budget_exceeded');
    os.shutdown();
  });

  it('等人期间不计活跃时长 —— 等一天和等一秒成本相同', async () => {
    /* 注入假时钟: 决策等待期间时间跳 24 小时 */
    let clock = 1_000_000;
    const os = devOs({ now: () => clock });

    const pid = os.proc.spawn(
      { app: 'demo', caps: [], budget: { activeMs: 5000 } },
      inproc(async (ctx) => {
        await ctx.decide({ present: { kind: 'approve', title: 'ok?' } });
        /* 醒来后再花一点点活跃时间 —— 没超预算, 因为等待的 24h 不算 */
        clock += 10;
        ctx.spend({ tokens: 1 });
        return 'done';
      }),
    );

    await until(() => os.proc.info(pid)!.state === 'waiting');
    clock += 24 * 60 * 60 * 1000; // 等了一整天
    os.decide.resolve(os.decide.pending()[0]!.did, 'yes', 'phone');

    await until(() => os.proc.info(pid)!.state === 'exited');
    expect(os.proc.info(pid)!.outcome).toEqual({ ok: true, value: 'done' });
    os.shutdown();
  });
});

describe('事件日志 · 订阅补齐不许乱序或断层', () => {
  it('订阅期间产生的事件排在历史之后, 不重不漏', async () => {
    const os = devOs();
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));

    const pid = os.proc.spawn({ app: 'demo', caps: [] }, inproc(async (ctx) => {
      ctx.emit({ n: 1 });
      ctx.emit({ n: 2 });
      await gate;
      ctx.emit({ n: 3 });
      return 'ok';
    }));

    await until(() => os.log.replay(pid).filter((e) => e.kind === 'proc.output').length === 2);

    const seqs: number[] = [];
    const sub = os.log.subscribe(pid, (e) => seqs.push(e.seq));
    release();
    await until(() => os.proc.info(pid)!.state === 'exited');

    /* 从 0 开始、严格递增、无重复 */
    expect(seqs).toEqual(seqs.map((_, i) => i));
    sub.close();
    os.shutdown();
  });
});
