/** 开机验收 — 半启动状态不存在. */
import { describe, it, expect } from 'vitest';
import { boot } from '../init.js';

const silent = () => {};

describe('boot · 启动自检', () => {
  it('confined 模式在非 Linux 上直接不启动 —— 不降级到 dev', async () => {
    /* 开发机就是非 Linux, 所以这条在本机跑的就是真实路径 */
    await expect(
      boot({ mode: 'confined', volumeRoot: '/work', log: silent, installSignalHandlers: false }),
    ).rejects.toThrow(/boot aborted/);
  });

  it('dev 模式能起来, 但必须大声说自己不受约束', async () => {
    const lines: string[] = [];
    const { os, shutdown } = await boot({
      mode: 'dev',
      volumeRoot: '/work',
      log: (l) => lines.push(l),
      installSignalHandlers: false,
    });
    expect(os.mode).toBe('dev');
    expect(lines.join('\n')).toMatch(/不受内核约束/);
    await shutdown('test');
  });

  it('关机是幂等的 —— 连按两次 Ctrl-C 不该走两遍', async () => {
    const lines: string[] = [];
    const { shutdown } = await boot({
      mode: 'dev',
      volumeRoot: '/work',
      log: (l) => lines.push(l),
      installSignalHandlers: false,
    });
    await shutdown('first');
    await shutdown('second');
    expect(lines.filter((l) => l.includes('已关机'))).toHaveLength(1);
  });
});
