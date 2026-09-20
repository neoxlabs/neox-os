import { useEffect, useRef, useState, type ReactNode } from "react";
import { fetchToolchain } from "./Checkup.js";
import { t, tt, lang, setLang } from "../i18n/index.js";
import type { LiveSource, SpendReport } from "../os/source-live.js";
import { Avatar } from "../view/Avatar.js";
import neoxMark from "../assets/neox-mark.svg";
import { boundaryNote } from "./boundary.js";
import { keyHint } from "./keysource.js";

/**
 * Settings — 设置.
 *
 *   骨架照 Grok Bot: 860×620 的对话框, 左边 190px 导航, 右边一页内容.
 *   之前那个小方块塞不下任何真正要配的东西, 于是"设置"变成了
 *   一个只能改主题的装饰品.
 *
 *   放什么进来的判据: **它是不是你会改第二次的东西**.
 *   一次性的(观察口地址、凭据)只读展示, 不给编辑框 ——
 *   给了就等于承诺改完能生效, 而它其实要重启宿主.
 */

export type ThemeChoice = "system" | "dark" | "light";
export type SettingsPage = "connect" | "me" | "permission" | "provider" | "spend" | "about";

export interface Me { name: string }

export interface SettingsProps {
  open: boolean;
  /** 打开时停在哪一页. 不给就是"个人信息" —— 从"去填 key"进来的要直接落到推理服务 */
  page?: SettingsPage;
  onClose(): void;
  theme: ThemeChoice;
  onTheme(choice: ThemeChoice): void;
  devOpen: boolean;
  onDev(next: boolean): void;
  endpoint: { url: string; token: string } | null;
  health: { inference: boolean; model?: string; enforced?: boolean; mode?: string };
  me: Me;
  onMe(next: Me): void;
  live: LiveSource | null;
  /** 把环境自检那张卡叫出来 —— 首启弹过一次, 之后从这儿随时再看 */
  onCheckup(): void;
}

const PAGES: { id: SettingsPage; label: string }[] = [
  { id: "connect", label: t("连接") },
  { id: "me", label: t("个人信息") },
  { id: "spend", label: t("花了多少") },
  { id: "permission", label: t("权限") },
  { id: "provider", label: t("推理服务") },
  { id: "about", label: t("关于与更新") }
];

export function Settings(props: SettingsProps) {
  const { open, onClose } = props;
  // 缺省停在"连接" —— 这台客户端连的是谁, 是所有别的设置的前提
  const [page, setPage] = useState<SettingsPage>(props.page ?? "connect");
  const scrim = useRef<HTMLDivElement | null>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    // 每次打开都听调用方的: 从"去填一把 key"进来就该停在推理服务那一页,
    // 而不是让人在几页里自己找
    setPage(props.page ?? "connect");
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") closeRef.current(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);
  if (!open) return null;

  return (
    <div className="scrim" ref={scrim} onMouseDown={(event) => { if (event.target === scrim.current) onClose(); }}>
      <div className="dlg" role="dialog" aria-label={t("设置")}>
        <nav className="dlg__nav">
          {/* 顶上那一格原来是空的 —— 而左边这一栏跟右边的标题不在一条线上时,
              空着的那块看着像没加载出来. 放一个标记, 顺便说清这是哪儿 */}
          <div className="dlg__brand">
            <img className="dlg__mark" src={neoxMark} alt="" aria-hidden="true" />
            <span>{t("设置")}</span>
          </div>
          {PAGES.map((entry) => (
            <button key={entry.id} className="dlg__navItem" data-active={page === entry.id ? "" : undefined} onClick={() => setPage(entry.id)}>
              {entry.label}
            </button>
          ))}
        </nav>
        <section className="dlg__panel">
          <h2>{PAGES.find((entry) => entry.id === page)?.label}</h2>
          <button className="dlg__close" onClick={onClose} aria-label={t("关闭")}>×</button>
          <div className="dlg__body">
            {page === "connect" ? <ConnectPage {...props} /> : null}
            {page === "me" ? <MePage {...props} /> : null}
            {page === "permission" ? <PermissionPage {...props} /> : null}
            {page === "provider" ? <ProviderPage {...props} /> : null}
            {page === "spend" ? <SpendPage {...props} /> : null}
            {page === "about" ? <AboutPage {...props} /> : null}
          </div>
        </section>
      </div>
    </div>
  );
}

/**
 * ProviderSnapshot — /provider 写接口要的**全份**配置.
 *
 *	那个写是整体覆盖: 少带一个字段, 那个字段就被清零. 谁要改单项,
 *	都先揣着这份快照再改 —— 别只带自己关心的两格.
 */
interface ProviderSnapshot {
  baseUrl: string; model: string; protocol?: string;
  vision?: boolean; think?: boolean; searchApi?: string; searchUrl?: string; searchKey?: string; searchOn?: boolean; capTokens?: number; autoResume?: boolean; autoHire?: boolean;
}

