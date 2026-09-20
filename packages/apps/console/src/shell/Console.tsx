/**
 * Console — NeoxOS 的对话客户端.
 *
 *   形态是对话客户端: 左边会话列表(房间 + 单聊), 中间对话, 底下输入框.
 *   对面是**活着的进程**: 关掉窗口它照跑, 再打开靠每个 pid 各自的
 *   highWater 补齐, 中间不许有断层.
 *
 *   界面**没有特权**: 只能订阅事件流、发送消息和提交决策,
 *   不能直接控制进程或绕过事件流.
 */

import { t, tt } from "../i18n/index.js";
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { EventStore } from "../os/store.js";
import { MockSource } from "../os/source-mock.js";
import { LiveSource, type LiveProcess, type LiveState, type OsHealth } from "../os/source-live.js";
import { RowProjector } from "../view/rows.js";
import { Timeline } from "../view/Timeline.js";
import { BusyStrip } from "../view/BusyStrip.js";
import { waitingHint } from "../view/waiting.js";
import { historyLines } from "./history.js";
import { roomLabel } from "./roomname.js";
import { renderWidget, type WidgetContext } from "../view/widgets.js";
import { startFrameMeter, type FrameStats } from "../perf/frames.js";
import { Composer } from "./Composer.js";
import { Lightbox } from "../view/Lightbox.js";
import type { Shot } from "./shots.js";
import { shotsFrom } from "./blobs.js";
import { presenceOf } from "../view/presence.js";
import { randomBotName } from "./botname.js";
import { pickTargets } from "./routing.js";
import { byProject } from "./grouping.js";
import { plainLine } from "./plainline.js";
import { Avatar, GroupAvatar } from "../view/Avatar.js";
import { Profile, type ProfileTarget } from "./Profile.js";
import { Room, type RoomTarget } from "./Room.js";
import { Project } from "./Project.js";
import type { ProjectReport } from "../os/source-live.js";
import { groupParties, membersFromHistory, type Party } from "./parties.js";
import { loadSeenAt, markSeen, seedSeen, unreadOf, type SeenAt } from "./unread.js";
import { askPermission, notify } from "./notify.js";
import { withRoomContext } from "./roomContext.js";
import { EVERYONE_NAME, mentionChoices } from "./mentions.js";
import { ROOMMATE_HINT, roommateNote } from "./roommate.js";
import { dominantWork } from "./workspace.js";
import { resolveEndpoint, forgetEndpoint } from "./endpoint.js";
import { Gate } from "./Gate.js";
import { Checkup, checkupSeen, markCheckupSeen, fetchToolchain } from "./Checkup.js";
import { Settings, type ThemeChoice, type Me } from "./Settings.js";
import { NewBot } from "./NewBot.js";
import wordmarkWhite from "../assets/wordmark-white.png";
import wordmarkDark from "../assets/wordmark-dark.png";
import neoxMark from "../assets/neox-mark.svg";

/** 浏览器模式左上角没有 macOS 红绿灯 —— 那 78px 的留白改由环形 mark 填 */
const desktopShell = typeof window !== "undefined" && window.neoxos !== undefined;

