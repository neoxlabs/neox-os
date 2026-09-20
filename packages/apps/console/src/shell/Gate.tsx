/**
 * Gate — 裸着进来(没带 token / token 过期)时的接入引导.
 *
 *	docker run 起来之后, 用户最自然的动作是点 Docker Desktop 里的端口
 *	链接 —— 打开的是不带 token 的裸地址. 之前的下场: 一个红点 + 空侧栏,
 *	页面不解释任何事. 现在: 探测到当前源是一台锁着的 OS(/processes 回
 *	401)就把门画出来, 告诉用户钥匙在启动日志里, 让他贴进来.
 *
 *	贴对钥匙之后顺手把推理服务也在这儿配了(没配的话) —— 不然进门第一眼
 *	还是一条"还没配推理服务"的横幅, 引导断在半路.
 *
 *	这不是登录页: 没有账号体系, token 是唯一的钥匙(见 osinit/observe.go
 *	的 guard). 环境变量注入(NEOX_OBSERVE_TOKEN / NEOX_API_KEY)的机器
 *	不会走到这儿 —— 钥匙和 key 都已经在了.
 */
import { t, tt } from "../i18n/index.js";
import { useState } from "react";
import neoxMark from "../assets/neox-mark.svg";
import wordmarkWhite from "../assets/wordmark-white.png";
import wordmarkDark from "../assets/wordmark-dark.png";

/** 贴进来的可能是启动日志里那整条 URL, 也可能只是 token 本身 */
export function tokenFromPaste(raw: string): string {
  const text = raw.trim();
  if (text === "") return "";
  try {
    const url = new URL(text);
    const param = url.searchParams.get("token");
    if (param !== null && param !== "") return param;
  } catch { /* 不是 URL —— 那就是 token 本身 */ }
  return text;
}

interface ProviderDraft { baseUrl: string; model: string; apiKey: string }

