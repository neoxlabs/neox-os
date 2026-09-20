/**
 * 跨语言对拍 —— **TypeScript 客户端 连 Go 服务端**.
 *
 *   这是整个 Go 重写里最硬的一条验收:
 *   前面所有对拍都是"两边各自算, 结果一致"; 这一条是两边**真的通话**.
 *
 *   证明的是两个实现认同一份线格式, 而不是各自跟自己对拍.
 *   用的是生产用的 AbiClient, 不是测试专用的简化客户端.
 *
 *   Go 侧: go/cmd/abi-harness —— 起 OS + 挂着的进程 + ABI 服务,
 *          并自动解决出现的决策 (标 by=go-side).
 *   没编译出 harness 时整个 suite 跳过, 不让它变成偶发红灯.
 */

import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { spawn, spawnSync, type ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

import { AbiClient } from '../abiClient.js';
import type { ProcessContext } from '@neox-os/abi';

const GO_DIR = resolve(__dirname, '../../../../go');
const BIN = join(tmpdir(), 'neox-abi-harness');

let child: ChildProcess | null = null;
let dir = '';
let client: AbiClient | null = null;
let remote: ProcessContext | null = null;
let available = false;

beforeAll(async () => {
  if (!existsSync(GO_DIR)) return;
  const build = spawnSync('go', ['build', '-o', BIN, './cmd/abi-harness'], {
    cwd: GO_DIR,
    encoding: 'utf8',
  });
  if (build.status !== 0) return; /* 没有 Go 工具链就跳过 */

  dir = mkdtempSync(join(tmpdir(), 'nxp-'));
  const sock = join(dir, 's');
  child = spawn(BIN, [sock], { stdio: ['ignore', 'pipe', 'pipe'] });

  const line = await new Promise<string>((res, rej) => {
    let buf = '';
    const t = setTimeout(() => rej(new Error('Go harness 启动超时')), 10_000);
    child!.stdout!.on('data', (b: Buffer) => {
      buf += b.toString('utf8');
      const i = buf.indexOf('\n');
      if (i >= 0) {
        clearTimeout(t);
        res(buf.slice(0, i));
      }
    });
    child!.on('error', rej);
  });

  const info = JSON.parse(line) as { socket: string; token: string; pid: string };
  client = new AbiClient({ socketPath: info.socket, token: info.token });
  remote = await client.connect();
  available = true;
}, 30_000);

afterAll(() => {
  client?.close();
  child?.kill('SIGTERM');
  if (dir) rmSync(dir, { recursive: true, force: true });
});

describe('TS 客户端 ↔ Go 服务端', () => {
  it('对拍环境真的起来了 —— 防止整组静默跳过变成假绿', () => {
    /* 有 Go 工具链就必须真跑. 没有才允许跳过 */
    const hasGo = spawnSync('go', ['version']).status === 0;
    if (hasGo) expect(available).toBe(true);
  });

  it('hello 握手成功, 拿到 Go 侧分配的 pid', () => {
    if (!available) return;
    /* pid 是**不透明标识**, 不该断言它的格式.
     *
     * 原来断言 /^p\d+$/, 而进程号改成"整台机器唯一"之后变成了
     * p<宿主pid>.<实例序号>.<序号> —— 这条就红了.
     * 红得不对: 变的是 Go 侧的内部编号策略, 契约(pid 是个非空字符串)
     * 一个字都没变. 断言实现细节就会这样: 改对了反而红. */
    expect(remote!.pid).toBeTruthy();
    expect(typeof remote!.pid).toBe('string');
    expect(remote!.abiVersion).toBe('0.0.1-unstable');
  });

  it('假 token 被 Go 侧拒绝', async () => {
    if (!available) return;
    const bad = new AbiClient({ socketPath: join(dir, 's'), token: '假的' });
    await expect(bad.connect()).rejects.toThrow(/token 不认/);
    bad.close();
  });

  it('能力预检: Go 侧的答案跟 TS 侧规则一致', async () => {
    if (!available) return;
    /* Go harness 给的能力是 read:/in */
    expect(await client!.canAsync('read', '/in/a.txt')).toBe(true);
    expect(await client!.canAsync('write', '/in/a.txt')).toBe(false);
    /* 路径按段比而非前缀 —— 两个语言都必须这么认 */
    expect(await client!.canAsync('read', '/inx/a.txt')).toBe(false);
  });

  it('中文 payload 往返不乱码', async () => {
    if (!available) return;
    remote!.emit({ 步骤: '跨语言输出', 值: '中文与 emoji 🚀' });
    /* emit 是即发即忘, 用一次同步调用确认连接仍然健康 */
    expect(await client!.canAsync('read', '/in/ok')).toBe(true);
  });

  it('并发 20 个请求按 id 归位, 不串味', async () => {
    if (!available) return;
    const results = await Promise.all(
      Array.from({ length: 20 }, (_, i) =>
        i % 2 === 0
          ? client!.canAsync('read', '/in/x').then((v) => ['read', v] as const)
          : client!.canAsync('write', '/in/x').then((v) => ['write', v] as const),
      ),
    );
    for (const [axis, v] of results) expect(v).toBe(axis === 'read');
  });

  it('decide 跨语言往返: TS 发起, Go 侧解决, 结果带回中文', async () => {
    if (!available) return;
    const res = await remote!.decide({
      urgency: 'high',
      present: {
        kind: 'choice',
        title: '跨语言选个方向',
        options: [
          { id: 'a', label: '深挖' },
          { id: 'b', label: '铺开', destructive: true },
        ],
      },
    });
    expect(res.choice).toBe('go说好');
    expect(res.by).toBe('go-side');
    expect(res.values?.['来自']).toBe('Go');
    expect(typeof res.at).toBe('number');
  });

  it('大 payload (>64KB) 分片传输不粘不断', async () => {
    if (!available) return;
    remote!.emit({ big: 'x'.repeat(200_000) });
    /* 大帧之后连接必须仍然可用 —— 这验证解帧器没被撑乱 */
    expect(await client!.canAsync('read', '/in/after-big')).toBe(true);
  });
});
