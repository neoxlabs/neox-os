/**
 * wire — NeoxABI 的线格式.
 *
 *   这是**跨语言边界**: 将来系统层可能换 Go, 引擎层留 TypeScript,
 *   两边只认这份线格式. 所以它必须简单到用任何语言半天能实现完.
 *
 *   帧: [4 字节大端长度][UTF-8 JSON]
 *     · 长度前缀而不是换行分隔 —— JSON 里出现换行不用转义, 也不会粘包
 *     · 有最大帧长上限, 否则一个坏长度就能让对端分配 4GB
 *
 *   请求都带 id, 回复按 id 归位.
 *     **不许按"最后一个请求"归位** —— 并发请求的回复允许乱序到达,
 *     按顺序归位会把 A 的结果接到 B 上, 而且症状会伪装成"对端返回了错数据".
 *     (这是从 Neox 带过来的已知陷阱 B1 的同类问题.)
 */

import type { DecisionRequest, DecisionResolution, CapabilityAxis, BudgetSpent } from './index.js';

/** 单帧上限 16 MiB. 超过说明协议被误用 —— 大内容应该走引用不走帧 */
export const MAX_FRAME_BYTES = 16 * 1024 * 1024;

export const WIRE_VERSION = 1;

/* ── 客户端 → 服务端 ─────────────────────────────────────── */

export type AbiRequest =
  | { id: number; method: 'hello'; params: { token: string; wire: number } }
  | { id: number; method: 'emit'; params: { payload: unknown } }
  | { id: number; method: 'decide'; params: { request: DecisionRequest } }
  | { id: number; method: 'can'; params: { axis: CapabilityAxis; scope: string } }
  | { id: number; method: 'spend'; params: { delta: Partial<BudgetSpent> } };

export type AbiMethod = AbiRequest['method'];

/* ── 服务端 → 客户端 ─────────────────────────────────────── */

export type AbiResponse =
  | { id: number; result: AbiResult }
  | { id: number; error: { code: AbiErrorCode; message: string } };

export type AbiResult =
  | { kind: 'hello'; pid: string; abiVersion: string }
  | { kind: 'ok' }
  | { kind: 'bool'; value: boolean }
  | { kind: 'decision'; resolution: DecisionResolution };

export type AbiErrorCode =
  /** 没先 hello, 或 token 不认 */
  | 'unauthenticated'
  /** 线格式版本不匹配 */
  | 'wire_version'
  /** 预算超了 */
  | 'budget_exceeded'
  /** 进程已经是终态 */
  | 'process_gone'
  /** 帧坏了 / JSON 解不开 / 方法不认 */
  | 'bad_request'
  /** 服务端内部错 */
  | 'internal';

/* ── 编解码 ──────────────────────────────────────────────── */

export function encodeFrame(msg: AbiRequest | AbiResponse): Buffer {
  const json = Buffer.from(JSON.stringify(msg), 'utf8');
  if (json.length > MAX_FRAME_BYTES) {
    throw new Error(`frame too large: ${json.length} > ${MAX_FRAME_BYTES}`);
  }
  const head = Buffer.allocUnsafe(4);
  head.writeUInt32BE(json.length, 0);
  return Buffer.concat([head, json]);
}

/**
 * 增量解帧器.
 *   TCP/unix socket 不保证一次 data 事件就是一帧 —— 可能半帧, 也可能几帧粘在一起.
 *   所以必须自己缓冲按长度切, **不能假设一次 data 就是一条消息**.
 */
export class FrameDecoder {
  private buf: Buffer = Buffer.alloc(0);

  /** 喂入原始字节, 吐出这一批能完整解出的所有消息 */
  push(chunk: Buffer): unknown[] {
    this.buf = this.buf.length === 0 ? chunk : Buffer.concat([this.buf, chunk]);
    const out: unknown[] = [];

    for (;;) {
      if (this.buf.length < 4) break;
      const len = this.buf.readUInt32BE(0);
      if (len > MAX_FRAME_BYTES) {
        /* 坏长度 —— 不要试图恢复, 恢复就是给攻击者留缝. 直接报错让上层断连 */
        throw new Error(`frame length ${len} exceeds max`);
      }
      if (this.buf.length < 4 + len) break;
      const json = this.buf.subarray(4, 4 + len).toString('utf8');
      this.buf = this.buf.subarray(4 + len);
      out.push(JSON.parse(json));
    }
    return out;
  }

  /** 还缓着多少字节 —— 诊断用 */
  get pending(): number {
    return this.buf.length;
  }
}
