/**
 * 约束层验收 — "能力是强制的, 不是问询的".
 *
 *   前两组是纯函数, 在 macOS 上就能锁住行为.
 *   第三组锁 fail-closed: 内核拦不住时**必须拒绝启动**, 不许降级.
 */

import { describe, it, expect } from 'vitest';
import { planFor, buildLaunch, probeEnforcement, DEFAULT_PATHS, type ProbeDeps } from '../index.js';
import type { Capability } from '@neox-os/abi';

const V = { volumeRoot: '/work' };

describe('planFor · 能力翻译成内核规则', () => {
  it('缺省是拒绝: 空能力集 = 什么都碰不到、完全断网、不能起子进程', () => {
    const plan = planFor([], V);
    expect(plan.fs).toEqual([]);
    expect(plan.net).toEqual([]);
    expect(plan.allowSubprocess).toBe(false);
    expect(plan.cgroup.pidsMax).toBe(64);
  });

  it('没有出网授权时**更**需要网络隔离 —— 断网要靠隔离, 不能靠"白名单里没有"', () => {
    const plan = planFor([], V);
    expect(plan.requires).toContain('net-isolate');
    expect(plan.requires).toEqual(['fs-enforce', 'resource-limit', 'net-isolate']);
  });

  it('只要求我们真正会施加的 —— 不要求自己都不用的 seccomp', () => {
    /* 真机第一次开机时它为"缺 seccomp"拒绝启动, 而 buildLaunch 从没
     * 施加过任何 seccomp 过滤. 为自己不用的东西拒绝开机是假严格. */
    expect(planFor([], V).requires).not.toContain('syscall-notify');
  });

  it('可写必然可读 —— landlock 里 write 不隐含 read, 分开授会踩坑', () => {
    const plan = planFor([{ axis: 'write', scope: '/out' }], V);
    expect(plan.fs).toEqual([{ path: '/work/out', access: ['read', 'write'] }]);
  });

  /* 两头都要验: 只验"放宽了"的话, 把上限整个去掉也能过 ——
   * 而没有上限等于一个 fork 炸弹就能拖垮整台机器 (上面还跑着别的对话). */
  it('proc 能力放宽配额到够跑 go test / npm ci, 但仍然有上限', () => {
    const p = planFor([{ axis: 'proc', scope: 'demo' }], V);
    expect(p.cgroup.pidsMax).toBe(4096);
    expect(p.cgroup.memoryMaxBytes).toBe(2 * 1024 ** 3);
  });

  it('没有 proc 能力时配额收窄, 但缺省仍然有上限', () => {
    const p = planFor([], V);
    expect(p.cgroup.pidsMax).toBe(64);
    expect(p.cgroup.memoryMaxBytes).toBe(512 * 1024 * 1024);
  });

  it('secret 不翻译成任何文件系统规则 —— 凭据不该以"某路径可读"的形式存在', () => {
    const plan = planFor([{ axis: 'secret', scope: 'github_token' }], V);
    expect(plan.fs).toEqual([]);
  });

  it('路径穿越直接拒绝, 不做解析 —— 解析等于给自己开后门', () => {
    const bad: Capability[] = [{ axis: 'read', scope: '/../../etc' }];
    expect(() => planFor(bad, V)).toThrow(/路径穿越/);
  });

  it('规则顺序稳定, 命令行可比对 (审计前提)', () => {
    const a = planFor(
      [
        { axis: 'read', scope: '/z' },
        { axis: 'read', scope: '/a' },
      ],
      V,
    );
    expect(a.fs.map((r) => r.path)).toEqual(['/work/a', '/work/z']);
  });
});

