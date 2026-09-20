/**
 * Fail-closed 验收 — 四种模式/进程体组合, 只有两种合法.
 *
 *   这组测试锁的是一件事: **不存在"看起来隔离了但实际没隔离"的状态。**
 *   一个自称能隔离而实际没有的 OS, 比明确不能隔离的 OS 危险得多.
 */

import { describe, it, expect } from 'vitest';
import { NeoxOs, type Spawner } from '../os.js';
import type { EnforcementProbe } from '@neox-os/abi';

const noop = async () => 'ok';

/* 测试绝不真的 spawn 系统二进制 —— 命令行正确性由 confine 的纯函数测试锁 */
const fakeSpawner: Spawner = () => ({
  onLine: () => {},
  onExit: (cb) => setTimeout(() => cb(0, null), 0),
  kill: () => {},
});
const okProbe = (): EnforcementProbe => ({
  platform: 'linux',
  mechanisms: ['landlock', 'cgroup2', 'netns'],
  detectedNotWired: [],
  satisfied: ['fs-enforce', 'resource-limit', 'net-isolate'],
  missing: [],
  usable: true,
});
const badProbe = (): EnforcementProbe => ({
  platform: 'linux',
  mechanisms: ['cgroup2', 'netns'],
  detectedNotWired: ['bpf-lsm'],
  satisfied: ['resource-limit', 'net-isolate'],
  missing: ['fs-enforce'],
  usable: false,
  reason: '无法满足: fs-enforce (内核有 bpf-lsm 但我们尚未接入, 不能算数)',
});

describe('模式闸 · 四种组合只有两种合法', () => {
  it('缺省是 confined —— 缺省必须是安全的那个', () => {
    expect(new NeoxOs().mode).toBe('confined');
  });

  it('confined + inproc → 拒: 同堆闭包内核管不着, 能力检查会形同虚设', () => {
    const os = new NeoxOs({ probe: okProbe, spawner: fakeSpawner });
    expect(() =>
      os.proc.spawn({ app: 'x', caps: [] }, { kind: 'inproc', entry: noop }),
    ).toThrow(/形同虚设/);
    os.shutdown();
  });

  it('dev + exec → 拒: 那会是一个完全不受约束的真进程, 比什么都不做更危险', () => {
    const os = new NeoxOs({ mode: 'dev' });
    expect(() =>
      os.proc.spawn({ app: 'x', caps: [] }, { kind: 'exec', argv: ['/bin/true'], cwd: '/work' }),
    ).toThrow(/不受约束/);
    os.shutdown();
  });

  it('confined + exec + 内核拦不住 → 拒绝启动, 不降级不 warning', () => {
    const os = new NeoxOs({ probe: badProbe, spawner: fakeSpawner });
    expect(() =>
      os.proc.spawn({ app: 'x', caps: [] }, { kind: 'exec', argv: ['/bin/true'], cwd: '/work' }),
    ).toThrow(/confinement unavailable/);
    os.shutdown();
  });

  it('拒绝启动时不留下半个进程 —— 进程表必须是干净的', () => {
    const os = new NeoxOs({ probe: badProbe, spawner: fakeSpawner });
    try {
      os.proc.spawn({ app: 'x', caps: [] }, { kind: 'exec', argv: ['/bin/true'], cwd: '/work' });
    } catch {
      /* 预期 */
    }
    expect(os.proc.list()).toEqual([]);
    os.shutdown();
  });

  it('confined + exec + 内核齐全 → 放行, 并把约束方案写进审计日志', () => {
    const os = new NeoxOs({ probe: okProbe, spawner: fakeSpawner });
    const pid = os.proc.spawn(
      { app: 'x', caps: [{ axis: 'read', scope: '/in' }] },
      { kind: 'exec', argv: ['/bin/true'], cwd: '/work' },
    );
    const audit = os.log.replay(pid).find((e) => e.kind === 'cap.used');
    expect(audit).toBeDefined();
    const payload = audit!.payload as { confinement: { fs: unknown[] }; argv: string[] };
    /* 审计里必须能看到**实际下发的命令行**, 不是"我们打算怎么限" */
    /* 只关心隔离层确实下发了 —— 分层顺序由 confine 自己的测试钉 */
    expect(payload.argv).toContain('/usr/bin/unshare');
    expect(payload.confinement.fs).toEqual([{ path: '/work/in', access: ['read'] }]);
    os.shutdown();
  });
});