function snapshotOf(got: { baseUrl: string; model: string; protocol?: string; vision?: boolean; think?: boolean; searchApi?: string; searchUrl?: string; searchKey?: string; searchOn?: boolean; capTokens?: number; autoResume?: boolean; autoHire?: boolean }): ProviderSnapshot {
  return {
    baseUrl: got.baseUrl, model: got.model,
    ...(got.protocol === undefined ? {} : { protocol: got.protocol }),
    ...(got.vision === undefined ? {} : { vision: got.vision }),
    ...(got.think === undefined ? {} : { think: got.think }),
    ...(got.searchApi === undefined ? {} : { searchApi: got.searchApi }),
    ...(got.searchUrl === undefined ? {} : { searchUrl: got.searchUrl }),
    ...(got.capTokens === undefined ? {} : { capTokens: got.capTokens }),
    ...(got.autoResume === undefined ? {} : { autoResume: got.autoResume }),
    ...(got.autoHire === undefined ? {} : { autoHire: got.autoHire })
  };
}

/** 一行设置: 左边标题+一句说明, 右边控件. 说明**只写一句** */
function Row({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div className="rw">
      <span className="rw__copy">
        {title}
        {hint === undefined ? null : <small>{hint}</small>}
      </span>
      <span className="rw__ctl">{children}</span>
    </div>
  );
}

function Seg<T extends string>({ value, options, onPick }: {
  value: T; options: readonly { id: T; label: string }[]; onPick(next: T): void;
}) {
  return (
    <span className="sg">
      {options.map((option) => (
        <button key={option.id} className={`sg__btn${value === option.id ? " sg__btn--on" : ""}`} onClick={() => onPick(option.id)}>
          {option.label}
        </button>
      ))}
    </span>
  );
}

function Toggle({ on, onChange }: { on: boolean; onChange(next: boolean): void }) {
  return (
    <button className={`tgl${on ? " tgl--on" : ""}`} role="switch" aria-checked={on} onClick={() => onChange(!on)}>
      <span className="tgl__knob" />
    </button>
  );
}

/**
 * ConnectPage — 这台客户端连的是谁.
 *
 *   两种形态, 差在一个开关上:
 *     本机: 客户端自己带一台 OS, 退出时一起收掉 —— 缺省
 *     远程: OS 跑在别处(Docker/云主机), 客户端只连接渲染
 *
 *   改完必须重启才生效(OS 是主进程起的), 所以按钮就叫"保存并重启" ——
 *   不做"看起来改了其实没生效"的假动作.
 *
 *   浏览器模式(界面由 OS 自己 serve)没有这个开关: 你已经连着它了,
 *   这一页只照实说连的是哪儿.
 */