describe('buildLaunch · 分层收紧的启动命令行', () => {
  const ctx = { pid: 'p1', cgroupSlice: 'neox-os/p1', abiSocket: '/run/neox-os/p1.sock' };
  const body = { argv: ['/usr/bin/node', 'app.js'], cwd: '/work' };

  it('由外到内: sh(加入 cgroup) → unshare → landlock-run → 用户程序', () => {
    const plan = planFor([{ axis: 'write', scope: '/out' }], {
      ...V,
      memoryMaxBytes: 1 << 30,
    });
    const { argv } = buildLaunch(plan, body, ctx);

    /* cgroup 那层必须在最外面: `ip netns exec` 会重挂一个干净的 sysfs,
     * 里面 /sys/fs/cgroup 是空的 —— 真机报 Directory nonexistent. */
    expect(argv[0]).toBe('/bin/sh');
    expect(argv).toContain('/usr/bin/unshare');
    expect(argv.indexOf('/bin/sh')).toBeLessThan(argv.indexOf('/usr/bin/unshare'));
    expect(argv).toEqual(expect.arrayContaining(['--net', '--pid', '--mount', '--fork']));

    const iSh = argv.indexOf('/bin/sh');
    const iLl = argv.indexOf('/usr/local/bin/landlock-run');
    const iSep = argv.indexOf('--');
    expect(iSh).toBe(0);
    expect(iLl).toBeGreaterThan(iSh);
    expect(iSep).toBeGreaterThan(iLl);
    /* 用户程序在最里层 —— 它继承了以上全部约束 */
    expect(argv.slice(iSep + 1)).toEqual(['/usr/bin/node', 'app.js']);
  });

  it('cgroup 必须在 landlock 之前 —— 之后就写不了 /sys 了', () => {
    const plan = planFor([], { ...V, memoryMaxBytes: 1 << 20 });
    const { argv } = buildLaunch(plan, body, ctx);
    const shCmd = argv[argv.indexOf('/bin/sh') + 2]!;
    expect(shCmd).toContain('/sys/fs/cgroup/neox-os/p1/cgroup.procs');
    expect(shCmd).toContain('exec "$@"');
    expect(argv.indexOf('/bin/sh')).toBeLessThan(argv.indexOf('/usr/local/bin/landlock-run'));
  });

  it('不用 cgexec —— 它是 cgroup v1 的工具, v2 下直接失败', () => {
    const { argv } = buildLaunch(planFor([], V), body, ctx);
    expect(argv.join(' ')).not.toContain('cgexec');
  });

  it('写权限走 --rw, 只读走 --ro', () => {
    const plan = planFor(
      [
        { axis: 'read', scope: '/ro' },
        { axis: 'write', scope: '/rw' },
      ],
      V,
    );
    const { argv } = buildLaunch(plan, body, ctx);
    expect(argv).toEqual(expect.arrayContaining(['--ro', '/work/ro']));
    expect(argv).toEqual(expect.arrayContaining(['--rw', '/work/rw']));
  });

  it('执行底座必须在规则里 —— 否则进程连自己的可执行文件都读不到', () => {
    /* 真机第一次跑就是 exec failed: Permission denied */
    const { argv } = buildLaunch(planFor([], V), body, ctx);
    for (const p of ['/usr', '/bin', '/lib']) {
      const i = argv.indexOf(p);
      expect(i, `底座缺 ${p}`).toBeGreaterThan(0);
      expect(argv[i - 1]).toBe('--ro');
    }
  });

  it('ABI socket 必须可写, 否则进程连 OS 都调不到', () => {
    const { argv } = buildLaunch(planFor([], V), body, ctx);
    const i = argv.indexOf('/run/neox-os/p1.sock');
    expect(argv[i - 1]).toBe('--rw');
  });

  it('宿主环境一律不继承 —— 继承等于把凭据漏进沙盒', () => {
    const { env } = buildLaunch(planFor([], V), body, ctx);
    expect(Object.keys(env).sort()).toEqual([
      'HOME',
      'LANG',
      'NEOX_ABI_SOCKET',
      'NEOX_ABI_TOKEN',
      'NEOX_PID',
      'PATH',
    ]);
  });

  it('进程不能覆盖 OS 注入的 NEOX_* —— 否则能把自己的 pid 改成别人的', () => {
    const { env } = buildLaunch(
      planFor([], V),
      { ...body, env: { NEOX_PID: 'p999', MY_VAR: 'ok' } },
      ctx,
    );
    expect(env.NEOX_PID).toBe('p1');
    expect(env.MY_VAR).toBe('ok');
  });

  it('空 argv 直接拒绝', () => {
    expect(() => buildLaunch(planFor([], V), { argv: [], cwd: '/work' }, ctx)).toThrow();
  });
});

