/**
 * foreign-agent.mjs — 一个**不依赖 Go agent 实现**的进程.
 *
 * 它存在的唯一目的是回答一个问题:
 *
 *   **其他语言写的 agent runtime, 能不能只靠 ABI 那几个系统调用活下来?**
 *
 * 如果能, agent runtime 就能独立演进, 由 OS 统一管理资源.
 * 如果不能, 缺的那几个调用就是 ABI 真正该补的东西.
 *
 * 刻意不 import 任何项目包: 裸说线协议 (4 字节大端长度 + JSON).
 * 引入项目包只能证明库可用, 不能证明这个契约语言中立.
 *
 * 它做的事很朴素, 但每一步都压在一条系统调用上:
 *
 *   hello  → 握手, 拿到 pid 和上下文窗口
 *   emit   → 产出事件 (宿主的日志里能看到)
 *   can    → 能力预检
 *   infer  → 自己没有 key, 让 OS 代它调模型
 *   spend  → 记账
 *   decide → 越界时请求人工决策
 */

import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';

const SOCK = process.env.NEOX_ABI_SOCKET;
const TOKEN = process.env.NEOX_ABI_TOKEN;
const WORK = process.env.NEOX_WORK || process.cwd();
if (!SOCK) {
  console.error('没有 NEOX_ABI_SOCKET —— 这个进程只能被 OS 起, 不能自己跑');
  process.exit(2);
}

// ── 线协议: 4 字节大端长度 + JSON ──
class Abi {
  #sock;
  #buf = Buffer.alloc(0);
  #pending = new Map();
  #id = 1;

  async connect() {
    await new Promise((res, rej) => {
      this.#sock = net.createConnection(SOCK, res);
      this.#sock.once('error', rej);
    });
    this.#sock.on('data', (chunk) => {
      this.#buf = Buffer.concat([this.#buf, chunk]);
      for (;;) {
        if (this.#buf.length < 4) return;
        const n = this.#buf.readUInt32BE(0);
        if (this.#buf.length < 4 + n) return;
        const body = this.#buf.subarray(4, 4 + n);
        this.#buf = this.#buf.subarray(4 + n);
        const msg = JSON.parse(body.toString('utf8'));
        const p = this.#pending.get(msg.id);
        if (!p) continue;
        this.#pending.delete(msg.id);
        msg.error ? p.rej(new Error(`${msg.error.code}: ${msg.error.message}`)) : p.res(msg.result);
      }
    });
    return this.call('hello', { wire: 1, token: TOKEN });
  }

  call(method, params) {
    const id = this.#id++;
    const frame = Buffer.from(JSON.stringify({ id, method, params }), 'utf8');
    const head = Buffer.alloc(4);
    head.writeUInt32BE(frame.length, 0);
    return new Promise((res, rej) => {
      this.#pending.set(id, { res, rej });
      this.#sock.write(Buffer.concat([head, frame]));
    });
  }
}

const abi = new Abi();
const hello = await abi.connect();
const say = (payload) => abi.call('emit', { payload });

await say({ phase: 'foreign_start', runtime: `node ${process.version}`, pid: hello.pid });

// 1. 能力预检 —— 这条走 can
const canWrite = await abi.call('can', { axis: 'write', scope: 'note.md' });
await say({ phase: 'cap', axis: 'write', scope: 'note.md', allowed: canWrite.value });

// 2. 干活: 读一个文件. 这一步不走 ABI —— 文件系统由内核的规则守着,
//    进程直接读就行, 读得到说明授权生效, 读不到说明笼子在起作用.
let seen = '';
try {
  seen = fs.readFileSync(path.join(WORK, 'input.txt'), 'utf8').slice(0, 400);
  await say({ phase: 'read_ok', bytes: seen.length });
} catch (e) {
  await say({ phase: 'read_denied', err: String(e).slice(0, 120) });
}

// 3. 推理 —— 进程里没有任何 key, 由 OS 代调
const infer = await abi.call('infer', {
  system: '你在一个被内核约束的进程里。只回一句话，不要解释。',
  messages: [{ role: 'user', content: `这段文字讲的是什么？一句话概括：\n${seen || '(什么都没读到)'}` }],
  maxTokens: 200,
});
await say({
  phase: 'inferred',
  said: (infer.infer?.content || '').slice(0, 200),
  promptTokens: infer.infer?.promptTokens,
  cached: infer.infer?.cachedTokens,
});

// 4. 记账 —— 止损线由 OS 强制, 不靠进程自觉
await abi.call('spend', { delta: { tokens: 100 } });

// 5. 越界 → 进入人工决策. 受约束运行时与仅在沙盒内执行 agent 的分界线在这里:
//    做不到的事不是崩, 是问人.
const outside = '/etc/neox-foreign-probe';
const allowed = await abi.call('can', { axis: 'write', scope: outside });
if (!allowed.value) {
  // request 必须保留这一层: 摊平后 Go 侧会解出空的
  // DecisionRequest, 结果是空白审批框; OS 因而拒绝这种请求.
  const res = await abi.call('decide', {
    request: {
      urgency: 'high',
      axis: 'write',
      scope: outside,
      present: {
        kind: 'choice',
        title: `允许写 ${outside} 吗?`,
        detail: '一个 Node 进程在验证越界会不会走到你面前',
        options: [
          { id: 'yes', label: '允许' },
          { id: 'no', label: '拒绝', destructive: true },
        ],
      },
    },
  });
  await say({ phase: 'decided', choice: res.resolution?.choice, by: res.resolution?.by });
}

// 6. 就算被批准, 也照样写不进去 —— 内核规则在进程启动时定死, 只能收紧不能放宽
let escaped = false;
try {
  fs.writeFileSync(outside, 'x');
  escaped = true;
} catch {
  /* 如期被拒 */
}
await say({ phase: 'foreign_done', escaped });
process.exit(escaped ? 1 : 0);
