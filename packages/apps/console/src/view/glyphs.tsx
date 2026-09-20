import type { ReactNode } from "react";

/**
 * glyphs —— **一张卡片长什么脸**.
 *
 * ── 为什么一张通用卡也要有脸 ──
 *
 *	认不出种类的卡片原来是一摞 `键: 值` —— 那是调试输出的样子, 不是
 *	给人看的东西. 而"这是一趟航班"这件事, 一个飞机图形一眼就说完了,
 *	比 `type: flight` 这行字快得多.
 *
 *	种类是开的(bot 可以自己起一种), 所以这里**按词认, 不按枚举认**:
 *	flight / 航班 / 飞机 都落到同一架飞机上. 认不出来的给一个中性的
 *	方块, 而不是硬套一个错的 —— 一张画着飞机的酒店卡比没有图形更糟.
 *
 * ── 为什么是线条图形不是插画 ──
 *
 *	这套界面通篇是 1.4px 的线条(头像、图标、开关都是). 塞一张彩色插画
 *	进来, 它会比周围任何东西都响, 而它承载的信息只有"这是一趟航班".
 *	同一套笔画、跟着主题走的颜色, 才是"精致"该长的样子.
 */

/** 一类东西: 一个名字、一组关键词、一个图形、一个色相 */
interface Glyph {
  readonly key: string;
  readonly words: readonly string[];
  /** 色相 —— 卡头那层薄薄的底色由它来 */
  readonly hue: number;
  /**
   * 摆在行程线上时要转多少度.
   *
   *	飞机是**机头朝上**画的(图标里都这么画), 而行程线是横的 ——
   *	一架朝上的飞机摆在一条朝右的线上, 看着像它要飞走而不是飞过去.
   */
  readonly turn?: number;
  readonly draw: ReactNode;
}

/** 通用的笔画参数 —— 跟界面上别的图标一套 */
const S = { fill: "none", stroke: "currentColor", strokeWidth: 1.4, strokeLinecap: "round", strokeLinejoin: "round" } as const;

