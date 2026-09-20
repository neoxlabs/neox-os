/**
 * ABI over unix socket 验收.
 *
 *   锁的是"从同进程函数调用变成真 IPC"之后必须成立的性质:
 *     1. 线格式: 粘包/半包/坏帧
 *     2. 身份: 没 token 不给能力, 假 token 拒绝
 *     3. **按 id 归位**: 并发请求乱序回复不串味
 *     4. 长请求: decide 可以挂很久, 期间别的调用照常
 *     5. 断连: 在途请求立刻失败, 不永远挂着
 */

import { describe, it, expect, afterEach } from 'vitest';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { AbiServer, mintToken, MAX_SOCKET_PATH } from '../abiServer.js';
import { AbiClient } from '../abiClient.js';
import { NeoxOs } from '../os.js';
import { encodeFrame, FrameDecoder, type ProcessContext } from '@neox-os/abi';

const dirs: string[] = [];
function sockPath(): string {
  /* sun_path 有 104/108 字节上限 —— 路径必须短, 不能塞进深目录 */
  const d = mkdtempSync(join(tmpdir(), 'nx-'));
  dirs.push(d);
  return join(d, 's');
}
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true });
});

describe('线格式', () => {
  it('粘包: 一次收到多帧要全部解出', () => {
    const d = new FrameDecoder();
    const a = encodeFrame({ id: 1, method: 'emit', params: { payload: { n: 1 } } });
    const b = encodeFrame({ id: 2, method: 'emit', params: { payload: { n: 2 } } });
    const out = d.push(Buffer.concat([a, b]));
    expect(out).toHaveLength(2);
    expect((out[1] as { id: number }).id).toBe(2);
  });

  it('半包: 分片到达要缓冲到完整才吐', () => {
    const d = new FrameDecoder();
    const f = encodeFrame({ id: 7, method: 'emit', params: { payload: 'x'.repeat(500) } });
    expect(d.push(f.subarray(0, 3))).toHaveLength(0); /* 连长度都没凑齐 */
    expect(d.push(f.subarray(3, 100))).toHaveLength(0);
    const out = d.push(f.subarray(100));
    expect(out).toHaveLength(1);
    expect((out[0] as { id: number }).id).toBe(7);
  });

  it('坏长度直接报错, 不试图恢复', () => {
    const d = new FrameDecoder();
    const bad = Buffer.alloc(8);
    bad.writeUInt32BE(0xffffffff, 0);
    expect(() => d.push(bad)).toThrow(/exceeds max/);
  });

  it('socket 路径过长要提前炸, 不留给 bind 报天书', () => {
    expect(
      () => new AbiServer({ socketPath: '/tmp/' + 'x'.repeat(MAX_SOCKET_PATH), resolve: () => null }),
    ).toThrow(/路径过长/);
  });
});

describe('身份与隔离', () => {
  it('假 token 被拒, 且连接被断开', async () => {
    const p = sockPath();
    const srv = new AbiServer({ socketPath: p, resolve: () => null });
    await srv.listen();
    const c = new AbiClient({ socketPath: p, token: '假的' });
    await expect(c.connect()).rejects.toThrow(/token 不认/);
    c.close();
    await srv.close();
  });

  it('没 hello 就调方法 → unauthenticated', async () => {
    const p = sockPath();
    const srv = new AbiServer({ socketPath: p, resolve: () => null });
    await srv.listen();

    const net = await import('node:net');
    const sock = net.createConnection(p);
    await new Promise((r) => sock.once('connect', r));
    const d = new FrameDecoder();
    const got = new Promise<any>((r) => sock.on('data', (b: Buffer) => r(d.push(b)[0])));
    sock.write(encodeFrame({ id: 1, method: 'emit', params: { payload: 1 } }));
    const res = await got;
    expect(res.error.code).toBe('unauthenticated');
    sock.destroy();
    await srv.close();
  });
});

