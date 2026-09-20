/**
 * capabilities — 能力预检.
 *
 *   语义**不在这里定义**, 在 @neox-os/confine 的 scope.ts —— 那是唯一实现.
 *   这里只是把它包成"这个进程能不能做 X"的问法.
 *
 *   预检 vs 强制, 分工必须说清楚:
 *     强制 = 内核 (landlock/netns). 进程调不调预检都逃不掉
 *     预检 = 这个函数. 唯一价值是**让进程提前知道**, 少吃一个莫名其妙的 EACCES
 *
 *   所以预检**不是安全边界**. 它要是跟内核不一致, 不会造成越权,
 *   但会造成 agent 困惑 (以为能做结果被拒, 或以为不能做而放弃).
 *   一致性由 confine 的一致性测试锁住.
 */

import type { Capability, CapabilityAxis } from '@neox-os/abi';
import { scopeMatches } from '@neox-os/confine';

/** 缺省是拒绝: 空能力集 = 什么都不能做 */
export function capabilityAllows(
  caps: readonly Capability[],
  axis: CapabilityAxis,
  scope: string,
): boolean {
  for (const cap of caps) {
    if (!axisSatisfies(cap.axis, axis)) continue;
    if (scopeMatches(axis, cap.scope, scope)) return true;
  }
  return false;
}

/**
 * 轴的蕴含关系 —— 必须跟 planFor 翻译内核规则时的蕴含**完全一致**.
 *   write 蕴含 read: landlock 里 write 不隐含 read, 所以 planFor 授 write 时
 *   会同时授 read. 预检不认这条就会告诉 agent "你不能读你自己能写的目录",
 *   于是它放弃了本来有权做的事. 一致性测试锁住这一条.
 */
function axisSatisfies(granted: CapabilityAxis, requested: CapabilityAxis): boolean {
  if (granted === requested) return true;
  return granted === 'write' && requested === 'read';
}