const GLYPHS: readonly Glyph[] = [
  {
    key: "plane", hue: 206, turn: 90, words: ["flight", "plane", "air", "航班", "飞机", "机票", "航空"],
    // 飞机是这一套里唯一填实的: 一架侧面的飞机用线条画出来太碎,
    // 15px 上就成了一团 —— 而"这是一趟航班"必须一眼就到
    draw: <path fill="currentColor" d="M14 10.7v-1.3L8.7 6.1V2.3a1 1 0 0 0-2 0v3.8L1.3 9.4v1.3l5.4-1.7v3.7l-1.4 1v1l2.4-.7 2.3.7v-1l-1.3-1V9z" />
  },
  {
    key: "train", hue: 268, words: ["train", "rail", "metro", "subway", "火车", "高铁", "列车", "地铁", "动车"],
    draw: <><rect {...S} x="3.4" y="2.2" width="9.2" height="9.4" rx="2.4" /><path {...S} d="M3.6 7h8.8M5.6 14l1.6-2.4M10.4 14l-1.6-2.4" /><circle cx="6.2" cy="9.4" r=".8" fill="currentColor" stroke="none" /><circle cx="9.8" cy="9.4" r=".8" fill="currentColor" stroke="none" /></>
  },
  {
    key: "car", hue: 24, words: ["car", "taxi", "ride", "drive", "汽车", "打车", "出租", "驾"],
    draw: <><path {...S} d="M2.2 10.4v-1.6l1.4-3.2a1.6 1.6 0 0 1 1.5-1h5.8a1.6 1.6 0 0 1 1.5 1l1.4 3.2v1.6" /><path {...S} d="M2.2 10.4h11.6v2H2.2z" /><circle {...S} cx="4.6" cy="12.4" r="1.1" /><circle {...S} cx="11.4" cy="12.4" r="1.1" /></>
  },
  {
    key: "ship", hue: 190, words: ["ship", "boat", "ferry", "cruise", "船", "邮轮", "轮渡"],
    draw: <><path {...S} d="M2.4 10.2h11.2l-1.6 3.4H4z" /><path {...S} d="M4.4 10.2V6.4h7.2v3.8M8 6.4V3.2" /></>
  },
  {
    key: "hotel", hue: 32, words: ["hotel", "stay", "room", "bed", "酒店", "住宿", "房间", "民宿"],
    draw: <><path {...S} d="M2 12.4V4.6M2 9.2h12v3.2M14 12.4v-2" /><path {...S} d="M4.6 9.2V7.6a1 1 0 0 1 1-1h2.2a1 1 0 0 1 1 1v1.6" /></>
  },
  {
    key: "food", hue: 12, words: ["food", "meal", "restaurant", "eat", "餐", "吃", "菜", "外卖", "美食"],
    draw: <><path {...S} d="M4.4 2.4v5.2a1.6 1.6 0 0 0 3.2 0V2.4M6 7.6v6M11.6 2.4c-1 1-1.4 2.2-1.4 3.6 0 1 .5 1.6 1.4 1.8v5.8" /></>
  },
  {
    key: "money", hue: 145, words: ["money", "price", "cost", "pay", "invoice", "bill", "报销", "费用", "价格", "支付", "账"],
    draw: <><circle {...S} cx="8" cy="8" r="5.8" /><path {...S} d="M8 4.6v6.8M9.8 6.2H7.2a1.3 1.3 0 0 0 0 2.6h1.6a1.3 1.3 0 0 1 0 2.6H6.2" /></>
  },
  {
    key: "chart", hue: 176, words: ["chart", "report", "stat", "metric", "data", "报表", "统计", "数据", "图表"],
    draw: <><path {...S} d="M2.4 13.4h11.2" /><path {...S} d="M4.6 13.4V8M8 13.4V3.6M11.4 13.4V6.4" /></>
  },
  {
    key: "calendar", hue: 348, words: ["calendar", "event", "meeting", "schedule", "日程", "会议", "日历", "安排"],
    draw: <><rect {...S} x="2.4" y="3.4" width="11.2" height="10.2" rx="2" /><path {...S} d="M2.4 6.6h11.2M5.6 2.2v2.4M10.4 2.2v2.4" /></>
  },
  {
    key: "clock", hue: 210, words: ["time", "clock", "timer", "duration", "时间", "倒计时", "耗时"],
    draw: <><circle {...S} cx="8" cy="8" r="5.8" /><path {...S} d="M8 4.6V8l2.4 1.6" /></>
  },
  {
    key: "place", hue: 4, words: ["place", "location", "map", "address", "地点", "位置", "地址", "地图"],
    draw: <><path {...S} d="M8 14s4.6-4.2 4.6-7.4a4.6 4.6 0 1 0-9.2 0C3.4 9.8 8 14 8 14Z" /><circle {...S} cx="8" cy="6.6" r="1.7" /></>
  },
  {
    key: "package", hue: 36, words: ["package", "parcel", "delivery", "shipping", "order", "快递", "包裹", "物流", "订单", "发货"],
    draw: <><path {...S} d="M8 2.4 13.6 5v6L8 13.6 2.4 11V5z" /><path {...S} d="M2.4 5 8 7.8 13.6 5M8 7.8v5.8" /></>
  },
  {
    key: "ticket", hue: 292, words: ["ticket", "pass", "booking", "reservation", "票", "预订", "凭证"],
    draw: <><path {...S} d="M2.4 5.4a1 1 0 0 1 1-1h9.2a1 1 0 0 1 1 1v1.2a1.4 1.4 0 0 0 0 2.8v1.2a1 1 0 0 1-1 1H3.4a1 1 0 0 1-1-1V9.4a1.4 1.4 0 0 0 0-2.8z" /><path {...S} strokeDasharray="1.2 1.6" d="M9.4 4.4v7.2" /></>
  },
  {
    key: "music", hue: 320, words: ["music", "song", "audio", "podcast", "音乐", "歌", "音频", "播客"],
    draw: <><path {...S} d="M6 12V4.2l7-1.4v7.6" /><circle {...S} cx="4.4" cy="12" r="1.6" /><circle {...S} cx="11.4" cy="10.4" r="1.6" /></>
  },
  {
    key: "video", hue: 258, words: ["video", "movie", "film", "视频", "电影", "影片"],
    draw: <><rect {...S} x="2.2" y="4" width="8.6" height="8" rx="1.6" /><path {...S} d="m10.8 8.6 3-2v3z" /></>
  },
  {
    key: "book", hue: 40, words: ["book", "doc", "article", "note", "文档", "书", "文章", "笔记"],
    draw: <><path {...S} d="M3 3.4a1.4 1.4 0 0 1 1.4-1.4H12v12H4.4A1.4 1.4 0 0 1 3 12.6z" /><path {...S} d="M3 11.6h9" /></>
  },
  {
    key: "person", hue: 200, words: ["person", "people", "user", "contact", "member", "人", "联系人", "成员", "用户"],
    draw: <><circle {...S} cx="8" cy="5.6" r="2.6" /><path {...S} d="M3 13.4c.6-2.6 2.6-4 5-4s4.4 1.4 5 4" /></>
  },
  {
    key: "mail", hue: 214, words: ["mail", "email", "message", "邮件", "消息", "私信"],
    draw: <><rect {...S} x="2.2" y="3.6" width="11.6" height="8.8" rx="1.6" /><path {...S} d="m2.6 5 5.4 3.8L13.4 5" /></>
  },
  {
    key: "weather", hue: 198, words: ["weather", "rain", "sun", "temp", "天气", "气温", "下雨"],
    draw: <><circle {...S} cx="6.4" cy="6.4" r="2.4" /><path {...S} d="M8.6 12.6h3.6a2.4 2.4 0 0 0 0-4.8 3.2 3.2 0 0 0-6-.8" /></>
  },
  {
    key: "cart", hue: 130, words: ["cart", "shop", "buy", "product", "商品", "购物", "买"],
    draw: <><path {...S} d="M2.2 3h1.6l1.8 7.4h6.4l1.6-5H5" /><circle {...S} cx="6.4" cy="13" r="1" /><circle {...S} cx="11.4" cy="13" r="1" /></>
  },
  {
    key: "tool", hue: 220, words: ["tool", "build", "deploy", "task", "job", "构建", "部署", "任务", "工具"],
    draw: <><path {...S} d="M10.4 2.6a3.4 3.4 0 0 0-4.2 4.3L2.8 10.3a1.4 1.4 0 0 0 2 2l3.4-3.4a3.4 3.4 0 0 0 4.3-4.2l-2 2-1.7-.4-.4-1.7z" /></>
  },
  {
    key: "star", hue: 44, words: ["star", "rating", "score", "评分", "推荐", "星"],
    draw: <path {...S} d="m8 2.6 1.7 3.5 3.8.5-2.8 2.7.7 3.8L8 11.3l-3.4 1.8.7-3.8-2.8-2.7 3.8-.5z" />
  }
];