export function Gate() {
  const [step, setStep] = useState<"key" | "provider">("key");
  const [pasted, setPasted] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [wrong, setWrong] = useState("");
  const [draft, setDraft] = useState<ProviderDraft>({ baseUrl: "", model: "", apiKey: "" });
  const [probe, setProbe] = useState("");
  /** 供应商自报的模型名单 —— 模型名是抄不对的东西, 能选就别让人敲 */
  const [models, setModels] = useState<readonly string[]>([]);
  const [modelsNote, setModelsNote] = useState("");

  const auth = (t: string) => ({ Authorization: `Bearer ${t}` });

  /** 进门 —— resolveEndpoint 会记住 token 并从地址栏抹掉 */
  const enter = (t: string) => location.replace(`/?token=${encodeURIComponent(t)}`);

  const tryKey = async () => {
    // 叫 key 不叫 t: t 是 i18n 的查表函数, 这里原来的局部变量把它遮住了,
    // 下面两句报错文案就调到了一个字符串上 —— TS 报 "String has no call signatures"
    const key = tokenFromPaste(pasted);
    if (key === "" || busy) return;
    setBusy(true); setWrong("");
    try {
      const got = await fetch("/processes", { headers: auth(key) });
      if (!got.ok) { setWrong(t("这把钥匙开不了这扇门 —— 再对一眼启动日志。")); return; }
      setToken(key);
      // 推理服务配过了就不啰嗦, 直接进门
      const conf = await fetch("/provider", { headers: auth(key) });
      if (conf.ok) {
        const body = await conf.json() as { baseUrl?: string; model?: string; hasKey?: boolean };
        if (body.baseUrl !== undefined && body.baseUrl !== "" && body.model !== undefined && body.model !== "" && body.hasKey === true) { enter(key); return; }
        setDraft({ baseUrl: body.baseUrl ?? "", model: body.model ?? "", apiKey: "" });
      }
      setStep("provider");
    } catch {
      setWrong(t("够不着这台 OS —— 它还活着吗？"));
    } finally {
      setBusy(false);
    }
  };

  /**
   * 自动拉模型名单 —— 地址或 key 一填好(失焦)就问供应商要.
   *
   *	不用点任何按钮: 名单是供应商的事实, 人只该从里面挑, 不该默写.
   *	拉不到不拦路(有的口子没 key 不给列), 说一声接着让人手填.
   */
  const pullModels = async (next: ProviderDraft) => {
    if (next.baseUrl === "") return;
    setModelsNote(t("正在拉模型列表…"));
    try {
      const got = await fetch("/provider/models", {
        method: "POST", headers: { ...auth(token), "Content-Type": "application/json" },
        body: JSON.stringify({ baseUrl: next.baseUrl, model: next.model, ...(next.apiKey !== "" ? { apiKey: next.apiKey } : {}) }),
      });
      const body = await got.json() as { models?: string[]; error?: string };
      if (!got.ok || body.models === undefined) {
        setModels([]);
        setModelsNote(tt("拉不到模型列表({a}) —— 手填也行", { a: body.error ?? "对面没回话" }));
        return;
      }
      setModels(body.models);
      setModelsNote(body.models.length === 0 ? t("供应商说它一个模型都没有 —— 手填一个试试") : "");
    } catch {
      setModels([]);
      setModelsNote(t("拉不到模型列表 —— 手填也行"));
    }
  };

  const tryProvider = async () => {
    if (busy) return;
    setBusy(true); setProbe("");
    try {
      const got = await fetch("/provider/test", {
        method: "POST", headers: { ...auth(token), "Content-Type": "application/json" },
        body: JSON.stringify({ baseUrl: draft.baseUrl, model: draft.model, ...(draft.apiKey !== "" ? { apiKey: draft.apiKey } : {}) }),
      });
      const body = await got.json() as { ok?: boolean; model?: string; latencyMs?: number; error?: string };
      setProbe(body.ok === true ? tt("通了：{a} · {b}ms", { a: body.model ?? draft.model, b: body.latencyMs ?? "?" }) : tt("没通：{a}", { a: body.error ?? "对面没回话" }));
    } catch {
      setProbe(t("没通：请求都没出去"));
    } finally {
      setBusy(false);
    }
  };

  const saveAndEnter = async () => {
    if (busy) return;
    setBusy(true); setProbe("");
    try {
      const got = await fetch("/provider", {
        method: "POST", headers: { ...auth(token), "Content-Type": "application/json" },
        body: JSON.stringify({ baseUrl: draft.baseUrl, model: draft.model, ...(draft.apiKey !== "" ? { apiKey: draft.apiKey } : {}) }),
      });
      if (!got.ok) {
        const body = await got.json().catch(() => ({})) as { error?: string };
        setProbe(tt("存不上：{a}", { a: body.error ?? "地址和模型都得填" }));
        return;
      }
      enter(token);
    } catch {
      setProbe(t("存不上：请求都没出去"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="gate">
      <div className="gate__card">
        <div className="gate__brand">
          <img className="gate__mark" src={neoxMark} alt="" aria-hidden="true" />
          <img className="gate__word gate__word--dark" src={wordmarkWhite} alt="NeoX" />
          <img className="gate__word gate__word--light" src={wordmarkDark} alt="NeoX" />
        </div>

        {step === "key" ? (
          <>
            <h1 className="gate__title">{t("这台 OS 认钥匙")}</h1>
            <p className="gate__text">
              {t("启动它的地方打印过一行")} <code>{t("浏览器打开 http://…/?token=…")}</code> {t("—— 那就是钥匙。 Docker 里跑的用")} <code>{t("docker logs 容器名")}</code> {t("能再看到； 也可以启动时用")} <code>{t("-e NEOX_OBSERVE_TOKEN=自己定的钥匙")}</code> {t("直接指定。")}
            </p>
            <div className="gate__row">
              <input
                className="gate__input" type="password" autoFocus
                placeholder={t("把那行地址或 token 贴进来")}
                value={pasted}
                onChange={(event) => { setPasted(event.target.value); setWrong(""); }}
                onKeyDown={(event) => { if (event.key === "Enter") void tryKey(); }}
              />
              <button className="gate__go" onClick={() => void tryKey()} disabled={busy || tokenFromPaste(pasted) === ""}>
                {busy ? t("开门…") : t("进门")}
              </button>
            </div>
            {wrong !== "" ? <p className="gate__wrong">{wrong}</p> : null}
          </>
        ) : (
          <>
            <h1 className="gate__title">{t("顺手把推理服务接上")}</h1>
            <p className="gate__text">
              {t("bot 说话要走它。OpenAI 兼容协议：接口地址 + 模型名 + API key。 也可以启动时注入")} <code>-e NEOX_API_KEY=… -e NEOX_API_BASE=… -e NEOX_MODEL_ID=…</code>。
            </p>
            <input className="gate__input gate__input--wide" placeholder={t("接口地址，如 https://api.deepseek.com")}
              value={draft.baseUrl}
              onChange={(event) => setDraft({ ...draft, baseUrl: event.target.value })}
              onBlur={() => void pullModels(draft)} />
            <input className="gate__input gate__input--wide" type="password" placeholder="API key"
              value={draft.apiKey}
              onChange={(event) => setDraft({ ...draft, apiKey: event.target.value })}
              onBlur={() => void pullModels(draft)} />
            <input className="gate__input gate__input--wide" placeholder={t("模型名，如 deepseek-chat")} list="gate-models"
              value={draft.model} onChange={(event) => setDraft({ ...draft, model: event.target.value })} />
            <datalist id="gate-models">
              {models.map((name) => <option key={name} value={name} />)}
            </datalist>
            {models.length > 0 ? (
              <div className="gate__models">
                {models.slice(0, 12).map((name) => (
                  <button key={name} type="button"
                    className={`gate__chip${draft.model === name ? " gate__chip--on" : ""}`}
                    onClick={() => setDraft({ ...draft, model: name })}>{name}</button>
                ))}
              </div>
            ) : null}
            {modelsNote !== "" ? <p className="gate__note">{modelsNote}</p> : null}
            <div className="gate__row gate__row--acts">
              <button className="gate__side" onClick={() => void tryProvider()}
                disabled={busy || draft.baseUrl === "" || draft.model === ""}>{t("测一把")}</button>
              <button className="gate__go" onClick={() => void saveAndEnter()}
                disabled={busy || draft.baseUrl === "" || draft.model === ""}>{t("存好，进门")}</button>
              <button className="gate__skip" onClick={() => enter(token)} disabled={busy}>{t("先跳过")}</button>
            </div>
            {probe !== "" ? <p className={probe.startsWith(t("通了")) ? "gate__ok" : "gate__wrong"}>{probe}</p> : null}
          </>
        )}
      </div>
    </div>
  );
}