function ConnectPage({ live, endpoint, onCheckup, onClose }: SettingsProps) {
  const bridge = window.neoxos?.connection;
  const [mode, setMode] = useState<"local" | "remote">("local");
  const [url, setUrl] = useState("");
  const [token, setToken] = useState("");
  const [probe, setProbe] = useState<{ busy: boolean; ok?: boolean; text?: string }>({ busy: false });
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (bridge === undefined) return;
    void bridge.get().then((got) => {
      setMode(got.mode);
      setUrl(got.url ?? "");
      setToken(got.token ?? "");
    });
  }, [bridge]);

  /**
   * 干活环境 —— **连的是哪台 OS, 就报哪台脚下有什么**.
   *
   *	远程模式下这一格说的是容器里那套(装死在镜像里, 永远是满的);
   *	本机模式说的是用户这台机器. 这个区别正是用户切远程的理由,
   *	所以它必须跟"连接方式"待在同一页.
   */
  const [kit, setKit] = useState<{ missing: number; total: number; have: number } | null>(null);
  useEffect(() => {
    let alive = true;
    void fetchToolchain(endpoint).then((got) => {
      if (!alive || got === null) return;
      setKit({ missing: got.missing, total: got.tools.length, have: got.tools.filter((t) => t.have).length });
    });
    return () => { alive = false; };
  }, [endpoint]);

  /** 真连一次 /health —— "填了"和"能连上"是两件事 */
  const test = async () => {
    const target = url.replace(/\/+$/, "");
    if (target === "" || token === "") { setProbe({ busy: false, ok: false, text: t("地址和 token 都要填") }); return; }
    setProbe({ busy: true });
    const started = Date.now();
    try {
      const res = await fetch(`${target}/health`, { headers: { Authorization: `Bearer ${token}` } });
      setProbe(res.ok
        ? { busy: false, ok: true, text: tt("通了 · {a}ms", { a: Date.now() - started }) }
        : { busy: false, ok: false, text: res.status === 401 ? t("token 不对") : tt("对面回了 {a}", { a: res.status }) });
    } catch {
      setProbe({ busy: false, ok: false, text: t("连不上 —— 地址、网络或对面没开") });
    }
  };

  /**
   * 这台 OS 算在哪个时区.
   *
   *	**它进的是判断不是显示**: 日报几点发、规则里"晚上七点到十点"算
   *	哪一段、"明天早上八点"是哪一刻. Docker 里默认 UTC, 全差 8 小时,
   *	而**一处都不会报错** —— 日报照发, 只是在凌晨五点发.
   *
   *	跟"干活环境"待在同一页: 两者都是"对面那台机器是什么样"的事实,
   *	而不是这个客户端的偏好
   */
  const [zone, setZone] = useState<{ zone: string; fixed: boolean; now: string; pick: string[] } | null>(null);
  const [zoneErr, setZoneErr] = useState<string | null>(null);
  useEffect(() => {
    if (live === null) return;
    let alive = true;
    void live.getTimezone().then((got) => { if (alive && got !== null) setZone(got); });
    return () => { alive = false; };
  }, [live]);

  const pickZone = async (next: string) => {
    if (live === null || zone === null) return;
    setZoneErr(null);
    // 空值 = 回到"跟着手机走". **定死了要能松开** —— 没有这一条,
    // 用户选过一次就永远回不到自动, 而界面上看不出新报上来的时区
    // 是被什么挡着的
    const failure = await live.setTimezone(next === "" ? null : next);
    if (failure !== null) { setZoneErr(failure); return; }
    const got = await live.getTimezone();
    if (got !== null) setZone(got);
  };

  const apply = async () => {
    if (bridge === undefined) return;
    await bridge.set(mode === "remote" ? { mode, url: url.replace(/\/+$/, ""), token } : { mode: "local" });
    await bridge.relaunch();
  };

  return (
    <div className="stack">
      <section className="sec">
        <div className="sec__label">{t("现在连的")}</div>
        <div className="panel">
          <Row title={endpoint === null ? t("还没连上任何 OS") : endpoint.url}
            hint={bridge === undefined ? t("界面由这台 OS 提供 —— 换一台就用它的带 token 链接打开") : t("本机 = 客户端自己带一台，退出一起关")}>
            <span className={`pill${endpoint !== null ? " pill--on" : ""}`}>{endpoint !== null ? t("已连接") : t("未连接")}</span>
          </Row>
        </div>
      </section>

      <section className="sec">
        <div className="sec__label">{t("干活环境")}<small>{t("bot 用的是这台 OS 脚下的工具")}</small></div>
        <div className="panel">
          <Row title={kit === null ? t("还没查") : kit.missing > 0 ? tt("缺 {a} 件要紧的", { a: kit.missing }) : tt("{a}/{b} 件在", { a: kit.have, b: kit.total })}
            hint={kit === null ? t("这台 OS 没应答自检") : kit.missing > 0
              ? t("缺 git 这类东西时，bot 干到一半才会报错 —— 不如现在装上")
              : t("要紧的都在，派什么活都行")}>
            <button className="w__btn" onClick={() => { onClose(); onCheckup(); }}>{t("看清单")}</button>
          </Row>
        </div>
      </section>

      {zone === null ? null : (
        <section className="sec">
          <div className="sec__label">{t("时区")}<small>{t("对面那台机器按哪儿的时间过日子")}</small></div>
          <div className="panel">
            {/* **右边显示的是对面此刻的钟点** —— 那是唯一能看出设对没设对
                的东西; 只显示时区名的话, 用户得自己去换算 */}
            <Row title={zone.now}
              hint={zone.fixed ? t("已定死 —— 手机换地方也不跟着变") : t("跟着手机报上来的时区走")}>
              <select className="field field--inline" value={zone.fixed ? zone.zone : ""}
                onChange={(event) => void pickZone(event.target.value)}>
                <option value="">{t("跟着手机")}</option>
                {zone.pick.includes(zone.zone) ? null : <option value={zone.zone}>{zone.zone}</option>}
                {zone.pick.map((z) => <option key={z} value={z}>{z}</option>)}
              </select>
            </Row>
          </div>
          {zoneErr === null ? null : <div className="dlg__error">{zoneErr}</div>}
        </section>
      )}

      {bridge === undefined ? null : (
        <section className="sec">
          <div className="sec__label">{t("连接方式")}<small>{t("改完要重启客户端")}</small></div>
          <div className="panel">
            <Row title={t("OS 在哪")} hint={t("远程 = 连一台已经跑着的（Docker、云主机、另一台机器）")}>
              <Seg value={mode} options={[{ id: "local", label: t("本机") }, { id: "remote", label: t("远程") }] as const}
                onPick={(next) => { setMode(next); setDirty(true); }} />
            </Row>
            {mode === "remote" ? (
              <>
                <Row title={t("地址")}>
                  <input className="field field--inline" value={url} placeholder="http://192.168.1.10:7717"
                    onChange={(event) => { setUrl(event.target.value); setDirty(true); }} />
                </Row>
                <Row title="Token" hint={t("对面首次启动时打印的那一串（存在它的 ~/.neox-os/token）")}>
                  <input className="field field--inline" type="password" value={token} placeholder={t("观察口凭据")}
                    onChange={(event) => { setToken(event.target.value); setDirty(true); }} />
                </Row>
                <Row title={t("先试一下")} hint={probe.text ?? t("真连一次，不保存")}>
                  <button className="w__btn" onClick={() => void test()} disabled={probe.busy}>
                    {probe.busy ? t("试着…") : probe.ok === true ? t("再试") : t("测试连接")}
                  </button>
                </Row>
              </>
            ) : null}
            <Row title={t("生效")} hint={t("OS 是客户端启动时定的，换连接得从头来")}>
              <button className="w__btn w__btn--primary" onClick={() => void apply()}
                disabled={!dirty || (mode === "remote" && (url === "" || token === ""))}>
                {t("保存并重启")}
              </button>
            </Row>
          </div>
        </section>
      )}
    </div>
  );
}

