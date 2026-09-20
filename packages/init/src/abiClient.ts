/**
 * abiClient — 进程侧的 ABI 客户端.
 *
 *   实现的是同一个 ProcessContext 接口, 所以用户态程序不知道自己
 *   是同堆闭包还是跨进程 —— 这正是 ABI 存在的意义.
 *
 *   将来 Go 版系统层落地时, 这个文件会有一个 Go 对照实现;
 *   两边认同一份 wire.ts. 那时这个文件就是"参考实现".
 */

import * as net from 'node:net';

import type {
  AbiRequest,
  AbiResponse,
  BudgetSpent,
  CapabilityAxis,
  DecisionRequest,
  DecisionResolution,
  ProcessContext,
} from '@neox-os/abi';
import { encodeFrame, FrameDecoder, WIRE_VERSION, BudgetExceeded } from '@neox-os/abi';

interface Pending {
  resolve: (r: unknown) => void;
  reject: (e: Error) => void;
}

export interface AbiClientOptions {
  socketPath: string;
  token: string;
}

export class AbiClient {
  private sock: net.Socket | null = null;
  private readonly pending = new Map<number, Pending>();
  private nextId = 1;
  private pid = '';
  private abiVersion = '';
  private readonly abort = new AbortController();

  constructor(private readonly opts: AbiClientOptions) {}

  async connect(): Promise<ProcessContext> {
    const sock = net.createConnection(this.opts.socketPath);
    this.sock = sock;
    const decoder = new FrameDecoder();

    await new Promise<void>((resolve, reject) => {
      sock.once('connect', resolve);
      sock.once('error', reject);
    });
    sock.removeAllListeners('error');

    sock.on('data', (chunk: Buffer) => {
      let msgs: unknown[];
      try {
        msgs = decoder.push(chunk);
      } catch (e) {
        this.failAll(e as Error);
        sock.destroy();
        return;
      }
      for (const m of msgs) {
        const res = m as AbiResponse;
        /* **按 id 归位**, 不按到达顺序 —— 并发请求的回复允许乱序 */
        const p = this.pending.get(res.id);
        if (!p) continue; /* 迟到的回复 (已超时/已断连) 直接丢, 不猜归属 */
        this.pending.delete(res.id);
        if ('error' in res) {
          const err =
            res.error.code === 'budget_exceeded'
              ? new BudgetExceeded('tokens')
              : Object.assign(new Error(res.error.message), { code: res.error.code });
          p.reject(err);
        } else {
          p.resolve(res.result);
        }
      }
    });

    /* 连接断了 = OS 没了. 所有在途请求必须立刻失败, 不能永远挂着 */
    const die = (): void => {
      this.abort.abort(new Error('ABI 连接断开'));
      this.failAll(new Error('ABI 连接断开'));
    };
    sock.on('close', die);
    sock.on('error', die);

    const hello = (await this.call({
      id: 0,
      method: 'hello',
      params: { token: this.opts.token, wire: WIRE_VERSION },
    })) as { kind: 'hello'; pid: string; abiVersion: string };
    this.pid = hello.pid;
    this.abiVersion = hello.abiVersion;

    return this.context();
  }

  private failAll(e: Error): void {
    for (const p of this.pending.values()) p.reject(e);
    this.pending.clear();
  }

  private call(req: Omit<AbiRequest, 'id'> & { id?: number }): Promise<unknown> {
    const sock = this.sock;
    if (!sock || sock.destroyed) return Promise.reject(new Error('ABI 未连接'));
    const id = this.nextId++;
    const msg = { ...req, id } as AbiRequest;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      sock.write(encodeFrame(msg), (e) => {
        if (e) {
          this.pending.delete(id);
          reject(e);
        }
      });
    });
  }

  private context(): ProcessContext {
    const self = this;
    return {
      get pid() {
        return self.pid;
      },
      get abiVersion() {
        return self.abiVersion;
      },
      get signal() {
        return self.abort.signal;
      },
      emit(payload: unknown): void {
        /* emit 是即发即忘 —— 等一个 ack 会把每次输出变成一次往返.
         * 失败也不抛: 输出丢了是可观测性问题, 不该打断进程逻辑. */
        void self.call({ method: 'emit', params: { payload } }).catch(() => {});
      },
      decide(request: DecisionRequest): Promise<DecisionResolution> {
        return self
          .call({ method: 'decide', params: { request } })
          .then((r) => (r as { resolution: DecisionResolution }).resolution);
      },
      can(_axis: CapabilityAxis, _scope: string): boolean {
        /* 同步接口 + 跨进程调用天生冲突.
         * 不做假的同步 (那要么阻塞事件循环, 要么撒谎), 而是明确不支持:
         * 跨进程侧用 canAsync(). 真正的强制在内核, 预检只是省一次 EACCES. */
        throw new Error('跨进程侧请用 canAsync(); can() 只在同堆模式可用');
      },
      spend(delta: Partial<BudgetSpent>): void {
        void self.call({ method: 'spend', params: { delta } }).catch(() => {});
      },
    };
  }

  /** 跨进程版的能力预检 */
  canAsync(axis: CapabilityAxis, scope: string): Promise<boolean> {
    return this.call({ method: 'can', params: { axis, scope } }).then(
      (r) => (r as { value: boolean }).value,
    );
  }

  close(): void {
    this.sock?.destroy();
    this.sock = null;
  }
}
