/**
 * abiServer — 把 ProcessContext 从"同进程函数调用"变成"跨进程 IPC".
 *
 *   这一步买到三件事:
 *
 *   1. **执行体崩了 OS 不崩**. 之前进程是同一个堆里的闭包, 它抛什么
 *      OS 就吃什么. 现在它死了只是一条连接断掉.
 *   2. **语言边界**. 系统层将来换 Go, 引擎层留 TS, 两边只认线格式.
 *   3. **权限边界**. 进程只能拿到 socket, 拿不到 OS 实例, 所以它
 *      写不进别人的日志、改不了别人的状态.
 *
 *   身份: 每个进程 spawn 时发一个一次性 token (走 env), 连上先 hello.
 *     · token → pid 映射只在 OS 内存里
 *     · 一个 token 只能绑一个 pid, 进程冒充不了别人
 *     · 进程终态后 token 立即失效
 *
 *   注意 sun_path 长度限制: macOS 104 字节 / Linux 108 字节.
 *   socket 路径必须短, 放 /run/neox-os/<pid>.sock 而不是深目录.
 */

import * as net from 'node:net';
import { randomBytes } from 'node:crypto';
import { unlinkSync, existsSync } from 'node:fs';

import type {
  AbiRequest,
  AbiResponse,
  AbiResult,
  AbiErrorCode,
  ProcessContext,
  ProcessId,
} from '@neox-os/abi';
import { encodeFrame, FrameDecoder, WIRE_VERSION, BudgetExceeded } from '@neox-os/abi';

/** sun_path 上限 (取两平台里更严的 macOS 104, 留出终止符余量) */
export const MAX_SOCKET_PATH = 100;

export interface AbiServerOptions {
  socketPath: string;
  /** 用 token 换这个进程的 ProcessContext. 返回 null = token 不认 */
  resolve: (token: string) => { pid: ProcessId; ctx: ProcessContext } | null;
  onError?: (e: Error) => void;
}

export class AbiServer {
  private server: net.Server | null = null;
  private readonly conns = new Set<net.Socket>();

  constructor(private readonly opts: AbiServerOptions) {
    if (Buffer.byteLength(opts.socketPath) > MAX_SOCKET_PATH) {
      /* 提前炸掉, 否则 bind 会给一个很难懂的 EINVAL/ENAMETOOLONG */
      throw new Error(
        `socket 路径过长 (${Buffer.byteLength(opts.socketPath)} > ${MAX_SOCKET_PATH}): ${opts.socketPath}`,
      );
    }
  }

  async listen(): Promise<void> {
    /* 残留的 socket 文件会让 bind 报 EADDRINUSE —— 上次没干净退出时常见 */
    if (existsSync(this.opts.socketPath)) {
      try {
        unlinkSync(this.opts.socketPath);
      } catch {
        /* 删不掉就让 listen 自己报错, 不要在这里吞 */
      }
    }

    const server = net.createServer((sock) => this.handle(sock));
    server.on('error', (e) => this.opts.onError?.(e as Error));
    this.server = server;

    await new Promise<void>((resolve, reject) => {
      server.once('error', reject);
      server.listen(this.opts.socketPath, () => {
        server.removeListener('error', reject);
        resolve();
      });
    });
  }

  private handle(sock: net.Socket): void {
    this.conns.add(sock);
    const decoder = new FrameDecoder();
    /* 未 hello 之前不给任何能力 —— 缺省拒绝 */
    let bound: { pid: ProcessId; ctx: ProcessContext } | null = null;

    const reply = (r: AbiResponse): void => {
      if (!sock.destroyed) sock.write(encodeFrame(r));
    };
    const fail = (id: number, code: AbiErrorCode, message: string): void =>
      reply({ id, error: { code, message } });

    sock.on('data', (chunk: Buffer) => {
      let msgs: unknown[];
      try {
        msgs = decoder.push(chunk);
      } catch (e) {
        /* 帧坏了不试图恢复 —— 恢复就是给攻击者留缝 */
        this.opts.onError?.(e as Error);
        sock.destroy();
        return;
      }

      for (const m of msgs) {
        const req = m as AbiRequest;
        if (typeof req?.id !== 'number' || typeof req?.method !== 'string') {
          fail(0, 'bad_request', '缺 id 或 method');
          continue;
        }

        if (req.method === 'hello') {
          if (req.params?.wire !== WIRE_VERSION) {
            fail(req.id, 'wire_version', `需要线格式 v${WIRE_VERSION}`);
            sock.destroy();
            continue;
          }
          const found = this.opts.resolve(req.params.token);
          if (!found) {
            fail(req.id, 'unauthenticated', 'token 不认');
            sock.destroy();
            continue;
          }
          bound = found;
          reply({
            id: req.id,
            result: { kind: 'hello', pid: found.pid, abiVersion: found.ctx.abiVersion },
          });
          continue;
        }

        if (!bound) {
          fail(req.id, 'unauthenticated', '必须先 hello');
          sock.destroy();
          continue;
        }

        void this.dispatch(req, bound.ctx).then(
          (result) => reply({ id: req.id, result }),
          (e: Error) =>
            fail(
              req.id,
              e instanceof BudgetExceeded ? 'budget_exceeded' : 'internal',
              e.message,
            ),
        );
      }
    });

    sock.on('error', () => sock.destroy());
    sock.on('close', () => this.conns.delete(sock));
  }

  private async dispatch(req: AbiRequest, ctx: ProcessContext): Promise<AbiResult> {
    switch (req.method) {
      case 'emit':
        ctx.emit(req.params.payload);
        return { kind: 'ok' };
      case 'can':
        return { kind: 'bool', value: ctx.can(req.params.axis, req.params.scope) };
      case 'spend':
        ctx.spend(req.params.delta);
        return { kind: 'ok' };
      case 'decide': {
        /* 这个请求可能几小时后才回 —— 决策活得比任何一次运行都长.
         * 刻意不设超时: 超时由 DecisionRequest.onTimeout 表达, 不能由传输层决定. */
        const resolution = await ctx.decide(req.params.request);
        return { kind: 'decision', resolution };
      }
      case 'hello':
        return { kind: 'ok' }; /* 已在上面处理 */
      default: {
        const never: never = req;
        throw new Error(`未知方法: ${JSON.stringify(never)}`);
      }
    }
  }

  async close(): Promise<void> {
    for (const c of this.conns) c.destroy();
    this.conns.clear();
    const s = this.server;
    this.server = null;
    if (s) await new Promise<void>((r) => s.close(() => r()));
    if (existsSync(this.opts.socketPath)) {
      try {
        unlinkSync(this.opts.socketPath);
      } catch {
        /* best effort */
      }
    }
  }
}

/** 生成一次性 token. 128 位随机, 不带任何可推断信息 */
export function mintToken(): string {
  return randomBytes(16).toString('hex');
}