function MePage({ me, onMe, theme, onTheme, devOpen, onDev }: SettingsProps) {
  return (
    <div className="stack">
      <section className="sec">
        <div className="sec__label">{t("你")}</div>
        <div className="panel">
          {/* **不留过时的说明**: 这句话原来写的是"在群里", 那是名字只用于
              群聊转述的年代. 现在它会跟着每一句话到模型眼前 —— 单聊里
              问"我是谁"它也答得出. 写"在群里"会让人以为单聊填了没用. */}
          <Row title={t("名字")} hint={t("bot 就这么称呼你，问它「我是谁」也答得出")}>
            <input className="field field--inline" value={me.name} maxLength={20} placeholder={t("你的名字")}
              onChange={(event) => onMe({ name: event.target.value })} />
          </Row>
        </div>
      </section>
      <section className="sec">
        <div className="sec__label">{t("外观")}</div>
        <div className="panel">
          <Row title={t("主题")}>
            <Seg value={theme} onPick={onTheme} options={[
              { id: "system", label: t("跟随系统") }, { id: "dark", label: t("深色") }, { id: "light", label: t("浅色") }
            ]} />
          </Row>
          {/* 语言切换 = 整页重来 (见 i18n/index.ts 文件头): 六百处文案不是 state.
              两个选项的标签**各用自己的语言**写死, 不走 t() —— 切错了语言的人
              得能认出回去的那一个 */}
          <Row title={t("语言 / Language")} hint={t("切换后界面会重新载入；桌面客户端里新起的 bot 也跟着换")}>
            <Seg value={lang} onPick={(next) => setLang(next)} options={[
              { id: "zh", label: t("中文") }, { id: "en", label: "English" }
            ] as const} />
          </Row>
          <Row title={t("性能表")} hint={t("底部那一条，⌥D 也能切")}>
            <Toggle on={devOpen} onChange={onDev} />
          </Row>
        </div>
      </section>
    </div>
  );
}

const ASK_MODES = [
  { id: "always", label: t("每步都问") },
  { id: "bounds", label: t("要改东西时问") },
  { id: "never", label: t("都不问") }
] as const;

function PermissionPage({ live, health }: SettingsProps) {
  const [mode, setMode] = useState<string>("bounds");
  const [error, setError] = useState<string | null>(null);
  /** 拉人免批 —— 存在 provider 配置里, 跟 capTokens/autoResume 同一条通道 */
  const [autoHire, setAutoHire] = useState(false);
  /**
   * **带全份快照, 不带两个字段**: /provider 的写是整体覆盖 ——
   * 只回传 baseUrl+model 的话, 这一格一保存, capTokens/autoResume/vision
   * 全被清零(花费页原来就踩着这个坑).
   */
  const [config, setConfig] = useState<ProviderSnapshot | null>(null);

  useEffect(() => {
    if (live === null) return;
    void live.getPolicy().then((got) => { if (got !== null) setMode(got); });
    void live.getProvider().then((got) => {
      if (got === null) return;
      setConfig(snapshotOf(got));
      setAutoHire(got.autoHire === true);
    });
  }, [live]);

  const pick = async (next: string) => {
    if (live === null) return;
    const before = mode;
    setMode(next); setError(null);
    const failure = await live.setPolicy(next);
    if (failure !== null) { setMode(before); setError(failure); }
  };

  const pickHire = async (next: boolean) => {
    if (live === null || config === null) return;
    setAutoHire(next);
    const merged = { ...config, autoHire: next };
    setConfig(merged);
    const failure = await live.setProvider(merged);
    if (failure !== null) { setAutoHire(!next); setError(failure); }
  };

  return (
    <div className="stack">
      <section className="sec">
        <div className="sec__label">{t("动手前问不问你")}</div>
        <div className="panel">
          <Row title={t("什么时候问你")} hint={t("都不问 = 权限给足，连出网也不用批。拉人默认仍要问——下面那格单独放")}>
            <Seg value={mode as "always" | "bounds" | "never"} options={ASK_MODES} onPick={(next) => void pick(next)} />
          </Row>
          {/* 拉人单独一档: 多一个人 = 花费翻倍, 缺省要问; 但天天一起干活的
              用户嫌每次按一下烦 —— 他的钱包他做主 */}
          <Row title={t("拉人也不用问")} hint={t("开了它们就能自己扩编。每个免批进来的人在记录里都标着这一档")}>
            <Toggle on={autoHire} onChange={(next) => void pickHire(next)} />
          </Row>
          <Row title={t("改了之后新起的 bot 生效")} hint={t("能力在进程启动时定死，这是内核语义")}>
            <span className="rw__note">{t("已在的 bot 不变（拉人开关立刻生效）")}</span>
          </Row>
        </div>
      </section>
      {error !== null ? <div className="dlg__error">{error}</div> : null}
      <section className="sec">
        <div className="sec__label">{t("边界")}</div>
        <div className="panel">
          {/* **按这台机器的实情说** —— 三档由 boundaryNote 判, 见 boundary.ts.
              说了做不到的话, 后果不是"少一层保护", 是**用户以为有一层保护
              而其实没有** —— 他会因此放心把没验过的 bot 放进真实项目目录. */}
          {(() => {
            const b = boundaryNote(health ?? null);
            return (
              <Row title={b.title} hint={b.hint}>
                <span className={`rw__note${b.enforced ? "" : " rw__note--off"}`}>{b.note}</span>
              </Row>
            );
          })()}
        </div>
      </section>
    </div>
  );
}

