/**
 * Checkup — 首启的一次环境自检.
 *
 *	**桌面客户端的本机模式里, bot 是在用户这台机器上真刀真枪干活的** ——
 *	用的是这台机器上已有的 git / python / node, 而不是什么自带的环境
 *	(那是镜像形态才有的东西, 见 Dockerfile.console). 一台干净的 Mac 上
 *	很可能连 git 都没有, 而现在缺 git 的下场是: 派下去的第一摊活跑到
 *	一半, 回来一句"这台机器上没有 git" —— **太晚了**. 那时用户已经取
 *	好名字、选好目录、交代完事情了.
 *
 *	所以把这件事挪到最前面: 开门先说清楚这台机器能干什么活、缺的那几件
 *	怎么装. **可以跳过** —— 只让 bot 写文档聊天的人一件都不需要装,
 *	拿一份工具清单拦住这种人是不讲道理的.
 *
 *	查的是**OS 那一侧**(/toolchain 由 OS 应答), 不是客户端所在的机器.
 *	远程模式下问到的于是是容器里那套(满的) —— 这正是想要的: 活在那边干.
 *
 *	只在**没缺 core 的时候**自己不出现. 见 shouldCheckup: 判据是
 *	"没跳过过 + 这一版没看过", 不是每次开机都弹.
 */
import { t } from "../i18n/index.js";
import { useEffect, useState } from "react";
import neoxMark from "../assets/neox-mark.svg";
import wordmarkWhite from "../assets/wordmark-white.png";
import wordmarkDark from "../assets/wordmark-dark.png";
import type { Endpoint } from "./endpoint.js";

export interface Tool {
  readonly name: string;
  readonly have: boolean;
  readonly path?: string;
  readonly core: boolean;
  readonly what: string;
  readonly fix?: string;
}
export interface Toolchain {
  readonly platform: string;
  readonly tools: readonly Tool[];
  readonly missing: number;
}

/** 看过就不再拦路 —— 值里带一个版本号, 将来清单变了可以再问一次 */
const SEEN = "neox.checkup.v1";

export function markCheckupSeen(): void {
  try { localStorage.setItem(SEEN, "1"); } catch { /* 存不了就每次都问, 不至于坏事 */ }
}
export function checkupSeen(): boolean {
  try { return localStorage.getItem(SEEN) === "1"; } catch { return false; }
}

/** 问一次 OS: 你脚下有什么. 够不着就当没这回事(合成源/老版本 OS 没这个口) */
export async function fetchToolchain(endpoint: Endpoint | null): Promise<Toolchain | null> {
  try {
    const got = await fetch(endpoint === null ? "/toolchain" : `${endpoint.url}/toolchain`,
      endpoint === null ? {} : { headers: { Authorization: `Bearer ${endpoint.token}` } });
    if (!got.ok) return null;
    return await got.json() as Toolchain;
  } catch { return null; }
}

interface Props {
  readonly endpoint: Endpoint | null;
  readonly onClose: () => void;
  /** 首启那次关掉就记下"看过了"; 从设置里点开的那次不记 —— 那是主动来看的 */
  readonly remember: boolean;
}

export function Checkup({ endpoint, onClose, remember }: Props) {
  const [state, setState] = useState<Toolchain | null>(null);
  const [busy, setBusy] = useState(true);
  const [copied, setCopied] = useState("");

  const recheck = async () => {
    setBusy(true);
    setState(await fetchToolchain(endpoint));
    setBusy(false);
  };
  useEffect(() => { void recheck(); }, []);

  const done = () => { if (remember) markCheckupSeen(); onClose(); };

  const copy = (text: string) => {
    void navigator.clipboard.writeText(text).then(
      () => { setCopied(text); setTimeout(() => setCopied(""), 1600); },
      () => { /* 剪贴板被拒 = 命令还在屏幕上, 手抄也行 */ },
    );
  };

  const tools = state?.tools ?? [];
  const missingCore = tools.filter((t) => t.core && !t.have);
  const missingElse = tools.filter((t) => !t.core && !t.have);

  return (
    <div className="gate">
      <div className="gate__card checkup__card">
        <div className="gate__brand">
          <img className="gate__mark" src={neoxMark} alt="" aria-hidden="true" />
          <img className="gate__word gate__word--dark" src={wordmarkWhite} alt="NeoX" />
          <img className="gate__word gate__word--light" src={wordmarkDark} alt="NeoX" />
        </div>

        <h1 className="gate__title">{t("看看这台机器能干什么活")}</h1>
        <p className="gate__text">
          {t("bot 干活用的是")}<strong>{t("这台 OS 脚下已有的工具")}</strong>{t("。缺的那几件不影响它说话， 但会影响它能不能真的把活干完。")}<strong>{t("一件都不装也能用")}</strong> {t("—— 只让它写文档、 聊天、整理东西的话，下面全都不必要。")}
        </p>

        {busy ? (
          <p className="gate__note">{t("正在看…")}</p>
        ) : state === null ? (
          <p className="gate__note">{t("这台 OS 不认这个自检接口 —— 多半是版本还没跟上，跳过就好。")}</p>
        ) : (
          <>
            <ul className="checkup__list">
              {tools.map((tool) => (
                <li key={tool.name} className={`checkup__row${tool.have ? "" : " checkup__row--off"}`}>
                  <span className={`checkup__dot${tool.have ? " checkup__dot--on" : tool.core ? " checkup__dot--core" : ""}`} />
                  <div className="checkup__body">
                    <div className="checkup__head">
                      <code className="checkup__name">{tool.name}</code>
                      {tool.have
                        ? <span className="checkup__where" title={tool.path}>{t("在 ·")} {tool.path}</span>
                        : <span className={tool.core ? "checkup__miss checkup__miss--core" : "checkup__miss"}>
                            {tool.core ? t("缺，建议装") : t("没有")}
                          </span>}
                    </div>
                    <p className="checkup__what">{tool.what}</p>
                    {!tool.have && tool.fix !== undefined && tool.fix !== "" ? (
                      <button className="checkup__fix" type="button" onClick={() => copy(tool.fix!)}
                        title={t("点一下复制，去终端里粘贴")}>
                        <code>{tool.fix}</code>
                        <span className="checkup__copy">{copied === tool.fix ? t("复制好了") : t("复制")}</span>
                      </button>
                    ) : null}
                  </div>
                </li>
              ))}
            </ul>
            <p className="gate__note">
              {missingCore.length > 0
                ? t("装完回来点「再看一遍」就认，不用重启。")
                : missingElse.length > 0
                  ? t("要紧的都在。剩下那几件等真派到那种活再装也来得及。")
                  : t("齐了，什么活都派得动。")}
            </p>
          </>
        )}

        <div className="gate__row gate__row--acts">
          <button className="gate__side" onClick={() => void recheck()} disabled={busy}>{t("再看一遍")}</button>
          <button className="gate__go" onClick={done}>{t("知道了，开始用")}</button>
          <button className="gate__skip" onClick={done}>{t("跳过")}</button>
        </div>
      </div>
    </div>
  );
}
