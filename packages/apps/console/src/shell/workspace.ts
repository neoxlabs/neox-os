/**
 * workspace — 一个房间"在哪儿干活".
 *
 *   ── 为什么要单独一个 ──
 *
 *   这件事原来有**三处各自的实现**, 而且答案可以不一样:
 *
 *     projectOf     侧栏按项目分组时用
 *     roomwork      新建 bot 时预填工作区用(R90)
 *     roommate      拉人进来时判断"他在不在一处"用(R98)
 *
 *   前两处取的都是"第一个有工作区的成员" —— 而房间成员**可以散在不同
 *   工作区**(拉人进房间只改房间标签, 不动工作区). 于是答案取决于成员
 *   顺序, 而成员顺序跨重启会变(pid 全变).
 *
 *   同一个问题三个答案, 而且都会变 —— 这不是三个 bug, 是一个概念没有
 *   落到一处.
 *
 *   ── 判据 ──
 *
 *   **人数多的那个**. 票数相同按路径定 —— 谁赢不重要, 同样的输入给
 *   同样的答案才重要.
 */

/** 系统分的匿名目录: 一人一个, 从来不是"大家一起干的那摊活" */
function anonymous(work: string): boolean {
  return work.includes("/.neox-os/work/");
}

export function dominantWork(members: readonly { readonly work?: string }[]): string | undefined {
  const votes = new Map<string, number>();
  for (const member of members) {
    const work = member.work;
    if (work === undefined || work === "" || anonymous(work)) continue;
    const path = work.replace(/\/+$/, "");
    votes.set(path, (votes.get(path) ?? 0) + 1);
  }
  let best: string | undefined;
  let most = 0;
  for (const [path, count] of [...votes].sort((a, b) => a[0].localeCompare(b[0]))) {
    if (count > most) { best = path; most = count; }
  }
  return best;
}
