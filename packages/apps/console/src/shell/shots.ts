import { t, tt } from "../i18n/index.js";
/**
 * shots —— 从剪贴板/拖拽/选择器里拿到的图, 变成能发出去的东西.
 *
 *   **只认图**: 粘一段带格式的文本、拖一个 zip 进来, 都不该变成
 *   一个发不出去的附件. 认不出的当场说, 别等发送时才失败.
 */

/** 一份待发的附件 —— 图有 url 预览, 文件只有名字; 发送都要 data */
export interface Shot {
  readonly key: string;
  readonly name: string;
  readonly mime: string;
  /** 本地预览用的 blob: URL —— 图才有; 文件是空串(画一枚标签, 不画图) */
  readonly url: string;
  /** base64, **不带 data: 前缀** */
  readonly data: string;
  readonly bytes: number;
  /** true = 普通文件, 不是图 —— OS 那侧走 files 通道, bot 用 read_file 打开 */
  readonly file?: boolean;
}

/** 认得的那几种 —— 跟 OS 那侧一致(go/osinit/blobs.go) */
const KINDS = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);

/**
 * 一张图最大 6MB.
 *
 *	跟 view_image 的上限对齐(agent/viewimage.go): 大过那个数, 图存下来了
 *	但 bot 打不开 —— 那种"发出去了却没人看得见"最难查.
 */
export const MAX_SHOT_BYTES = 6 << 20;

/**
 * 一个文件最大 20MB.
 *
 *	它要过一遍 base64 + JSON(约 ×1.37), 再大这一发就是几十兆的请求体 ——
 *	本机没问题, 远程连接会顶在半路. 更大的东西该放进工作区让 bot 直接读.
 */
export const MAX_FILE_BYTES = 20 << 20;

export function isShot(file: { type: string }): boolean {
  return KINDS.has(file.type);
}

/** 读一批图. 返回 [能发的, 没收下的原因] —— 两样都要, 不能静默丢 */
export async function readShots(files: readonly File[]): Promise<[Shot[], string[]]> {
  const ok: Shot[] = [];
  const bad: string[] = [];
  for (const file of files) {
    if (!isShot(file)) {
      bad.push(tt("{a} 不是图片（png / jpeg / webp / gif）", { a: file.name || "这个文件" }));
      continue;
    }
    if (file.size > MAX_SHOT_BYTES) {
      bad.push(tt("{a} 有 {b}MB，超过 6MB", { a: file.name, b: mb(file.size) }));
      continue;
    }
    ok.push(await shotOf(file, ok.length));
  }
  return [ok, bad];
}

/**
 * 读一批**任何类型**的附件: 图按图收(有预览、6MB), 其余按文件收(20MB).
 *
 *	拖一个 zip 进来原来是被拒的 —— "不是图片". 现在文件是一等公民:
 *	落到 OS 那侧的磁盘上, bot 用 read_file 打开.
 */
export async function readAny(files: readonly File[]): Promise<[Shot[], string[]]> {
  const ok: Shot[] = [];
  const bad: string[] = [];
  for (const file of files) {
    if (isShot(file)) {
      if (file.size > MAX_SHOT_BYTES) { bad.push(tt("{a} 有 {b}MB，图最大 6MB", { a: file.name, b: mb(file.size) })); continue; }
      ok.push(await shotOf(file, ok.length));
      continue;
    }
    if (file.size > MAX_FILE_BYTES) {
      bad.push(tt("{a} 有 {b}MB，超过 20MB —— 大文件放进工作区让他直接读", { a: file.name, b: mb(file.size) }));
      continue;
    }
    if (file.size === 0) { bad.push(tt("{a} 是空的", { a: file.name || "这个文件" })); continue; }
    ok.push({
      key: `${file.name}:${file.size}:${file.lastModified}:${ok.length}`,
      name: file.name || t("文件"),
      mime: file.type || "application/octet-stream",
      url: "",
      data: await base64Of(file),
      bytes: file.size,
      file: true
    });
  }
  return [ok, bad];
}

async function shotOf(file: File, index: number): Promise<Shot> {
  return {
    key: `${file.name}:${file.size}:${file.lastModified}:${index}`,
    name: file.name || t("粘贴的图.png"),
    mime: file.type,
    url: URL.createObjectURL(file),
    data: await base64Of(file),
    bytes: file.size
  };
}

function mb(size: number): number {
  return Math.round(size / 1024 / 1024 * 10) / 10;
}

/** FileReader 给的是 data:mime;base64,xxx —— 逗号后面那段才是我们要的 */
function base64Of(file: File): Promise<string> {
  return new Promise((done, fail) => {
    const reader = new FileReader();
    reader.onerror = () => fail(new Error(tt("{a} 读不出来", { a: file.name })));
    reader.onload = () => {
      const text = String(reader.result ?? "");
      done(text.slice(text.indexOf(",") + 1));
    };
    reader.readAsDataURL(file);
  });
}

/** 从一次粘贴/拖拽里挑出图片文件 */
export function filesFrom(data: DataTransfer | null): File[] {
  if (data === null) return [];
  return [...data.files];
}