function ProviderPage({ live, health }: SettingsProps) {
  const [baseUrl, setBaseUrl] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [hasKey, setHasKey] = useState(false);
  /** 那把 key 是哪来的 —— 见 keysource.ts */
  const [keyFrom, setKeyFrom] = useState<string | undefined>(undefined);
  /**
   * 这个模型认不认图.
   *
   *   **不按名字猜**: 猜错的表现不是报错, 是模型一本正经地描述一张
   *   它根本没收到的图. 所以是一个勾 —— 而且勾了之后"试一把"会真送
   *   一张图过去验, 不让人只能相信自己填对了.
   */
  const [vision, setVision] = useState(false);
  /**
   * 让它先想再答. **缺省关 —— 要的是快**.
   *
   *   量过: 同一句"1+1=?"，关掉 completion 是 1 个 token，开着 34–40。
   *   那三十几个 token 是要等的时间，而助理绝大多数时候在做的是
   *   "提醒我 5:30 打卡"这种事，想不想都是同一个答案。
   */
  const [think, setThink] = useState(false);
  /**
   * 搜网 —— **外挂服务让搜索不依赖供应商专有的工具协议**。
   *
   *   原生搜索的工具协议因供应商和接口而异：DeepSeek 的
   *   /chat/completions 中 tools[0].type 只认 function，
   *   传 web_search 直接 400，不能把原生搜索当作通用能力。
   *
   *   所以搜索是一个外挂服务。认识的几家填名字加 key；**别的填 URL** ——
   *   用户换第五家不该依赖应用发版。三个都空 = 不能搜网，而那时候
   *   web_search 这个工具根本不挂出来，bot 会照实说搜不了。
   */
  /**
   * 走哪套 wire format。**空 = 让 OS 自己认**（api.deepseek.com → Anthropic）。
   *
   *   这不是一个口味问题：**DeepSeek 官方的联网搜索只挂在 Messages
   *   那条口上**。同一把 key 同一个模型，/chat/completions 传 web_search
   *   直接 400，/responses 接受但一次都不搜，只有 /anthropic/v1/messages
   *   是真去搜的。所以协议选错 = 这台机器白白不能搜网。
   *
   *   留一个手动挡：认不出来的自建网关猜错了，表现是整台机器不能推理，
   *   那不该只能等我们发版。
   */
  const [protocol, setProtocol] = useState("");
  const [searchNative, setSearchNative] = useState(false);
  const [searchApi, setSearchApi] = useState("");
  const [searchUrl, setSearchUrl] = useState("");
  const [searchKey, setSearchKey] = useState("");
  const [searchOn, setSearchOn] = useState(false);
  const [state, setState] = useState<"idle" | "saving" | "saved">("idle");
  const [error, setError] = useState<string | null>(null);
  /** 试一把的结果 —— 试是"看看行不行", 跟保存是两件事 */
  const [probe, setProbe] = useState<{ busy: boolean; ok?: boolean; text?: string }>({ busy: false });
  /** 供应商自己报的模型名单. null = 还没拉过 */
  const [models, setModels] = useState<readonly string[] | null>(null);
  const [listing, setListing] = useState(false);

  useEffect(() => {
    if (live === null) return;
    void live.getProvider().then((got) => {
      if (got === null) return;
      setBaseUrl(got.baseUrl); setModel(got.model); setHasKey(got.hasKey); setKeyFrom(got.keyFrom);
      setVision(got.vision === true);
      setThink(got.think === true);
      setSearchApi(got.searchApi ?? "");
      setSearchUrl(got.searchUrl ?? "");
      setProtocol(got.protocol ?? "");
      setSearchNative(got.searchNative === true);
      setSearchOn(got.searchOn === true);
    });
  }, [live]);

  /**
   * 试一把: **真发一次请求, 但不保存**.
   *
   *   分开的理由: 试是"这条配置行不行", 保存是"从现在起全体 bot 都用它".
   *   合成一个按钮的话, 人就没法在不影响正在干活的那几个 bot 的前提下
   *   试一个新模型.
   */
  const test = async () => {
    if (live === null) return;
    setProbe({ busy: true }); setError(null);
    const got = await live.testProvider({ baseUrl, model, vision, ...(apiKey.length > 0 ? { apiKey } : {}) });
    setProbe({
      busy: false, ok: got.ok,
      text: got.ok
        ? tt("通了 · {a}ms{b}", { a: got.latencyMs, b: got.model !== undefined && got.model !== "" ? tt(" · 对面是 {a}", { a: got.model }) : "" })
          + (got.vision === undefined ? "" : ` · ${got.vision}`)
        : got.error ?? t("没通")
    });
  };

  /**
   * 一键拉模型: **让供应商自己报名字**.
   *
   *   手敲模型名是这一页最容易出错的一格: 拼错不会当场报错, 要等
   *   一次真请求打过去才知道 —— 而那时人已经在别的地方找原因了.
   *   (deepseek-v4 就不是合法名字, 只有 -pro 和 -flash.)
   */
  const fetchModels = async () => {
    if (live === null || listing) return;
    setListing(true); setError(null);
    const got = await live.listModels({ baseUrl, ...(apiKey.length > 0 ? { apiKey } : {}) });
    setListing(false);
    if (got.error !== undefined) { setError(got.error); return; }
    setModels(got.models ?? []);
  };

  const save = async () => {
    if (live === null) return;
    setState("saving"); setError(null);
    const failure = await live.setProvider({
      baseUrl, model, protocol, vision, think, searchApi, searchUrl,
      ...(apiKey.length > 0 ? { apiKey } : {}),
      // **key 空着不回传**：界面永远拿不到它（只进不出），
      // 而空不该被理解成"删掉"
      ...(searchKey.length > 0 ? { searchKey } : {})
    });
    if (failure !== null) { setState("idle"); setError(failure); return; }
    setApiKey(""); setHasKey(true); setState("saved");
    setTimeout(() => setState("idle"), 1600);
  };

  return (
    <div className="stack">
      <section className="sec">
        <div className="sec__label">{t("现在用的")}</div>
        <div className="panel">
          <Row title={health.inference ? health.model ?? t("已接") : t("还没接上")} hint={t("凭据只在宿主，不进任何进程")}>
            <span className={`pill${health.inference ? " pill--on" : ""}`}>{health.inference ? t("在用") : t("未配置")}</span>
          </Row>
        </div>
      </section>

      <section className="sec">
        <div className="sec__label">{t("配置")}<small>{t("换完立刻生效")}</small></div>
        <div className="panel">
          <Row title={t("接口地址")}>
            <input className="field field--inline" value={baseUrl} placeholder="https://api.deepseek.com"
              onChange={(event) => setBaseUrl(event.target.value)} />
          </Row>
          {/* 协议 —— **不是口味问题**：DeepSeek 官方的联网搜索只挂在
              Messages 那条口上（/chat/completions 传 web_search 直接 400，
              /responses 接受但一次都不搜）。认不出来的自建网关走 OpenAI 那套 */}
          <Row title={t("协议")}
               hint={protocol === "" ? t("按地址认。api.deepseek.com 走 Anthropic —— 官方联网搜索只在那条口上") : t("填死了就按这个走")}>
            <select className="field field--inline" value={protocol}
              onChange={(event) => { setProtocol(event.target.value); setProbe({ busy: false }); }}>
              <option value="">{t("自动识别")}</option>
              <option value="anthropic">Anthropic Messages</option>
              <option value="openai">OpenAI 兼容</option>
            </select>
          </Row>
          <Row title={t("模型")}>
            <span className="pickline">
              <input className="field field--inline" value={model} placeholder="deepseek-v4-flash"
                onChange={(event) => setModel(event.target.value)} />
              {/* 一颗小图标就够: 写"拉取模型列表"五个字要占掉半行, 而它只是个动作 */}
              <button className="icon--field" onClick={() => void fetchModels()} disabled={listing || live === null}
                      title={t("拉一份模型名单")} aria-label={t("拉一份模型名单")}>
                {listing
                  ? <span className="tool__spin" />
                  : <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
                      <path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9" /><path d="M13.6 2.6v3.2h-3.2" />
                    </svg>}
              </button>
            </span>
          </Row>
          {models === null ? null : (
            <Row title="" >
              {models.length === 0
                ? <span className="probe probe--bad">{t("这个地址没报出模型")}</span>
                : (
                  <span className="chips">
                    {models.map((name) => (
                      <button key={name} className={`chip${name === model ? " chip--on" : ""}`}
                              onClick={() => setModel(name)}>{name}</button>
                    ))}
                  </span>
                )}
            </Row>
          )}
          {/* key 只进不出: 读回来永远是空的, 它只该躺在宿主的 0600 文件里.
              **但要说清它在哪儿**: 环境变量那把从来不落盘 —— 见 keysource.ts */}
          <Row title="API Key" hint={keyHint(keyFrom, hasKey)}>
            <input className="field field--inline" type="password" value={apiKey}
              placeholder={hasKey ? "••••••••" : "sk-…"}
              onChange={(event) => setApiKey(event.target.value)} />
          </Row>
          {/* **不按模型名猜**: 猜错了不会报错, 它会一本正经地描述一张
              根本没收到的图. 勾了之后「试一把」会真送一张纯红的小图过去问
              颜色 —— 谁配谁负责, 而这下配的人当场就能验 */}
          {/* ── 搜网 ──
              **先看供应商自己带不带**：DeepSeek 是带的，但只在 Messages
              那条口上（见上面的「协议」）。带的话这里不用填，用的是同一把 key。

              不带、也没外挂的话，web_search 这个工具根本不挂出来，
              bot 会照实说搜不了 —— 那比调一个必然失败的工具然后编一个答案强 */}
          <Row title={t("搜网")}
            hint={searchOn ? t("已配好。bot 查不到的东西会自己去搜")
              : searchNative ? t("这家自己会搜，用的是上面那把 key。要换别家再配下面的")
              : t("这个模型自己不会搜网。不配的话它会照实说搜不了")}>
            <select className="field field--inline" value={searchApi}
              onChange={(event) => setSearchApi(event.target.value)}>
              <option value="">{t("自定义（填下面的地址）")}</option>
              <option value="bocha">{t("博查（国内）")}</option>
              <option value="tavily">Tavily</option>
              <option value="serper">Serper（Google）</option>
              <option value="brave">Brave</option>
            </select>
          </Row>
          {searchApi === "" ? (
            <Row title={t("搜索接口地址")} hint={t("自建的 SearXNG 之类也行，回包认 results / organic / data.webPages")}>
              <input className="field field--inline" value={searchUrl}
                placeholder="https://api.example.com/search"
                onChange={(event) => setSearchUrl(event.target.value)} />
            </Row>
          ) : null}
          <Row title={t("搜索 key")}
            hint={searchOn ? t("已经存着一把，不用重填") : t("自建不要 key 的可以留空")}>
            <input className="field field--inline" type="password" value={searchKey}
              placeholder={searchOn ? t("已设置") : t("粘进来")}
              onChange={(event) => setSearchKey(event.target.value)} />
          </Row>
          <Row title={t("先想再答")} hint={t("关着更快。排查问题、算方案的时候再打开")}>
            <Toggle on={think} onChange={setThink} />
          </Row>
          <Row title={t("这个模型能看图")} hint={t("勾了之后 bot 才能看你发的截图；试一把会真送一张图过去验")}>
            <Toggle on={vision} onChange={setVision} />
          </Row>
        </div>
      </section>

      {error !== null ? <div className="dlg__error">{error}</div> : null}
      <div className="sec__foot sec__foot--split">
        {/* 试的结果就摆在按钮边上: 它是这次点击的回执, 不该跑到别处去 */}
        {probe.busy
          ? <span className="probe">{t("试着…")}</span>
          : probe.text === undefined
            ? <span className="probe probe--hint">{t("保存之后全体 bot 都用这条")}</span>
            : <span className={`probe${probe.ok === true ? " probe--ok" : " probe--bad"}`}>{probe.text}</span>}
        <button className="w__btn" onClick={() => void test()} disabled={probe.busy || live === null}>
          {t("试一把")}
        </button>
        <button className="w__btn w__btn--primary" onClick={() => void save()} disabled={state === "saving" || live === null}>
          {state === "saving" ? t("存着…") : state === "saved" ? t("存好了") : t("保存")}
        </button>
      </div>
    </div>
  );
}