/** 认不出是什么的时候: 一个中性的方块, 不硬套一个错的图形 */
const PLAIN: Glyph = {
  key: "plain", hue: 220, words: [],
  draw: <><rect {...S} x="2.6" y="2.6" width="10.8" height="10.8" rx="2.6" /><path {...S} d="M5.4 6.6h5.2M5.4 9.4h3.2" /></>
};

/**
 * glyphOf 这张卡该长哪张脸.
 *
 *	**种类和字段名一起看**: 种类叫 itinerary 认不出来, 但里面有
 *	departure / 航班号 的话, 那就是一趟航班. 卡片是 bot 现编的,
 *	它起的名字五花八门, 而字段名往往更诚实.
 */
export function glyphOf(kind: string, keys: readonly string[] = [], fallback?: string): Glyph {
  const hay = (kind + " " + keys.join(" ")).toLowerCase();
  for (const glyph of GLYPHS) {
    for (const word of glyph.words) {
      if (hay.includes(word)) return glyph;
    }
  }
  // 调用方比这儿多知道一点的时候(比如"这张卡有起点和终点"), 让它兜底
  const asked = GLYPHS.find((one) => one.key === fallback);
  return asked ?? PLAIN;
}

/** 画出来 —— 尺寸交给外面 */
export function Glyph({ glyph, size = 18, moving = false }: {
  glyph: { draw: ReactNode; turn?: number }; size?: number;
  /** 摆在行程线上 —— 该朝着走的方向 */
  moving?: boolean;
}) {
  const turn = moving && glyph.turn !== undefined ? { transform: `rotate(${glyph.turn}deg)` } : undefined;
  return (
    <svg viewBox="0 0 16 16" width={size} height={size} aria-hidden="true" style={turn}>{glyph.draw}</svg>
  );
}
