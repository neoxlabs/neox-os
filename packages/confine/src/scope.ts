/**
 * scope — 能力作用域匹配 · **全工程唯一实现**.
 *
 *   为什么单独抽出来:
 *     这套语义有两个消费者 ——
 *       ① planFor()          翻译成内核规则 (强制)
 *       ② capabilityAllows() 回答"我能不能做" (预检)
 *     两边各写一份就是双真相源: 预检说能, 内核说不能 (或者反过来),
 *     而这种不一致**只在生产环境的特定路径上才暴露**.
 *     所以两个消费者必须调同一份函数, 并且有测试锁住它们的一致性.
 *
 *   路径按段匹配、保留 UNC 连续斜杠, 域名通配符不包含父域:
 *   否则会把相邻路径、不同位置或未授权域名误判为授权范围内.
 */

import type { CapabilityAxis } from '@neox-os/abi';

/** 路径穿越的哨兵值 —— 永远匹配不上任何东西 */
const NEVER = '\u0000never';

export function scopeMatches(
  axis: CapabilityAxis,
  granted: string,
  requested: string,
): boolean {
  switch (axis) {
    case 'read':
    case 'write':
      /* fs 轴上 '*' = 整个卷根, **不是**无条件放行 ——
       * 内核只可能授到卷内, 预检无条件 true 就跟内核对不上. */
      return pathWithin(granted === '*' ? '/' : granted, requested);
    case 'net':
      return granted === '*' || hostMatches(granted, requested);
    case 'proc':
    case 'secret':
      return granted === '*' || granted === requested;
  }
}

/**
 * 路径包含判断.
 *   "/a" 不匹配 "/ab" —— 必须按路径段比, 不能按字符串前缀比 (陷阱 D2).
 */
export function pathWithin(grantedPrefix: string, requested: string): boolean {
  const g = normalizePath(grantedPrefix);
  const r = normalizePath(requested);
  if (g === NEVER || r === NEVER) return false;
  if (g === '/') return true;
  if (r === g) return true;
  return r.startsWith(g.endsWith('/') ? g : `${g}/`);
}

/**
 * 路径规范化.
 *   - 反斜杠统一成正斜杠
 *   - **不压缩连续斜杠** —— UNC 路径 \\host\share 压缩后会指向别处 (陷阱 D1)
 *   - 含 `..` 一律判不匹配, 不做解析 —— 解析等于给自己开后门
 */
export function normalizePath(p: string): string {
  let out = p.replace(/\\/g, '/');
  if (!out.startsWith('/')) out = `/${out}`;
  if (out.length > 1 && out.endsWith('/')) out = out.slice(0, -1);
  if (out.split('/').includes('..')) return NEVER;
  return out;
}

/**
 * host 匹配.
 *   `*.example.com` **不覆盖 example.com 本身** —— 通配符授权不连带父域 (陷阱 D3).
 *   端口写了就必须一致.
 */
export function hostMatches(granted: string, requested: string): boolean {
  const [gHost, gPort] = splitHostPort(granted);
  const [rHost, rPort] = splitHostPort(requested);
  if (gPort !== undefined && gPort !== rPort) return false;

  if (gHost.startsWith('*.')) {
    const suffix = gHost.slice(1); /* ".example.com" */
    return rHost.endsWith(suffix) && rHost.length > suffix.length;
  }
  return gHost === rHost;
}

export function splitHostPort(s: string): [string, string | undefined] {
  const i = s.lastIndexOf(':');
  if (i === -1) return [s.toLowerCase(), undefined];
  return [s.slice(0, i).toLowerCase(), s.slice(i + 1)];
}