/**
 * SpendPage —— **这摊活到底花了多少**.
 *
 *   一个人带着一屋子 bot 干活, 花的是真钱, 而这笔钱原来只在账本里,
 *   界面上一个数都没有. 看不见花费的人只有两种反应: 要么不敢用,
 *   要么用到某天收到账单才发现.
 *
 *   **按人排, 贵的在前**: 这一页是拿来做决定的, 而决定通常关于最贵的
 *   那一个 —— "小登太贵了"能引出下一步(换模型、拆活、换人),
 *   "这个月花了 12 块"引不出任何东西.
 *
 *   每个人后面跟着**它换来了什么**(几次提交、几轮对话) —— 只报花费
 *   会让人只想着省, 而真正的问题从来是"这笔钱值不值".
 */
function SpendPage({ live }: SettingsProps) {
  const [report, setReport] = useState<SpendReport | null | undefined>(undefined);
  /**
   * 每轮上限 —— **缺省不限**.
   *
   *   一道用户自己没设过的上限, 突然把一件正经活拦腰砍断, 比没有上限
   *   更糟: 他不知道发生了什么, 只看到"它干到一半不干了".
   *
   *   按"轮"不按"累计": 跑飞的样子是一轮停不下来, 而一个干了三天的
   *   bot 累计花得多是正常的 —— 按累计设上限, 到点之后它就永久残废了.
   */
  const [cap, setCap] = useState("");
  /** 掉线之后自己接着干 —— 同样缺省关着: 自己动起来是件要点头的事 */
  const [resume, setResume] = useState(false);
  const [saved, setSaved] = useState(false);
  // 全份快照 —— /provider 的写是整体覆盖, 只带两格会把别的清零(见 snapshotOf)
  const [config, setConfig] = useState<ProviderSnapshot | null>(null);

  useEffect(() => {
    if (live === null) return;
    void live.getProvider().then((got) => {
      if (got === null) return;
      setConfig(snapshotOf(got));
      setCap(got.capTokens ? String(got.capTokens) : "");
      setResume(got.autoResume === true);
    });
  }, [live]);

  const keep = async (next: { capTokens?: number; autoResume?: boolean }) => {
    if (live === null || config === null) return;
    const merged = { ...config, ...next };
    setConfig(merged);
    await live.setProvider(merged);
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
  };

  useEffect(() => {
    if (live === null) { setReport(null); return; }
    let alive = true;
    const pull = () => { void live.spend().then((got) => { if (alive) setReport(got); }); };
    pull();
    // 它们正在干活, 数字一直在动 —— 但不必太勤: 这一页是拿来看趋势的
    const timer = setInterval(pull, 5000);
    return () => { alive = false; clearInterval(timer); };
  }, [live]);

  if (report === undefined) return <div className="stack"><div className="pick__empty">{t("读着…")}</div></div>;
  if (report === null) return <div className="stack"><div className="pick__empty">{t("现在连的是合成源，没有账可算。")}</div></div>;

  const total = report.total;
  const tokens = total.prompt + total.completion;
  const top = report.bots.length > 0 ? report.bots[0]! : null;
  const most = top === null ? 0 : top.prompt + top.completion;

  return (
    <div className="stack">
      <section className="sec">
        <div className="sec__label">{t("一共")}<small>{t("全部来自事件账本，不另记一份")}</small></div>
        <div className="tiles">
          <Tile big={compact(tokens)} label="token" note={tt("送进去 {a} · 吐出来 {b}", { a: compact(total.prompt), b: compact(total.completion) })} />
          <Tile big={compact(total.cached)} label={t("缓存命中")} note={cacheNote(total.cached, total.prompt)} good />
          <Tile big={String(total.commits)} label={t("次提交")} note={tt("{a} 轮对话换来的", { a: total.turns })} />
        </div>
      </section>

      <section className="sec">
        <div className="sec__label">{t("管住花费")}<small>{saved ? t("存好了") : t("改完立刻生效，正在跑的那一轮也算")}</small></div>
        <div className="panel">
          <Row title={t("一轮最多烧多少")} hint={t("留空 = 不限。跑飞的样子是一轮停不下来，所以按轮算，不按累计")}>
            <span className="rw__ctl">
              <input className="field field--inline field--num" value={cap} placeholder={t("不限")}
                inputMode="numeric"
                onChange={(event) => setCap(event.target.value.replace(/[^0-9]/g, ""))}
                onBlur={() => void keep({ capTokens: cap === "" ? 0 : Number(cap) })} />
              <span className="rw__note">token</span>
            </span>
          </Row>
          <Row title={t("掉线之后自己接着干")} hint={t("重启／被停之后，工作区里还有没提交的改动就自己接上；不勾就等你说话")}>
            <Toggle on={resume} onChange={(next) => { setResume(next); void keep({ autoResume: next }); }} />
          </Row>
        </div>
      </section>

      <section className="sec">
        <div className="sec__label">{t("按人")}<small>{t("贵的在前")}</small></div>
        <div className="panel">
          {report.bots.length === 0
            ? <div className="pick__empty">{t("还没有人花过钱")}</div>
            : report.bots.map((row) => {
              const sum = row.prompt + row.completion;
              return (
                <div key={row.bot} className="spend">
                  <div className="spend__head">
                    <Avatar id={row.bot} size={22} />
                    <span className="spend__who">{row.bot}</span>
                    {row.project === undefined || row.project === "" ? null
                      : <span className="spend__where">{row.project.split("/").pop()}</span>}
                    <span className="spend__num">{compact(sum)}</span>
                  </div>
                  {/* 一条横杠比一列数字好读: 谁占大头**一眼就看出来**, 
                      而这一页要回答的正是那个问题 */}
                  <div className="spend__bar">
                    <span className="spend__fill" style={{ width: `${most === 0 ? 0 : Math.max(2, (sum / most) * 100)}%` }} />
                  </div>
                  <div className="spend__note">
                    {row.turns} {t("轮 ·")} {row.calls} {t("次调用 ·")} {row.commits} {t("次提交")}
                    {row.cached > 0 ? <> {t("· 缓存省了")} {compact(row.cached)}</> : null}
                  </div>
                </div>
              );
            })}
        </div>
      </section>
    </div>
  );
}

