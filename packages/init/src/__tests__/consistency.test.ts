/**
 * 一致性验收 —— 预检(用户态) 必须跟 强制(内核规则) 说同一件事.
 *
 *   这组测试锁的是审计里发现的最严重一处: 能力语义原来有两份实现
 *   (init/capabilities.ts 和 confine/plan.ts 各写一套路径/host 匹配),
 *   两份不一致时:
 *     · 预检说能 / 内核说不能 → agent 以为能做, 撞一堵看不见的墙
 *     · 预检说不能 / 内核说能 → agent 放弃了它本来有权做的事
 *   两种都不造成越权, 但都让 agent 变笨. 而这种漂移**只在生产的特定路径上暴露**.
 *
 *   修法: 语义收敛到 confine/scope.ts 一份, 这里用交叉验证锁死.
 */

import { describe, it, expect } from 'vitest';
import { capabilityAllows } from '../capabilities.js';
import { planFor, resolveInVolume, pathWithin, hostMatches } from '@neox-os/confine';
import type { Capability } from '@neox-os/abi';

const VOLUME = '/work';

/** 内核那侧的答案: 请求的绝对路径是否落在 plan 的某条 fs 规则内 */
function kernelWouldAllowRead(caps: Capability[], relPath: string): boolean {
  const plan = planFor(caps, { volumeRoot: VOLUME });
  const abs = resolveInVolume(VOLUME, relPath);
  return plan.fs.some((r) => r.access.includes('read') && pathWithin(r.path, abs));
}

function kernelWouldAllowNet(caps: Capability[], target: string): boolean {
  const plan = planFor(caps, { volumeRoot: VOLUME });
  return plan.net.some((r) =>
    hostMatches(r.port === undefined ? r.host : `${r.host}:${r.port}`, target),
  );
}

describe('预检 ↔ 内核规则 一致性', () => {
  const capSets: { name: string; caps: Capability[] }[] = [
    { name: '空能力集', caps: [] },
    { name: '只读一个目录', caps: [{ axis: 'read', scope: '/src' }] },
    { name: '可写一个目录', caps: [{ axis: 'write', scope: '/out' }] },
    { name: '读写多个', caps: [
      { axis: 'read', scope: '/src' },
      { axis: 'write', scope: '/out' },
      { axis: 'read', scope: '/docs' },
    ] },
    { name: '通配全部', caps: [{ axis: 'read', scope: '*' }] },
  ];

  const paths = [
    '/src/a.ts', '/src', '/srcx/a.ts', '/out/b.txt', '/out',
    '/docs/x/y.md', '/etc/passwd', '/', '/src/deep/nested/file.ts',
  ];

  for (const { name, caps } of capSets) {
    it(`读权限一致 · ${name}`, () => {
      for (const p of paths) {
        const precheck = capabilityAllows(caps, 'read', p);
        const kernel = kernelWouldAllowRead(caps, p);
        expect(
          precheck,
          `不一致: caps=${name} path=${p} 预检=${precheck} 内核=${kernel}`,
        ).toBe(kernel);
      }
    });
  }

  const netCaps: { name: string; caps: Capability[] }[] = [
    { name: '无出网', caps: [] },
    { name: '单域名', caps: [{ axis: 'net', scope: 'api.example.com' }] },
    { name: '通配子域', caps: [{ axis: 'net', scope: '*.example.com' }] },
    { name: '带端口', caps: [{ axis: 'net', scope: 'db.internal:5432' }] },
  ];
  const hosts = [
    'api.example.com', 'example.com', 'a.b.example.com',
    'evil.com', 'db.internal:5432', 'db.internal:5433', 'db.internal',
  ];

  for (const { name, caps } of netCaps) {
    it(`出网一致 · ${name}`, () => {
      for (const h of hosts) {
        expect(
          capabilityAllows(caps, 'net', h),
          `不一致: caps=${name} host=${h}`,
        ).toBe(kernelWouldAllowNet(caps, h));
      }
    });
  }

  it('通配符授权不连带父域 —— 两侧都必须这么认', () => {
    const caps: Capability[] = [{ axis: 'net', scope: '*.example.com' }];
    expect(capabilityAllows(caps, 'net', 'example.com')).toBe(false);
    expect(kernelWouldAllowNet(caps, 'example.com')).toBe(false);
    expect(capabilityAllows(caps, 'net', 'a.example.com')).toBe(true);
    expect(kernelWouldAllowNet(caps, 'a.example.com')).toBe(true);
  });

  it('路径按段比而非字符串前缀 —— 两侧都必须这么认', () => {
    const caps: Capability[] = [{ axis: 'read', scope: '/src' }];
    expect(capabilityAllows(caps, 'read', '/srcx/a.ts')).toBe(false);
    expect(kernelWouldAllowRead(caps, '/srcx/a.ts')).toBe(false);
  });
});
