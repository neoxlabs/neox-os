/**
 * parties — 把进程表折成"会话列表".
 *
 *   规则只有一条: **同一个 thread 的进程是一个房间**.
 *   thread 是 OS 自己的概念(授权挂在它上面, 进程会死对话不死),
 *   界面只是把它显示出来 —— 没有另造一个"群"的数据结构.
 *
 *   一个人的 thread 不算房间: 那只是他自己的一段对话.
 */

export interface Member {
  pid: string; name: string; state: string;
  /** 它在哪儿干活 —— 来自内核发给它的写能力, 不是界面自己记的 */
  work?: string;
  /**
   * 他负责什么 —— 一句话.
   *
   *   用户的原话: "确实不同的助手太多, 我自己都分不清". 每个 bot 本来
   *   就有岗位说明, 只是界面上一个字都看不到.
   */
  role?: string;
  /** 它干活的那条 git 分支. 空 = 这个项目没走分支隔离 */
  branch?: string;
  /**
   * 项目根 —— **不是** work.
   *
   *   走分支隔离时 work 是它自己的 worktree(每人一个, 各不相同),
   *   而"这几个人在同一个项目里"只有项目根答得出.
   */
  project?: string;
}

/**
 * 从事件日志里认领**已经死掉的**进程.
 *
 *   重启之后 pid 全变了, 但历史还在日志里. 那条 created 事件带着
 *   bot/name 标签 —— 界面据此把新旧进程认成同一个人, 于是
 *   "对话跨重启存活"这件事在界面上才成立.
 *
 *   没有它的话: 历史确实落了盘、也确实补齐回来了, 但界面上一条都看不见,
 *   因为进程表里只有活着的 —— 用户看到的还是"我的对话全没了".
 */
export function membersFromHistory(store: {
  pids(): readonly string[];
  eventsOf(pid: string): readonly { kind: string; payload: unknown }[];
}): Member[] {
  const out: Member[] = [];
  for (const pid of store.pids()) {
    for (const event of store.eventsOf(pid)) {
      if (event === undefined || event.kind !== "proc.state") continue;
      const body = event.payload as { state?: string; work?: string; labels?: Record<string, string> };
      if (body.state !== "created") continue;
      const labels = body.labels ?? {};
      if (labels.bot === undefined) break;
      out.push({
        pid, name: labels.name ?? labels.bot, state: "exited", bot: labels.bot,
        ...(labels.thread === undefined ? {} : { thread: labels.thread }),
        // 死掉的进程读不到能力集了 —— 工作区从创建事件里认, 那是唯一还在的地方
        ...(body.work === undefined ? {} : { work: body.work }),
        // 分支同理: 人死了它的产物还在那条分支上, 交接要认得出
        ...(labels.branch === undefined ? {} : { branch: labels.branch }),
        ...(labels.project === undefined ? {} : { project: labels.project })
      } as Member);
      break;
    }
  }
  return out;
}

export interface Party {
  readonly id: string;
  readonly title: string;
  /** 所有 pid —— 历史挂在死掉的那些上, 读事件要用全的 */
  readonly members: readonly Member[];
  /**
   * 去重之后的**人**.
   *
   *   一个 bot 重启过 N 次就有 N 个 pid, 但他还是一个人.
   *   人数、头像、@ 候选一律用这个 —— 用 members 的话,
   *   重启一次房间就从"3 人"变成"6 人", 而且头像里同一张脸出现两次.
   */
  readonly people: readonly Member[];
  readonly isRoom: boolean;
  /** 房间取最活跃成员的状态: 只要有人在干活, 这个房间就是在干活 */
  readonly state: string;
}

const RANK: Record<string, number> = { running: 3, waiting: 2, created: 1 };

/**
 * 进程表 → 会话列表.
 *
 *   两条规则:
 *     1. 同一个 **thread** 的进程是一个房间
 *     2. 同一个 **bot** 的进程是同一个人 —— 重启之后 pid 变了, 人没变
 *
 *   第 2 条是"对话跨重启存活"在界面上的落点: 历史挂在死掉的那些 pid 下,
 *   新说的话挂在活着的那个 pid 下, 合成一条会话人才看得出连续.
 */
/**
 * 进程表 → 会话列表.
 *
 *   两步, 顺序不能反:
 *
 *   1. **先按 bot 把人拼起来** —— 一个 bot 重启过 N 次就有 N 个 pid,
 *      历史散在这些 pid 上, 但他是一个人.
 *   2. **再按这个人现在在哪儿分组** —— 归属看**活着的那个进程**的
 *      thread 标签, 不看历史上的.
 *
 *   顺序反了就会留影子: 把人拉进房间之后, 他早先那些没有 thread 的
 *   历史 pid 会自成一条单聊, 而那条单聊是死的 —— 点进去发不出话,
 *   用户看到的是"同一个人出现了两次, 其中一个坏了".
 */
export function groupParties(processes: readonly (Member & { thread?: string; bot?: string })[]): Party[] {
  // ① 按人拼
  const byBot = new Map<string, Member[]>();
  for (const process of processes) {
    const bot = (process as { bot?: string }).bot ?? process.name;
    const bucket = byBot.get(bot);
    if (bucket === undefined) byBot.set(bot, [process]);
    else bucket.push(process);
  }

  // ② 按"现在在哪儿"分组
  const rooms = new Map<string, { title: string; members: Member[]; people: Member[] }>();
  const solos: Party[] = [];
  for (const pids of byBot.values()) {
    // 代表 = 活着的那个; 都不活就取状态最好的
    const rep = pids.reduce((best, m) => (RANK[m.state] ?? 0) > (RANK[best.state] ?? 0) ? m : best, pids[0]!);
    const thread = (rep as { thread?: string }).thread;
    if (thread === undefined || thread.length === 0) {
      solos.push({
        id: (rep as { bot?: string }).bot ?? rep.name,
        title: rep.name, members: pids, people: [rep], isRoom: false, state: rep.state
      });
      continue;
    }
    const room = rooms.get(thread);
    if (room === undefined) rooms.set(thread, { title: thread, members: [...pids], people: [rep] });
    else { room.members.push(...pids); room.people.push(rep); }
  }

  const out: Party[] = [];
  for (const [thread, room] of rooms) {
    // 一个人的 thread 不是房间: 那只是他自己的一段对话
    if (room.people.length < 2) {
      const only = room.people[0]!;
      solos.push({ id: thread, title: only.name, members: room.members, people: room.people, isRoom: false, state: only.state });
      continue;
    }
    out.push({
      id: thread, title: room.title, members: room.members, people: room.people, isRoom: true,
      state: room.people.reduce((best, m) => (RANK[m.state] ?? 0) > (RANK[best] ?? 0) ? m.state : best, "idle")
    });
  }
  // 房间在前, 其余按名字 —— **顺序必须稳定**, 否则每次拉进程表侧栏就重排
  return [...out, ...solos].sort((a, b) => Number(b.isRoom) - Number(a.isRoom) || a.title.localeCompare(b.title));
}