describe('端到端 · 进程通过 socket 用 ABI', () => {
  /** 起一个 OS + ABI server, 把某个 inproc 进程的 ctx 暴露出去 */
  async function harness() {
    const os = new NeoxOs({ mode: 'dev' });
    const p = sockPath();
    const token = mintToken();
    let ctx!: ProcessContext;
    let ready!: () => void;
    const started = new Promise<void>((r) => (ready = r));

    const pid = os.proc.spawn({ app: 'demo', caps: [{ axis: 'read', scope: '/in' }] }, {
      kind: 'inproc',
      entry: async (c) => {
        ctx = c;
        ready();
        /* 进程本体挂着不退, 让外部客户端代表它调 ABI */
        await new Promise((r) => setTimeout(r, 3000));
        return 'done';
      },
    });
    await started;

    const srv = new AbiServer({
      socketPath: p,
      resolve: (t) => (t === token ? { pid, ctx } : null),
    });
    await srv.listen();
    const client = new AbiClient({ socketPath: p, token });
    const remote = await client.connect();
    return { os, srv, client, remote, pid };
  }

  it('emit / can / spend 跨进程可用, 且 pid 对得上', async () => {
    const h = await harness();
    expect(h.remote.pid).toBe(h.pid);

    h.remote.emit({ step: '跨进程输出' });
    await new Promise((r) => setTimeout(r, 30));

    expect(await h.client.canAsync('read', '/in/a.txt')).toBe(true);
    expect(await h.client.canAsync('write', '/in/a.txt')).toBe(false);

    const kinds = h.os.log.replay(h.pid).map((e) => e.kind);
    expect(kinds).toContain('proc.output');
    expect(kinds).toContain('cap.used');
    expect(kinds).toContain('cap.denied');

    h.client.close();
    await h.srv.close();
    h.os.shutdown();
  });

  it('并发请求按 id 归位 —— 回复乱序也不串味', async () => {
    const h = await harness();
    /* 同时发 20 个 can, 一半应为 true 一半为 false. 若按到达顺序归位必然错乱 */
    const results = await Promise.all(
      Array.from({ length: 20 }, (_, i) =>
        i % 2 === 0
          ? h.client.canAsync('read', '/in/ok').then((v) => ['read', v] as const)
          : h.client.canAsync('write', '/in/ok').then((v) => ['write', v] as const),
      ),
    );
    for (const [axis, v] of results) expect(v).toBe(axis === 'read');

    h.client.close();
    await h.srv.close();
    h.os.shutdown();
  });

  it('decide 挂很久, 期间别的调用照常; 解决后才回', async () => {
    const h = await harness();
    let resolved = false;
    const pending = h.remote
      .decide({ present: { kind: 'approve', title: '要继续吗' } })
      .then((r) => {
        resolved = true;
        return r;
      });

    await new Promise((r) => setTimeout(r, 40));
    /* decide 还挂着, 但别的调用不受影响 —— 单连接上不能被长请求堵死 */
    expect(resolved).toBe(false);
    expect(await h.client.canAsync('read', '/in/x')).toBe(true);

    const d = h.os.decide.pending()[0]!;
    h.os.decide.resolve(d.did, 'yes', 'phone:测试');
    const r = await pending;
    expect(r.choice).toBe('yes');
    expect(r.by).toBe('phone:测试');

    h.client.close();
    await h.srv.close();
    h.os.shutdown();
  });

  it('OS 侧断开 → 在途请求立刻失败, 不永远挂着', async () => {
    const h = await harness();
    const inflight = h.remote.decide({ present: { kind: 'approve', title: '会断' } });
    await new Promise((r) => setTimeout(r, 20));
    await h.srv.close(); /* 模拟 OS 崩了 */
    await expect(inflight).rejects.toThrow(/连接断开/);
    /* 进程侧能通过 signal 感知, 从而自己决定收尾 */
    expect(h.remote.signal.aborted).toBe(true);
    h.client.close();
    h.os.shutdown();
  });
});