function Tile({ big, label, note, good }: { big: string; label: string; note: string; good?: boolean }) {
  return (
    <div className="tile">
      <div className={`tile__big${good === true ? " tile__big--good" : ""}`}>{big}</div>
      <div className="tile__label">{label}</div>
      <div className="tile__note">{note}</div>
    </div>
  );
}

/** 大数写成 12.3k / 4.5M —— 一列七位数字没人读得出大小 */
function compact(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** 缓存命中率: 它是**省下来的钱**, 值得单说一句 */
function cacheNote(cached: number, prompt: number): string {
  if (prompt === 0) return t("还没有");
  return tt("送进去的 {a}% 是命中的", { a: Math.round((cached / prompt) * 100) });
}

function AboutPage({ endpoint, health }: SettingsProps) {
  return (
    <div className="stack">
      <section className="sec">
        <div className="sec__label">{t("NeoxOS 控制台")}</div>
        <div className="panel">
          <Row title={t("版本")}><span className="rw__note">{t("0.1.0 · 开发中")}</span></Row>
          <Row title={t("推理")}><span className={`rw__note${health.inference ? "" : " rw__note--off"}`}>{health.inference ? health.model ?? t("已接") : t("没接")}</span></Row>
          <Row title={t("观察口")}><span className="rw__note rw__note--mono">{endpoint?.url ?? t("合成源")}</span></Row>
          <Row title={t("凭据")}><span className="rw__note rw__note--mono">{endpoint === null ? "—" : `${endpoint.token.slice(0, 6)}…`}</span></Row>
        </div>
      </section>
      <section className="sec">
        <div className="sec__label">{t("更新")}</div>
        <div className="panel">
          {/* 没接就说没接: 放一个点了没反应的"检查更新"比没有更糟 */}
          <Row title={t("更新通道")} hint={t("现在只能从源码起")}><span className="rw__note">{t("还没接")}</span></Row>
        </div>
      </section>
    </div>
  );
}