export function Console() {
  const store = useMemo(() => new EventStore(), []);
  const endpoint = useMemo(() => resolveEndpoint(), []);
  /** 图从哪儿取 —— 合成源那条路没有图 */
  const shots = useMemo(() => shotsFrom(endpoint), [endpoint]);
  const [processes, setProcesses] = useState<LiveProcess[]>([]);
  const [link, setLink] = useState<LiveState>(endpoint === null ? "live" : "connecting");
  /**
   * locked — 这一页对着一台锁着的 OS, 手里却没有(对的)钥匙.
   *
   *	两种进法都落到这儿: ① 裸地址进来(Docker Desktop 里点端口链接)没带
   *	token; ② 浏览器记住的 token 已经过期(容器重建 = 新 token). 之前的
   *	下场是红点 + 空侧栏, 页面不解释任何事 —— 现在把门画出来(Gate).
   *	判据是 /processes 回 401: vite dev 的合成源那条路上没有这个接口,
   *	探测静默失败, 不打扰开发.
   */
  const [locked, setLocked] = useState(false);
  /**
   * checkup — 首启的一次环境自检(见 Checkup.tsx).
   *
   *	**静默地查, 缺了才拦路**: 镜像形态自带整套工具链, 那儿永远是满的,
   *	给这些人弹一张"齐了"的卡是纯噪音. 真正需要被拦一下的是桌面客户端
   *	的本机模式 —— 一台干净的 Mac 上连 git 都可能没有.
   *
   *	查不通(合成源、老版本 OS 没这个口)就当没这回事, 不打扰.
   */
  const [checkupOpen, setCheckupOpen] = useState(false);
  useEffect(() => {
    if (desktopShell) return;
    void fetch(endpoint === null ? "/processes" : `${endpoint.url}/processes`,
      endpoint === null ? {} : { headers: { Authorization: `Bearer ${endpoint.token}` } })
      .then((got) => {
        if (got.status === 401) { forgetEndpoint(); setLocked(true); }
      })
      .catch(() => { /* 够不着 = 不是这扇门的事 */ });
  }, [endpoint]);

  useEffect(() => {
    if (locked || checkupSeen()) return;
    let alive = true;
    void fetchToolchain(endpoint).then((got) => {
      if (!alive || got === null) return;
      // 一件不缺就别弹, 直接记成看过 —— 下次开机也不再问
      if (got.tools.every((tool) => tool.have)) { markCheckupSeen(); return; }
      setCheckupOpen(true);
    });
    return () => { alive = false; };
  }, [endpoint, locked]);
  /**
   * 选中的会话**记住**: 重启回来该还在昨晚待的那间屋里, 不是回到第一个.
   *
   *	存的是 id(bot 名/房间名), 不是下标 —— 列表顺序会变.
   *	启动时那个 id 可能还没到(SSE 分批装回), 所以先记为"想去的地方",
   *	parties 里一出现就选中它(见下面那个 effect).
   */
  const [partyId, setPartyIdRaw] = useState<string | null>(() => {
    try { return localStorage.getItem("neoxos.party"); } catch { return null; }
  });
  const setPartyId = useCallback((id: string | null) => {
    setPartyIdRaw(id);
    try {
      if (id === null) localStorage.removeItem("neoxos.party");
      else localStorage.setItem("neoxos.party", id);
    } catch { /* 隐私模式存不了 —— 这次能用就行 */ }
  }, []);
  const [stats, setStats] = useState<FrameStats>({ fps: 0, p95: 0, worst: 0, longFrames: 0, samples: 0 });
  const [metrics, setMetrics] = useState({ rendered: 0, total: 0 });
  const [answers, setAnswers] = useState<ReadonlyMap<string, string>>(() => new Map());
  const [devOpen, setDevOpen] = useState(false);
  /** 正在确认删除的那条会话 —— **删是不可撤销的, 必须先问一次** */
  /**
   * 正在确认删掉哪段对话 —— **存 id, 不存那个 Party 对象**.
   *
   *   删掉是按 **pid 列表**发的. 存对象等于把那份名单也冻住: 确认框开着
   *   的这几秒里 bot 换个房间/换个工作区就会重起(pid 变了), 或者被 recruit
   *   拉进新人 —— 新 pid 不在名单里, 于是**删一半**.
   *
   *   而删一半比删不掉更坏: 账本里剩着几条, 侧栏那条会话过一会儿自己
   *   长回来, 用户以为删掉了. OnForget 的注释里写着同一句: 少删一个就是假删.
   */
  const [confirmDrop, setConfirmDrop] = useState<string | null>(null);
  const [whoName, setWhoName] = useState<string | null>(null);
  /** 点的是哪个头像 —— 资料卡贴着它弹, 而不是永远弹在屏幕正中 */
  const [whoAt, setWhoAt] = useState<DOMRect | null>(null);
  /**
   * **一次只开一张卡**: 资料卡和群卡各自铺着一层"接住外面点击"的透明层,
   *   两张同时开着的话, 上面那层挡住下面那层, 点空白就关不掉了 ——
   *   这个毛病之前在别处犯过一次.
   */
  const showWho = useCallback((name: string, anchor: DOMRect) => {
    setRoomOpen(null); setRoomAt(null); setProjectOn(null);
    setWhoName(name); setWhoAt(anchor);
  }, []);
  const hideWho = useCallback(() => { setWhoName(null); setWhoAt(null); }, []);
  /** 点了群头像/那三个点 —— 群卡贴着它弹, 跟资料卡同一套规矩 */
  /** 点开的那张大图. null = 没开 */
  const [bigShot, setBigShot] = useState<{ src: string; alt: string } | null>(null);
  const [roomAt, setRoomAt] = useState<DOMRect | null>(null);
  const [roomOpen, setRoomOpen] = useState<string | null>(null);
  /**
   * 点了侧栏那个项目名 —— **项目才是干活的单位**.
   *
   *   一次只开一张卡, 跟资料卡/群卡同一条规矩: 两层"接住外面点击"的
   *   透明层叠着的话, 点空白就关不掉了.
   */
  const [projectAt, setProjectAt] = useState<DOMRect | null>(null);
  const [projectOn, setProjectOn] = useState<{ name: string; path: string } | null>(null);
  const [projectSaw, setProjectSaw] = useState<ProjectReport | null | undefined>(undefined);
  const showProject = useCallback((name: string, path: string, at: DOMRect) => {
    setWhoName(null); setWhoAt(null); setRoomOpen(null); setRoomAt(null);
    setProjectOn({ name, path }); setProjectAt(at); setProjectSaw(undefined);
  }, []);
  const hideProject = useCallback(() => { setProjectOn(null); setProjectAt(null); }, []);

  const showRoom = useCallback((title: string, at: DOMRect) => {
    setProjectOn(null);
    setWhoName(null); setWhoAt(null);
    setRoomOpen(title); setRoomAt(at);
  }, []);
  const hideRoom = useCallback(() => { setRoomOpen(null); setRoomAt(null); }, []);
  /** 正在往哪个房间拉人 */
  /**
   * 正在给哪个房间拉人 —— **存 id, 不存那个 Party 对象**.
   *
   *   存对象就是存一张快照: 对话框开着的时候有人被拉进来/请出去,
   *   它照样显示旧的那份, 而里面的按钮还照着旧名字动作.
   *   否则房间头上写着 3 人, 对话框里"现在房间里"可能只有 1 个.
   */
  const [inviting, setInviting] = useState<string | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  /** 从"去填一把 key"进来的, 要直接停在推理服务那一页 */
  // 缺省停在"连接" —— 这台客户端连的是谁, 是所有别的设置的前提
  const [settingsPage, setSettingsPage] = useState<"connect" | "me" | "provider">("connect");
  /**
   * 这台机器有没有推理服务.
   *
   *   **没有 key 这件事要在派活之前就说**. 原来只有 bot 自己会说 ——
   *   而那句"这台机器没有配置推理服务"出现在你交代完一件事、等了几秒
   *   之后, 藏在对话里, 还带着"×3"(重试了三次). 用户看到的是"它坏了",
   *   而不是"我少配了一样东西".
   *
   *   undefined = 还没问出来(别闪一条假警报); false = 真没有.
   */
  const [hasKey, setHasKey] = useState<boolean | undefined>(undefined);
  const [newBotOpen, setNewBotOpen] = useState(false);
  /** 新建对话框以什么身份打开: 预填的工作区 + 是不是"新建项目" */
  const [newBotWork, setNewBotWork] = useState("");
  const [newBotAsProject, setNewBotAsProject] = useState(false);
  /**
   * 侧栏右键菜单: 在哪儿(光标处)、对着哪段会话.
   *
   *	存 id 不存对象 —— 菜单开着时世界还在动, 快照会说谎(同 inviting).
   */
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number; id: string } | null>(null);
  useEffect(() => {
    if (ctxMenu === null) return;
    const away = () => setCtxMenu(null);
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") setCtxMenu(null); };
    document.addEventListener("mousedown", away);
    window.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("mousedown", away); window.removeEventListener("keydown", onKey); };
  }, [ctxMenu]);
  /** 顶部那个 + 的菜单 */
  const [topPlusOpen, setTopPlusOpen] = useState(false);
  const topPlusRef = useRef<HTMLSpanElement | null>(null);
  useEffect(() => {
    if (!topPlusOpen) return;
    const away = (event: MouseEvent) => {
      if (topPlusRef.current !== null && !topPlusRef.current.contains(event.target as Node)) setTopPlusOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [topPlusOpen]);
  /**
   * 项目显示名: path → 人起的名字. 真相源在 OS 侧(projects.json) ——
   * 目录名是磁盘上的事实, 显示名是人看的, 两边谁都不骗谁.
   */
  const [projNames, setProjNames] = useState<Record<string, string>>({});
  /** 群主: 房间名 → 开房的那个 bot */
  const [owners, setOwners] = useState<Record<string, string>>({});
  const [theme, setTheme] = useState<ThemeChoice>("system");
  /** 我是谁 —— 存本地. 它只影响称呼, 不是身份凭据 */
  const [me, setMe] = useState<Me>(() => {
    try { return { name: localStorage.getItem("neoxos.me.name") ?? t("我") }; }
    catch { return { name: t("我") }; }
  });
  useEffect(() => { try { localStorage.setItem("neoxos.me.name", me.name); } catch { /* 无痕窗口写不了, 不影响用 */ } }, [me]);

  /**
   * 视图: 平的还是按项目分堆.
   *
   *   **记住用户选的那一档**: 每次开机都退回默认, 等于这个开关不存在 ——
   *   他会重新点一遍, 点到第三次就不再用它了.
   */
  const [grouped, setGrouped] = useState<boolean>(() => {
    try { return localStorage.getItem("neoxos.view") === "project"; } catch { return false; }
  });
  useEffect(() => {
    try { localStorage.setItem("neoxos.view", grouped ? "project" : "flat"); } catch { /* 无痕窗口 */ }
  }, [grouped]);
  const [health, setHealth] = useState<OsHealth>({ inference: false });
  const [seen, setSeen] = useState<SeenAt>(() => loadSeenAt());
  const focused = useRef(true);

  // 主题落到 <html data-theme>. 跟随系统时**不设属性**, 让 CSS 的
  // prefers-color-scheme 自己判 —— 在 render 里读系统色再写死是快照,
  // 用户中途切换系统主题就陈旧了
  useEffect(() => {
    const root = document.documentElement;
    if (theme === "system") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", theme);
  }, [theme]);

  const live = useMemo(() => endpoint === null ? null : new LiveSource({
    baseUrl: endpoint.url, token: endpoint.token, store,
    onState: setLink,
    onProcesses: (list) => setProcesses([...list]),
    onHealth: setHealth
  }), [endpoint, store]);
  const mock = useMemo(() => endpoint !== null ? null : new MockSource((events) => store.append(events)), [endpoint, store]);

  useEffect(() => () => live?.close(), [live]);
  // 项目显示名/群主跟着连接走: 换一台 OS, 这些是那台的
  useEffect(() => {
    if (live === null) return;
    void live.projectNames().then(setProjNames);
    void live.roomOwners().then(setOwners);
  }, [live]);
  // 房间是 bot 拉人时当场开的 —— 卡一打开就重问一遍, 别用启动时那份旧的
  useEffect(() => {
    if (live === null || roomOpen === null) return;
    void live.roomOwners().then(setOwners);
  }, [live, roomOpen]);
  useEffect(() => {
    if (mock === null) return;
    mock.seed();
    setProcesses(mock.bots.map((b) => ({ pid: b.pid, name: b.name, state: b.state })));
  }, [mock]);
  useEffect(() => startFrameMeter(setStats), []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.altKey && e.key.toLowerCase() === "d") { e.preventDefault(); setDevOpen((v) => !v); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const version = useSyncExternalStore((l) => store.subscribe(l), () => store.version);

  useEffect(() => { void askPermission(); }, []);
  useEffect(() => {
    const on = () => { focused.current = true; };
    const off = () => { focused.current = false; };
    window.addEventListener("focus", on);
    window.addEventListener("blur", off);
    return () => { window.removeEventListener("focus", on); window.removeEventListener("blur", off); };
  }, []);

  /**
   * 会话 = 活着的进程 **∪** 日志里出现过的进程.
   *
   *   同一个 bot 标签的新旧进程合成一条会话 —— 重启之后 pid 变了,
   *   但对话不该跟着变.
   */
  const parties = useMemo(() => {
    /**
     * **状态以事件流为准, 轮询只当兜底**.
     *
     *	活进程列表 3 秒轮询一次, 而换房间/搬工作区/交办成组都是
     *	"杀旧进程、起新进程" —— 旧 pid 立刻记成不在, 新 pid 要等下一次
     *	轮询才露面. 这个窗口里, 明明在干活的人显示"不存在"
     *	(用户原话: "这种 bug 是不允许的").
     *	SSE 那条 proc.state 是**实时**到的, store 里全有 —— 用它.
     */
    const stateOf = (pid: string): string | null => {
      const evs = store.eventsOf(pid);
      for (let i = evs.length - 1; i >= 0; i -= 1) {
        const e = evs[i];
        if (e !== undefined && e.kind === "proc.state") {
          return (e.payload as { state?: string }).state ?? null;
        }
      }
      return null;
    };
    const fresh = <T extends { pid: string; state: string }>(m: T): T => ({ ...m, state: stateOf(m.pid) ?? m.state });

    const historical = membersFromHistory(store).map(fresh);
    const live = new Map(processes.map((p) => [p.pid, p]));
    // 死的在前、活的在后 —— 合并时后来的覆盖显示名, 于是改过名字能跟上
    return groupParties([...historical.filter((h) => !live.has(h.pid)), ...processes.map(fresh)]);
  }, [processes, store, version]);

  // 第一次见到的会话全记成已读 —— 补齐历史不该炸出一屏红点
  useEffect(() => { setSeen((current) => seedSeen(parties, store, current)); }, [parties, store]);
  useEffect(() => {
    if (parties.length === 0) return;
    if (partyId === null) { setPartyId(parties[0]!.id); return; }
    if (parties.some((p) => p.id === partyId)) return;
    // 记住的那个还没装回来(历史分批到) —— 等一等, 别急着跳去第一个
    // 把存的位置冲掉; 等了 3 秒还没有, 那多半是被删了, 再退
    const timer = setTimeout(() => setPartyId(parties[0]!.id), 3000);
    return () => clearTimeout(timer);
  }, [parties, partyId, setPartyId]);
  const party = parties.find((p) => p.id === partyId) ?? null;
  // **现查, 不吃快照**: 对话框开着的时候有人进出, 它得跟着变 —— 见 inviting
  const invitingRoom = inviting === null ? null : parties.find((p) => p.id === inviting) ?? null;
  const dropping = confirmDrop === null ? null : parties.find((p) => p.id === confirmDrop) ?? null;

  /** 一个会话一个投影器: 切走再切回来, 行还是原来那些, 不重算 */
  const projectors = useRef(new Map<string, RowProjector>());
  const projectorRef = useRef<RowProjector | null>(null);
  let projector: RowProjector | null = null;
  if (party !== null) {
    projector = projectors.current.get(party.id) ?? null;
    if (projector === null) {
      // 房间要标名字, 一对一不标 —— 对面只有一个人时名字是废话
      projector = new RowProjector(party.id, party.people[0]?.name ?? "bot", party.isRoom);
      projectors.current.set(party.id, projector);
    }
    // 房间身份是**后来才确定的**(成员从 SSE 增量到达), 所以每次都跟一下
    projector.setShowNames(party.isRoom);
    // **按时间归并**, 不是一个人一个人地灌 —— 见 consumeMerged.
    // member.work 一起传下去: 工具行里工作区内的路径写成相对的(inWorkspace)
    projector.consumeMerged(party.members.map((member) => ({
      pid: member.pid, speaker: member.name,
      events: store.eventsOf(member.pid), ...(member.work === undefined ? {} : { root: member.work })
    })));
  }
  projectorRef.current = projector;

  /**
   * 已读只在**窗口在前台且这个会话开着**的时候推进.
   * 少了"前台"这一条, 你切到别的应用之后新来的消息会被静默标成已读.
   */
  useEffect(() => {
    if (party === null || !focused.current) return;
    setSeen((current) => markSeen(party, store, current));
  }, [party, store, version]);

  /** 通知: 有人等着决策时一定推; 不在前台的新消息按会话限流 */
  const notified = useRef(new Set<string>());
  useEffect(() => {
    for (const entry of parties) {
      const waiting = entry.members.find((m) => m.state === "waiting");
      const isOpen = entry.id === partyId && focused.current;
      if (waiting !== undefined && !isOpen) {
        const key = `${entry.id}:waiting:${waiting.pid}`;
        if (notified.current.has(key)) continue;
        notified.current.add(key);
        notify({
          key, urgent: true,
          title: tt("{a} 在等你拍板", { a: entry.title }),
          body: store.preview(waiting.pid) || t("有个决定要你点一下"),
          onClick: () => setPartyId(entry.id)
        });
        continue;
      }
      if (waiting === undefined) notified.current.delete(`${entry.id}:waiting:${entry.members[0]?.pid ?? ""}`);
      /**
       * 合进主干了 —— **不在这间屋子里也要知道**.
       *
       *   它改的是所有人共用的那一份代码. 你可能正在别的会话里, 也可能
       *   不在电脑前 —— 而这条埋在那个 bot 的工具结果里, 点进去才看得见.
       *   所以它不受"这间屋子开着就不提醒"那条限制.
       */
      for (const member of entry.members) {
        const merged = store.lastMerged(member.pid);
        if (merged === null) continue;
        const key = `${member.pid}:merged:${merged.seq}`;
        if (notified.current.has(key)) continue;
        notified.current.add(key);
        // 这一屏正开着这间屋子、人也在, 那他已经看见了 —— 不用再响一次
        if (isOpen) continue;
        notify({
          key, urgent: false,
          title: tt("{a} 合进主干了", { a: entry.title }),
          body: merged.text,
          onClick: () => setPartyId(entry.id)
        });
      }
      if (isOpen || focused.current) continue;
      if (unreadOf(entry, store, seen)) {
        notify({
          key: entry.id, urgent: false,
          title: entry.title,
          body: store.preview(entry.members[0]!.pid) || t("有新动静"),
          onClick: () => setPartyId(entry.id)
        });
      }
    }
  }, [parties, partyId, seen, store, version]);

  const widgetContext: WidgetContext = useMemo(() => ({
    ...(shots === null ? {} : { shotSrc: shots }),
    onOpenImage: (src: string, alt: string) => setBigShot({ src, alt }),
    respond(widgetId, answer) {
      setAnswers((current) => new Map(current).set(widgetId, answer));
      if (live !== null) {
        void live.decide(widgetId, answer).then((failure) => {
          // OS 说"这条已经处理过了" —— 必须说出来, 不能静默吞掉
          if (failure !== null) setAnswers((current) => new Map(current).set(widgetId, "__gone__"));
        });
      }
      else if (mock !== null && party !== null) mock.resolve(party.members[0]!.pid, widgetId, answer);
    },
    /** 先问日志(真相), 再问本地(那两百毫秒的乐观回显) */
    answerOf(widgetId) { return projectorRef.current?.resolutions.get(widgetId) ?? answers.get(widgetId); }
  }), [answers, live, mock, party, shots]);

  /** 活着的 pid —— 判一张卡还有没有人在等 */
  const livePids = useMemo(
    () => new Set(processes.filter((p) => LIVE.has(p.state)).map((p) => p.pid)),
    [processes]
  );

  const renderRowWidget = useCallback(
    (row: { widget?: Parameters<typeof renderWidget>[0]; pid?: string }) => renderWidget(row.widget, {
      ...widgetContext,
      stale: row.pid !== undefined && !livePids.has(row.pid)
    }),
    [widgetContext, livePids]
  );

  /** 点头像看的是**这个人**, 不是某个进程 —— 所以按名字找, 跨重启合并 */
  const profile: ProfileTarget | null = useMemo(() => {
    if (whoName === null) return null;
    const mine = parties.flatMap((p) => p.members).filter((m) => m.name === whoName);
    if (mine.length === 0) return null;
    const live = mine.find((m) => LIVE.has(m.state));
    const events = mine.reduce((sum, m) => sum + store.highWater(m.pid), 0);
    const rooms = parties.filter((p) => p.isRoom && p.people.some((m) => m.name === whoName)).map((p) => p.title);
    const member = live ?? mine[0]!;
    return {
      member, events, rooms,
      // 他起过的进程都算 —— "起过几次"是排查时第一个要问的.
      // **去重**: 同一个 pid 会同时挂在房间和单聊两条会话下, 直接数就翻倍
      pids: [...new Set(mine.map((m) => m.pid))],
      canOpen: parties.some((p) => !p.isRoom && p.title === whoName),
      // 正在干活才给那颗"停" —— 一颗按不动的按钮比没有更糟
      busy: mine.some((m) => m.state === "running"),
      // 他的哪一个进程都算 —— 决策挂在下的那个进程上, 而它可能已经不是活着的那个
      // 同上: 只认活着那个进程上的 —— 死进程上的卡片按了也没人接
      pending: mine.some((m) => LIVE.has(m.state) && store.pendingDecision(m.pid)),
    };
  }, [whoName, parties, store, version]);

  /** 点群头像看的是**这个屋子**: 人、这摊活在哪儿、各自压着哪条分支 */
  const roomTarget: RoomTarget | null = useMemo(() => {
    if (roomOpen === null) return null;
    const room = parties.find((p) => p.isRoom && p.title === roomOpen);
    if (room === undefined) return null;
    return {
      title: room.title,
      people: room.people,
      // 历史挂在**所有**进程上, 包括死掉的 —— 只取活着的那些, 一重启
      // 屋里之前说过的话就全没了
      pids: [...new Set(room.members.map((m) => m.pid))],
      events: room.members.reduce((sum, m) => sum + store.highWater(m.pid), 0)
    };
  }, [roomOpen, parties, store, version]);

  /** 打开项目卡才去问 —— 它要跑好几条 git */
  useEffect(() => {
    if (projectOn === null || live === null) return;
    let alive = true;
    void live.projectOf(projectOn.path).then((got) => { if (alive) setProjectSaw(got); });
    return () => { alive = false; };
  }, [projectOn, live, version]);

  /** 边栏问历史 —— store 是唯一的真相源, 这里只做一次翻译 */
  const historyOf = useCallback(
    (pids: readonly string[], limit: number) => historyLines(store, pids, limit),
    [store, version]
  );

  useEffect(() => {
    if (live === null) { setHasKey(undefined); return; }
    let alive = true;
    void live.getProvider().then((got) => { if (alive && got !== null) setHasKey(got.hasKey); });
    return () => { alive = false; };
  }, [live, settingsOpen]);

  // 没送到的那次发言 —— 亮一条在输入框上方, 几秒后自己消失
  const [undelivered, setUndelivered] = useState<string | null>(null);

  const send = (text: string, shots: readonly Shot[] = []) => {
    if (party === null) return;
    /**
     * 房间里 **@谁就投给谁**.
     *
     *   没点名的话投给第一个成员 —— OS 那边给房间一条收件口之前,
     *   这是最诚实的近似, 而且它是**明说的**: 不装成"广播给所有人".
     */
    /**
     * **只能发给活着的进程**.
     *
     *   合并历史之后, members 里排在前面的往往是上一次运行留下的死 pid ——
     *   发给它 OS 会回 ok:false, 而界面上什么都不会发生: 消息发出去了、
     *   没人收、也没有报错. 这是最难查的一种"没反应".
     */
    const alive = party.people.filter((m) => LIVE.has(m.state));
    const pool = alive.length > 0 ? alive : party.people;
    /**
     * 投给谁 —— 规矩在 routing.ts, 这里只负责把材料凑齐.
     *
     *   **没点名不再投给所有人**. 上一版那样做的代价是: 每多一个 bot,
     *   你说一句话就多一次推理 —— 三个人的房间烧三轮, 一百个 bot 烧一百轮,
     *   而且屏幕上几个人抢着回同一件事.
     *
     *   没点名时投给**最近说话的那个** —— 那是在接着刚才的话头.
     *   要全屋就 @所有人: 爆炸只在你明说的时候发生.
     */
    const lastSpeaker = lastSaidWho(projector);
    // 群主在场时, 没点名的话默认归它 —— 群主承担房间内的默认接话角色
    const roomOwner = party.isRoom ? owners[party.title] : undefined;
    const targets = pickTargets({ isRoom: party.isRoom, pool, text, lastSpeaker, owner: roomOwner });
    /**
     * 这一次发言的身份.
     *
     *   群里没点名 = 投给每个人, 于是同一句话在账本里有 N 份 ——
     *   界面照实渲染就是**你说一句、屏幕上出现三遍**。
     *   给它一个共同的身份, 显示那边只认第一份。
     *
     *   不靠"文本+时间"去猜: 群聊里每份的文本本来就不同(各带各的上下文),
     *   而时间戳只差几毫秒 —— 那两样恰好都不可靠。
     */
    const utterance = `u${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
    /**
     * **一次说给一屋子人** —— "这话还发给了谁、谁拆活"由 OS 加.
     *
     *   那段原来是界面自己拼进去的, 于是它只存在于这一个客户端里:
     *   走 API 的路(测试台、命令行)收到的是**一句光秃秃的话**, 两个人
     *   于是各搭了一套 Vite 脚手架. 协议属于 OS, 不属于界面.
     */
    const pids = targets.map((one) => one.pid);
    const sends: Promise<"ok" | "gone" | "down">[] = [];
    for (const target of targets) {
      // 群里: 把他上次说完之后别人说的那几句捎给他, 否则他不知道在聊什么;
      // 群主是谁也一并捎上 —— 不然它答"谁是群主"要跑三个工具还答错
      const payload = party.isRoom && projector !== null
        ? withRoomContext({ room: party.title, speaker: target.name, rows: projector.rows, text, owner: roomOwner })
        : text;
      // 图给每个收话的人各带一份 —— OS 按内容存, 同一张只占一份盘
      if (live !== null) {
        sends.push(live.say(target.pid, payload, me.name, text, utterance,
          shots.filter((one) => one.file !== true).map((one) => ({ name: one.name, data: one.data })),
          // 一屋子人一起收到时, 分工那一段由 OS 按名单顺序给每个人加
          pids.length > 1 ? pids : undefined,
          // 文件走自己的通道 —— OS 落盘后把路径写进给模型的那段话
          shots.filter((one) => one.file === true).map((one) => ({ name: one.name, data: one.data }))));
      }
      else mock?.say(target.pid, payload);
    }
    /**
     * 送没送到要说出来.
     *
     *	OS 对着死 pid 只会回 ok:false —— 之前界面把它扔掉, 于是用户看到的
     *	是"点了发送, 什么都没发生", 这是最难查的一种没反应(例如:
     *	容器重建后话被投给上一代的 pid). 服务端现在会给上一代收尸, 这条
     *	兜底留着 —— 万一还有别的路走到死 pid, 至少人能看见.
     */
    if (sends.length > 0) {
      void Promise.all(sends).then((delivered) => {
        if (delivered.every((one) => one === "ok")) return;
        const down = delivered.includes("down");
        setUndelivered(down
          ? t("连不上这台 OS。客户端带着的那台进程可能停了，重启客户端就会回来。")
          : t("刚才那句话没送到 —— 收话的进程已经不在了。再说一句会把它拉起来。"));
        window.setTimeout(() => setUndelivered(null), 8000);
      });
    }
  };

  /**
   * 一行会话.
   *
   *   **群聊和单聊长得一样**: 同一个圆头像, 同一个两行结构.
   *   第二行永远是**这次会话最近说了什么**, 不是成员名单也不是状态词 ——
   *   人扫一眼列表是想知道"哪边有新动静", 不是想数人头.
   */
  const row = (entry: Party) => (
    <button
      key={entry.id}
      className={`item${entry.id === partyId ? " item--on" : ""}${unreadOf(entry, store, seen) ? " item--unread" : ""}`}
      onClick={() => { setPartyId(entry.id); setSeen((current) => markSeen(entry, store, current)); }}
      // 右键 = 菜单(详情/拉人/删除), 点头像 = 详情 —— 跟微信同一套手感
      onContextMenu={(event) => {
        event.preventDefault();
        setCtxMenu({ x: event.clientX, y: event.clientY, id: entry.id });
      }}
    >
      {/* 头像本身是入口: 点它开详情卡, 不切会话 */}
      <span className="item__face" role="button" aria-label={tt("{a} 的详情", { a: entry.isRoom ? roomLabel(entry.title) : entry.title })}
        onClick={(event) => {
          event.stopPropagation();
          const at = event.currentTarget.getBoundingClientRect();
          if (entry.isRoom) showRoom(entry.title, at);
          else showWho(entry.title, at);
        }}>
        {entry.isRoom
          ? <GroupAvatar ids={entry.people.map((m) => m.name)} liveness={presenceOf(entry.state, anyPending(entry, store)).kind} />
          : <Avatar id={entry.title} liveness={presenceOf(entry.state, anyPending(entry, store)).kind} />}
      </span>
      <span className="item__body">
        {/* 房间用一个小图标说"这是个房间", 不用一个井号 —— 见 roomname.ts */}
        <span className="item__line">
          {entry.isRoom ? <RoomMark /> : null}
          <span className="item__name">{entry.isRoom ? roomLabel(entry.title) : entry.title}</span>
        </span>
        <span className="item__preview">{summary(entry, store) || presenceOf(entry.state, anyPending(entry, store)).label}</span>
      </span>
      {/* 这一格**永远在**, 读过了只是看不见 —— 见 .item 那条注释:
          子元素个数是固定的, 行高不该跟着它变 */}
      <span className={`item__unread${unreadOf(entry, store, seen) ? "" : " item__unread--off"}`} />
      <span
        className="item__drop"
        role="button"
        aria-label={tt("删掉 {a}", { a: entry.title })}
        title={t("删掉这段对话")}
        onClick={(event) => { event.stopPropagation(); setConfirmDrop(entry.id); }}
      >×</span>
    </button>
  );

  // 所有 hook 都跑完了才轮到它 —— 这扇门只是换一张脸, 不换状态机
  if (locked) return <Gate />;

  return (
    <div className="app">
      <aside className="rail">
        {/* 这一条同时是 macOS 的拖拽区, 高度对齐右边的会话头 */}
        <div className={`rail__top${desktopShell ? "" : " rail__top--web"}`}>
          {desktopShell ? null : <img className="rail__mark" src={neoxMark} alt="" aria-hidden="true" />}
          {/* 只要字标, 不带环 —— 顶栏本来就窄, 环挤掉的是按钮的位置 */}
          <img className="rail__logo rail__logo--dark" src={wordmarkWhite} alt="NeoX" />
          <img className="rail__logo rail__logo--light" src={wordmarkDark} alt="NeoX" />
          {link !== "live" ? <span className={`link link--${link}`} title={link === "connecting" ? t("正在连") : t("断了, 正在重连")} /> : null}
          {/* + 是一个菜单: 加 bot / 加项目 —— 两种"新建"住同一扇门 */}
          <span className="plus" ref={topPlusRef}>
            <button className="icon" title={t("新建")} aria-label={t("新建")} aria-expanded={topPlusOpen}
              onClick={() => setTopPlusOpen((now) => !now)}>
              <svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true"><path d="M8 3.5v9M3.5 8h9" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" /></svg>
            </button>
            {topPlusOpen ? (
              <div className="plus__menu plus__menu--down" role="menu">
                <button className="plus__item" role="menuitem" type="button"
                  onClick={() => { setTopPlusOpen(false); setNewBotWork(""); setNewBotAsProject(false); setNewBotOpen(true); }}>
                  <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" aria-hidden="true">
                    <circle cx="8" cy="5.4" r="2.6" /><path d="M2.8 13.4c.6-2.6 2.7-4 5.2-4s4.6 1.4 5.2 4" />
                  </svg>
                  {t("加 bot")}<small>{t("一个人")}</small>
                </button>
                <button className="plus__item" role="menuitem" type="button"
                  onClick={() => { setTopPlusOpen(false); setNewBotWork(""); setNewBotAsProject(true); setNewBotOpen(true); }}>
                  <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" aria-hidden="true">
                    <path d="M1.8 4.2a1.2 1.2 0 0 1 1.2-1.2h3.2l1.5 1.8h5.3a1.2 1.2 0 0 1 1.2 1.2v6.2a1.2 1.2 0 0 1-1.2 1.2H3a1.2 1.2 0 0 1-1.2-1.2z" />
                  </svg>
                  {t("加项目")}<small>{t("一个目录 + 第一个人")}</small>
                </button>
              </div>
            ) : null}
          </span>
          <button className="icon" title={t("设置")} onClick={() => { setSettingsPage("connect"); setSettingsOpen(true); }} aria-label={t("设置")}>
            {/* 推子而不是齿轮: 15px 下齿轮的齿会糊成一圈毛, 看着像太阳 */}
            <svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round">
              <path d="M2.5 4.5h11M2.5 11.5h11" />
              <circle cx="6" cy="4.5" r="1.9" fill="var(--rail)" />
              <circle cx="10.5" cy="11.5" r="1.9" fill="var(--rail)" />
            </svg>
          </button>
        </div>
        {/* 一张平的列表: 群聊和单聊不分区 —— 它们是同一类东西 */}
        {/**
          * 视图切换: 默认那条**平的列表**跟微信一样, 分组是切过去的一档.
          *
          *   平铺列表降低默认复杂度; 助手数量多到难以区分时, 再提供项目分组.
          *
          *   所以它不是设置项里藏着的开关, 也不是默认行为 —— 列表顶上
          *   两格, 一眼看得见、一下切得回来. 只有真分得出堆的时候才显示:
          *   一个项目都没有的话, 这两格就是纯装饰.
          */}
        {byProject(parties).length > 1 ? (
          <div className="rail__view">
            <button className={`rail__viewbtn${grouped ? "" : " rail__viewbtn--on"}`}
              onClick={() => setGrouped(false)}>{t("全部")}</button>
            <button className={`rail__viewbtn${grouped ? " rail__viewbtn--on" : ""}`}
              onClick={() => setGrouped(true)}>{t("按项目")}</button>
          </div>
        ) : null}
        <div className="rail__list">
          {grouped
            ? byProject(parties).map((g) => (
                <div className={`rail__group${g.parties.some((p) => p.id === partyId) ? " rail__group--here" : ""}`} key={g.name}>
                  {/* **组头是这个项目的入口**: 点它看"这摊活现在什么状态" ——
                      谁手上还压着没合的、主干上次谁改的、合错了怎么退回去.
                      没派项目的那一堆("其他")没有状态可看, 就不给点 */}
                  {g.path === undefined
                    ? <div className="rail__grouphead">{g.name}<small>{g.parties.length}</small></div>
                    : <button className="rail__grouphead rail__grouphead--tap"
                        onClick={(event) => showProject(projNames[g.path!] ?? g.name, g.path!, event.currentTarget.getBoundingClientRect())}
                        title={t("看这个项目现在什么状态")}>
                        {projNames[g.path!] ?? g.name}<small>{g.parties.length}</small>
                      </button>}
                  {g.parties.map(row)}
                </div>
              ))
            : parties.map(row)}
        </div>
        {devOpen && mock !== null ? (
          <div className="rail__dev">
            <button onClick={() => party && mock.stream(party.members[0]!.pid)}>{t("模拟流式回答")}</button>
            <button onClick={() => party && mock.storm(party.members[0]!.pid, 20_000)}>{t("灌 2 万条")}</button>
            <button onClick={() => party && mock.widgets(party.members[0]!.pid)}>{t("发几张即时 UI")}</button>
          </div>
        ) : null}
      </aside>

      <main className="main">
        {hasKey === false ? (
          <div className="nokey">
            <span className="nokey__what">{t("还没配推理服务 —— 派了活 bot 也说不了话")}</span>
            <button className="w__btn w__btn--primary" onClick={() => { setSettingsPage("provider"); setSettingsOpen(true); }}>{t("去填一把 key")}</button>
          </div>
        ) : null}
        <header className="head">
          {party === null ? null : party.isRoom
            ? <button className="who" onClick={(event) => showRoom(party.title, event.currentTarget.getBoundingClientRect())} aria-label={tt("{a} 的资料", { a: roomLabel(party.title) })}>
                <GroupAvatar ids={party.people.map((m) => m.name)} size={28} liveness={presenceOf(party.state, anyPending(party, store)).kind} />
              </button>
            : <button className="who" onClick={(event) => showWho(party.title, event.currentTarget.getBoundingClientRect())} aria-label={tt("{a} 的资料", { a: party.title })}>
                <Avatar id={party.title} size={28} liveness={presenceOf(party.state, anyPending(party, store)).kind} />
              </button>}
          {party?.isRoom === true ? <RoomMark /> : null}
          <span className="head__name">{party === null ? t("还没有进程") : party.isRoom ? roomLabel(party.title) : party.title}</span>
          <span className="head__state">
            {party === null ? "" : party.isRoom ? tt("{a} 人", { a: party.people.length }) : presenceOf(party.state, anyPending(party, store)).label}
          </span>
          {party?.isRoom === true ? (
            <>
              <span className="head__grow" />
              {/* 那三个点: IM 里所有人都认得的"这个群是怎么回事" */}
              <button className="icon" title={t("这个群是怎么回事")} aria-label={t("这个群是怎么回事")}
                      onClick={(event) => showRoom(party.title, event.currentTarget.getBoundingClientRect())}>
                <svg viewBox="0 0 16 16" width="15" height="15" fill="currentColor" aria-hidden="true">
                  <circle cx="3.4" cy="8" r="1.35" /><circle cx="8" cy="8" r="1.35" /><circle cx="12.6" cy="8" r="1.35" />
                </svg>
              </button>
              <button className="icon" title={t("拉人进来")} aria-label={t("拉人进来")} onClick={() => setInviting(party.id)}>
                <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round">
                  <circle cx="6.2" cy="5.6" r="2.6" /><path d="M1.8 13.4c.5-2.3 2.3-3.6 4.4-3.6s3.9 1.3 4.4 3.6" />
                  <path d="M12.6 5.4v4.2M10.5 7.5h4.2" />
                </svg>
              </button>
            </>
          ) : null}
        </header>

        {projector !== null
          ? <Timeline
              projector={projector}
              version={version}
              renderWidget={renderRowWidget}
              onWho={showWho}
              // 我说的那几行右边挂我的脸 —— 名字是设置里那个
              me={me.name}
              showNames={party?.isRoom === true}
              // 正文里的 @所有人 也该点亮 —— 它跟点某个人是同一件事
              mentions={[...(party?.people.map((m) => m.name) ?? []), ...(party?.isRoom === true ? [EVERYONE_NAME] : [])]}
              onMetrics={setMetrics}
              // 头像右下角的状态点 —— 时间线里也要看得出谁在忙谁说完了
              presenceFor={(who) => {
                const member = party?.people.find((m) => m.name === who);
                return member === undefined ? undefined : presenceOf(member.state, false).kind;
              }}
              // 图的字节在 OS 那一侧, 界面按 id/路径取(见 shell/blobs.ts)
              {...(shots === null ? {} : { shotSrc: shots })}
              onOpenImage={(src, alt) => setBigShot({ src, alt })}
              // 刹车: 停掉这一轮, 人还在, 手上没提交的改动留着
              onStop={(who) => { if (live !== null) void live.stop(who); }}
            />
          : <div className="empty">{t("OS 上还没有进程。")}</div>}

        {/* 谁在干活: 一条状态条, 不再是时间线里垫底的伪消息 —— 见 BusyStrip */}
        <BusyStrip
          waiting={projector === null ? undefined : pendingLabel(party, projector, store)}
          onStop={(who) => { if (live !== null) void live.stop(who); }}
        />

        {/* 人不在了也能打字: /say 会把他拉起来. 锁死输入框的代价是
            用户只能重启整个客户端, 而重启之所以管用, 只是又走了一遍开机 spawn. */}
        {party !== null && party.people.every((m) => !LIVE.has(m.state))
          ? <div className="gone">{t("这个 bot 刚才不在。再说一句会把它拉起来。")}</div>
          : null}
        {undelivered !== null ? <div className="gone">{undelivered}</div> : null}
        <Composer
          onSend={send}
          disabled={party === null}
          // "所有人"也列进来, 而且排在最后 —— 见 mentions.ts
          mentions={mentionChoices(party?.people.map((m) => m.name) ?? [], party?.isRoom === true)}
        />

        {devOpen ? (
          <footer className="hud">
            <b>{stats.fps}</b> fps
            <span>p95 <b>{stats.p95}</b>ms</span>
            <span>{t("长帧")} <b>{stats.longFrames}</b></span>
            <span className="hud__sep" />
            <span>{endpoint === null ? t("合成源") : t("真 OS")}</span>
            <span>{t("渲染")} <b>{metrics.rendered}</b>/<b>{metrics.total}</b></span>
            <span>{t("事件")} <b>{store.ingested}</b></span>
            <span>{t("唤醒")} <b>{store.notified}</b></span>
          </footer>
        ) : null}
      </main>

      {invitingRoom === null ? null : (
        <div className="scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) setInviting(null); }}>
          <div className="sheet sheet--narrow" role="dialog" aria-label={t("拉人进来")}>
            <header className="sheet__head">
              <span>{t("拉进「")}{roomLabel(invitingRoom.title)}」</span>
              <button className="sheet__x" onClick={() => setInviting(null)} aria-label={t("关闭")}>×</button>
            </header>
            <section className="sheet__section">
              {/* 换房间要停掉旧进程再起一个 —— 说清楚, 别让人以为只是改个标签 */}
              <div className="sheet__label">
                {t("谁进来")}<small>{t("会重起他的进程，历史不丢")}</small>
              </div>
              {/* 解释只说一次 —— 每行都摊开一整句就是刷屏, 见 roommate.ts */}
              {parties.some((p) => !p.isRoom && p.people.some((m) => LIVE.has(m.state))
                && roommateNote(dominantWork(invitingRoom.people), dominantWork(p.people)) !== null)
                ? <div className="pick__hint">{ROOMMATE_HINT}</div> : null}
              <div className="pick">
                {parties.filter((p) => !p.isRoom && p.people.some((m) => LIVE.has(m.state))).map((p) => {
                  /* **进来之前先说清他碰不碰得到这摊活**: 拉人只改房间标签,
                     不动工作区 —— 见 roommate.ts */
                  const note = roommateNote(dominantWork(invitingRoom.people), dominantWork(p.people));
                  return (
                    <button key={p.id} className="pick__row" onClick={() => {
                      const room = invitingRoom.title;
                      setInviting(null);
                      if (live !== null) void live.move(p.title, room);
                    }}>
                      <Avatar id={p.title} size={26} />
                      <span>{p.title}</span>
                      {note === null ? null : <span className="pick__note">{note}</span>}
                    </button>
                  );
                })}
                {parties.filter((p) => !p.isRoom && p.people.some((m) => LIVE.has(m.state))).length === 0
                  ? <div className="pick__empty">{t("没有能拉进来的人了")}</div> : null}
              </div>
            </section>
            <section className="sheet__section">
              <div className="sheet__label">{t("现在房间里")}</div>
              <div className="pick">
                {invitingRoom.people.map((m) => (
                  <button key={m.pid} className="pick__row" onClick={() => {
                    setInviting(null);
                    if (live !== null) void live.move(m.name, "");
                  }}>
                    <Avatar id={m.name} size={26} />
                    <span>{m.name}</span>
                    <span className="pick__out">{t("请出去")}</span>
                  </button>
                ))}
              </div>
            </section>
          </div>
        </div>
      )}

      {projectOn === null ? null : (
        <Project
          name={projNames[projectOn.path] ?? projectOn.name} path={projectOn.path} report={projectSaw}
          onClose={hideProject} anchor={projectAt}
          onWho={(who, at) => { hideProject(); showWho(who, at); }}
          onRevert={async (hash) => {
            if (live === null) return { ok: false, message: t("现在连的是合成源，撤不了") };
            const got = await live.revert(projectOn.path, hash);
            // 撤完立刻重问一遍 —— 卡上那几行必须是撤之后的样子
            void live.projectOf(projectOn.path).then(setProjectSaw);
            return got;
          }}
          bots={byProject(parties).find((g) => g.path === projectOn.path)?.parties.length ?? 0}
          onHire={async () => {
            // 拉人像群里加人: 点一下就进来, 名字随机(事后能改), 不弹表单
            if (live === null) return;
            const used = new Set(parties.flatMap((p) => p.people.map((m) => m.name)));
            await live.create(randomBotName(used), "", projectOn.path, "");
            // 项目卡上的人数要立刻是加完之后的样子
            void live.projectOf(projectOn.path).then(setProjectSaw);
          }}
          onRename={async (next) => {
            if (live === null) return t("现在连的是合成源，改不了");
            const err = await live.nameProject(projectOn.path, next);
            if (err === null) {
              const names = await live.projectNames();
              setProjNames(names);
              setProjectOn({ name: names[projectOn.path] ?? projectOn.name, path: projectOn.path });
            }
            return err;
          }}
          onDisband={async () => {
            if (live === null) return;
            // 解散 = 删掉这个项目里所有会话(人和历史), 目录和代码留在磁盘上
            const group = byProject(parties).find((g) => g.path === projectOn.path);
            const pids = (group?.parties ?? []).flatMap((p) => p.members.map((m) => m.pid));
            if (pids.length > 0) await live.forget(pids);
            hideProject();
          }}
        />
      )}

      {/* 侧栏右键菜单: 详情 / 拉人(群) / 删除 —— 菜单是菜单, 卡是卡 */}
      {ctxMenu !== null ? (() => {
        const entry = parties.find((p) => p.id === ctxMenu.id);
        if (entry === undefined) return null;
        const anchor = new DOMRect(ctxMenu.x, ctxMenu.y, 1, 1);
        return (
          <div className="ctx" style={{ left: ctxMenu.x, top: ctxMenu.y }} role="menu"
            onMouseDown={(event) => event.stopPropagation()}>
            <button className="plus__item" role="menuitem" type="button"
              onClick={() => { setCtxMenu(null); if (entry.isRoom) showRoom(entry.title, anchor); else showWho(entry.title, anchor); }}>
              {entry.isRoom ? t("群详情") : t("看资料")}
            </button>
            {entry.isRoom ? (
              <button className="plus__item" role="menuitem" type="button"
                onClick={() => { setCtxMenu(null); setInviting(entry.title); }}>
                {t("拉人进来")}
              </button>
            ) : null}
            <button className="plus__item plus__item--danger" role="menuitem" type="button"
              onClick={() => { setCtxMenu(null); setConfirmDrop(entry.id); }}>
              {t("删掉这段对话")}
            </button>
          </div>
        );
      })() : null}

      <Lightbox shot={bigShot} onClose={() => setBigShot(null)} />

      <Room
        {...(roomOpen !== null && owners[roomOpen] !== undefined ? { owner: owners[roomOpen]! } : {})}
        target={roomTarget}
        onClose={hideRoom}
        anchor={roomAt}
        history={historyOf}
        onWho={(name, at) => { hideRoom(); showWho(name, at); }}
        onInvite={(room) => { hideRoom(); setInviting(room); }}
        onStop={(names) => { if (live !== null) names.forEach((one) => void live.stop(one)); }}
      />

      <Profile
        target={profile}
        onRebind={async (name, work) => {
          if (live === null) return t("现在连的是合成源, 换不了工作区");
          return await live.rebind(name, work);
        }}
        // 换房间跟"拉人进来"是同一件事(见上面那张单子), 只是从他这一侧发起
        rooms={parties.filter((p) => p.isRoom).map((p) => p.title)}
        // 别人正在用的工作区: 换地方时点一下就选中, 不用把屏幕上已经有的路径再敲一遍
        works={[...new Set(parties.flatMap((p) => p.people.map((m) => m.work ?? "")).filter((w) => w !== ""))]}
        onMove={async (name, room) => {
          if (live === null) return t("现在连的是合成源, 换不了房间");
          return await live.move(name, room);
        }}
        // "说过的话"翻成人话在 history.ts —— 边栏自己按页取, 不一次要全部
        history={historyOf}
        // 产物问的是 OS(它去跑 git), 合成源那条路答不出 —— 那时这一栏不出现
        {...(live === null ? {} : { output: (bot: string) => live.outputOf(bot) })}
        onClose={hideWho}
        anchor={whoAt}
        onStop={(name) => { if (live !== null) void live.stop(name); }}
        onOpen={(member) => {
          const solo = parties.find((p) => !p.isRoom && p.title === member.name);
          hideWho();
          if (solo !== undefined) setPartyId(solo.id);
        }}
      />

      {dropping === null ? null : (
        <div className="scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) setConfirmDrop(null); }}>
          <div className="sheet sheet--narrow" role="dialog" aria-label={t("删掉这段对话")}>
            <header className="sheet__head"><span>{t("删掉「")}{dropping.title}」?</span></header>
            <section className="sheet__section">
              {/* 说清楚删掉的是什么 —— 含糊的确认框等于没确认 */}
              <span className="drop__copy">
                {t("这段对话的全部历史会从账本里抹掉，进程会被停掉，之后不会再自己起来。")}
                <b>{t("这一步撤不回来。")}</b>
              </span>
            </section>
            <footer className="sheet__foot">
              <button className="w__btn" onClick={() => setConfirmDrop(null)}>{t("算了")}</button>
              <button
                className="w__btn w__btn--danger"
                onClick={() => {
                  // **现查那份 pid 名单**: 存下来的那份会过期 —— 见 confirmDrop
                  const pids = dropping.members.map((m) => m.pid);
                  setConfirmDrop(null);
                  if (partyId === dropping.id) setPartyId(null);
                  projectors.current.delete(dropping.id);
                  // 本地那份镜像也要清 —— 只让服务端删, 界面上一条都不会少
                  store.forget(pids);
                  setProcesses((current) => current.filter((p) => !pids.includes(p.pid)));
                  if (live !== null) void live.forget(pids);
                }}
              >{t("删掉")}</button>
            </footer>
          </div>
        </div>
      )}

      {checkupOpen ? <Checkup endpoint={endpoint} remember onClose={() => setCheckupOpen(false)} /> : null}

      <Settings
        open={settingsOpen} page={settingsPage} onClose={() => setSettingsOpen(false)}
        theme={theme} onTheme={setTheme}
        devOpen={devOpen} onDev={setDevOpen}
        endpoint={endpoint}
        health={health}
        me={me}
        onMe={setMe}
        live={live}
        onCheckup={() => setCheckupOpen(true)}
      />
      <NewBot
        open={newBotOpen} onClose={() => setNewBotOpen(false)}
        initialWork={newBotWork} project={newBotAsProject}
        // 已有项目的目录当下拉候选 —— 拉人进老项目不用重敲整条路径
        works={[...new Set([...byProject(parties).flatMap((g) => g.path === undefined ? [] : [g.path]), ...Object.keys(projNames)])]}
        // 随机起名不能跟在场的撞车
        taken={parties.flatMap((p) => p.people.map((m) => m.name))}
        // 房间的工作区一起传下去: 选了房间, "在哪儿干活"要跟着走 —— 见 roomwork.ts.
        // 哪个算"这个房间的"由 workspace.ts 判, 不再各算各的
        rooms={parties.filter((p) => p.isRoom).map((p) => {
          const work = dominantWork(p.people);
          return { title: p.title, ...(work === undefined ? {} : { work }) };
        })}
        onCreate={async (name, thread, work, role) => {
          if (live === null) return t("现在连的是合成源, 起不了真进程");
          const result = await live.create(name, thread, work, role);
          return result.error ?? null;
        }}
      />
    </div>
  );
}

/** 还活着的状态 —— 能收话的那几种 */
const LIVE: ReadonlySet<string> = new Set(["running", "waiting", "created"]);

/**
 * 这段会话里有没有活跃进程持有尚未解决的决策 —— 房间里任意一个活跃成员算.
 *
 *   **只看活着的那个进程**. 一条决策的主人死了(重启过、被杀过), 那张卡
 *   按下去也没人接 —— Resolve 找不到等它的那个进程. 把它算进来, 界面就会
 *   为一件**做不成的事**一直亮着橙点, 而点击后没有进程响应.
 *
 *   进程重启或退出后, 原进程持有的决策无法再被 Resolve 接收.
 */
function anyPending(party: Party, store: EventStore): boolean {
  return party.members.some((member) => LIVE.has(member.state) && store.pendingDecision(member.pid));
}

/**
 * 会话摘要: 房间里取**最近说话**的那个成员的最后一句.
 *
 *   ── 比的是时间, 不是条数 ──
 *
 *   原来比的是 highWater(事件条数) —— 那是"谁干得多", 不是"谁刚说过".
 *   一个做完整个模块的 bot 事件量远超刚上线的新人, 于是它永远赢:
 *   结果是 #oa 来了新人、说了话, 侧栏那一行**一个字都不变**.
 *
 *   一对一也一样错: 同一个 bot 跨多次重启有好几个 pid, 事件多的那个旧 pid
 *   会一直压住现在这个.
 */
function summary(party: Party, store: EventStore): string {
  let best = "";
  let bestAt = -1;
  // 摘要要扫**所有 pid** —— 最近说的话可能在上一次运行那个进程名下
  for (const member of party.members) {
    const at = store.previewAt(member.pid);
    if (at <= bestAt) continue;
    // **先压平再拼名字**: 反过来的话第一行的 markdown 记号(## 之类)
    // 就不在行首了, 脱不掉 —— 否则侧栏可能显示为 "回归: ## 渲染验收"
    const text = plainLine(store.preview(member.pid));
    if (text.length === 0) continue;
    best = party.isRoom ? `${member.name}: ${text}` : text;
    bestAt = at;
  }
  return best;
}

/**
 * 等待中的占位表示这一轮还没有收到进程回复.
 *
 *   判据是**事件流本身**: 最后一行仍是输入事件, 说明这一轮一个字都还没回来.
 *
 *   进程表里的 state === "running" 每 3 秒轮询一次, 而模型经常 1 秒就答完,
 *   于是 shimmer **一次都没出现过**.
 *   凡是"要立刻反映的东西", 判据就不能取自轮询来的快照.
 */
/**
 * RoomMark —— "这是个房间, 不是一个人".
 *
 *   原来这件事是靠名字前面那个 `#` 说的, 而那是内核里 thread 的写法,
 *   用户从没输入过它, 也没地方学过它是什么意思(见 roomname.ts).
 *   图标说得更清楚, 而且一个字都不占.
 */
function RoomMark() {
  return (
    <svg className="mark" viewBox="0 0 16 16" width="12" height="12" fill="none"
         stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="6" cy="6.4" r="2.5" />
      <path d="M1.8 13c.4-2 1.9-3.2 4.2-3.2S9.8 11 10.2 13" />
      <path d="M11 4.2a2.4 2.4 0 0 1 0 4.4M12.4 13c-.2-1.3-.7-2.3-1.5-2.9" />
    </svg>
  );
}

function pendingLabel(party: Party | null, projector: RowProjector | null, store: EventStore) {
  if (party === null || projector === null) return undefined;
  // 进度那几个数从账本算 —— 见 EventStore.turnProgress
  return waitingHint(party.people, projector.rows, (pid) => store.turnProgress(pid));
}

/**
 * lastSaidWho 房间里最近说话的是谁.
 *
 *   **判据是"说过的话", 不是事件条数**: 一个刚跑完十几个工具的 bot
 *   事件最多, 但它可能一句话都没说 —— 拿事件数当"最近说话的人",
 *   会把话投给一个正在埋头干活、跟这句话无关的人.
 */
function lastSaidWho(projector: { rows: readonly { kind: string; who?: string | undefined }[] } | null): string | undefined {
  if (projector === null) return undefined;
  for (let index = projector.rows.length - 1; index >= 0; index -= 1) {
    const row = projector.rows[index];
    if (row?.kind === "said" && row.who !== undefined) return row.who;
  }
  return undefined;
}