describe('probeEnforcement · fail-closed + 需求/机制解耦', () => {
  const linuxAll: ProbeDeps = {
    platform: 'linux',
    fileExists: () => true,
    readFile: (p) =>
      p.includes('actions_avail')
        ? 'kill_process errno user_notif trace log allow'
        : 'landlock,capability,bpf',
    paths: DEFAULT_PATHS,
  };

  it('非 Linux 一律不可用 —— 这是正确行为, 不是缺陷', () => {
    const probe = probeEnforcement(['fs-enforce'], { ...linuxAll, platform: 'darwin' });
    expect(probe.usable).toBe(false);
    expect(probe.reason).toMatch(/只在 Linux/);
  });

  it('机制齐全时可用', () => {
    const probe = probeEnforcement(['fs-enforce', 'resource-limit', 'net-isolate'], linuxAll);
    expect(probe.usable).toBe(true);
    expect(probe.missing).toEqual([]);
    expect(probe.mechanisms).toEqual(expect.arrayContaining(['landlock', 'cgroup2', 'netns']));
  });

  it('内核有 landlock 但启动器二进制不在 → 不算数', () => {
    const noBinary: ProbeDeps = {
      ...linuxAll,
      fileExists: (p) => p !== DEFAULT_PATHS.landlockRun,
    };
    const probe = probeEnforcement(['fs-enforce'], noBinary);
    expect(probe.usable).toBe(false);
    expect(probe.missing).toEqual(['fs-enforce']);
  });

  it('只支持不接入不算数 —— bpf-lsm 只进诊断, 不满足 fs-enforce', () => {
    /* 模拟无 landlock、有 bpf-lsm 的 linuxkit 环境: 检出但未接入的机制不能满足强制约束 */
    const linuxkit: ProbeDeps = {
      platform: 'linux',
      fileExists: (p) => !p.includes('landlock'),
      readFile: (p) =>
        p.includes('actions_avail') ? 'errno user_notif allow' : 'capability,bpf',
      paths: DEFAULT_PATHS,
    };
    const probe = probeEnforcement(['fs-enforce', 'net-isolate'], linuxkit);
    expect(probe.usable).toBe(false);
    expect(probe.missing).toEqual(['fs-enforce']);
    expect(probe.satisfied).toEqual(['net-isolate']);
    /* 探到了但没接入 —— 必须出现在诊断里, 否则我们会以为是内核不行 */
    expect(probe.detectedNotWired).toContain('bpf-lsm');
    expect(probe.reason).toMatch(/尚未接入/);
  });

  it('seccomp 判据是内核支持哪些 action, 不是"我自己被没被限制"', () => {
    const noUserNotif: ProbeDeps = {
      ...linuxAll,
      readFile: (p) => (p.includes('actions_avail') ? 'errno trace allow' : 'landlock'),
    };
    expect(probeEnforcement(['syscall-notify'], noUserNotif).usable).toBe(false);
    /* 内核支持 user_notif 时也只进诊断 —— 我们还没接 unotify 的下发路径 */
    expect(probeEnforcement(['syscall-notify'], linuxAll).usable).toBe(false);
    expect(probeEnforcement(['syscall-notify'], linuxAll).detectedNotWired)
      .toContain('seccomp-user-notif');
  });
});
