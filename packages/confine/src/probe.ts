/**
 * probe — 探测当前机器是否具备并接入强制约束能力.
 *
 *   两条铁律:
 *
 *   1. **Fail-closed**. 探测不过 → confined 模式拒绝启动, 不降级不 warning.
 *      一个自称能隔离而实际没隔离的 OS, 比明确不能隔离的 OS 危险得多.
 *
 *   2. **只支持不接入 = 不算数**. 内核有某个机制, 但我们没写对接代码,
 *      那它对我们就是不存在的. 把它算进"可用"就是 fail-open —— 我们会
 *      以为自己在强制, 实际什么都没做. 这类机制只进 detectedNotWired 供诊断.
 *
 *   诊断信息只记录检测到但尚未接入的机制: 内核启用 bpf-lsm 但没有对应
 *   下发路径时, 它不能计入可用能力; cgroup2、netns 和含 user_notif 的
 *   seccomp 只有在对应接入路径存在时才计入.
 */

import { existsSync, readFileSync } from 'node:fs';
import type {
  EnforcementMechanism,
  EnforcementProbe,
  EnforcementRequirement,
} from '@neox-os/abi';
import { DEFAULT_PATHS, type LauncherPaths } from './launcher.js';

export interface ProbeDeps {
  platform: string;
  fileExists: (p: string) => boolean;
  readFile: (p: string) => string;
  paths: LauncherPaths;
}

const realDeps: ProbeDeps = {
  platform: process.platform,
  fileExists: existsSync,
  readFile: (p) => readFileSync(p, 'utf8'),
  paths: DEFAULT_PATHS,
};

/**
 * 需求 → 能满足它的机制 (任一即可).
 *   bpf-lsm 列在 fs-enforce 下是**未来**的事 —— 目前它进不了 mechanisms,
 *   因为 buildLaunch 还没有对应的下发路径.
 */
const SATISFIED_BY: Record<EnforcementRequirement, EnforcementMechanism[]> = {
  'fs-enforce': ['landlock', 'bpf-lsm'],
  'resource-limit': ['cgroup2'],
  'net-isolate': ['netns'],
  'syscall-notify': ['seccomp-user-notif'],
};

/** 我们已经写了下发代码的机制. 不在这里 = 探到了也不算数 */
const WIRED: readonly EnforcementMechanism[] = ['landlock', 'cgroup2', 'netns'];

export function probeEnforcement(
  required: readonly EnforcementRequirement[],
  deps: ProbeDeps = realDeps,
): EnforcementProbe {
  if (deps.platform !== 'linux') {
    return {
      platform: deps.platform,
      mechanisms: [],
      detectedNotWired: [],
      satisfied: [],
      missing: [...required],
      usable: false,
      reason: `强制约束只在 Linux 上可用, 当前是 ${deps.platform}`,
    };
  }

  const present: EnforcementMechanism[] = [];
  for (const m of ALL_MECHANISMS) if (detect(m, deps)) present.push(m);

  const mechanisms = present.filter((m) => WIRED.includes(m));
  const detectedNotWired = present.filter((m) => !WIRED.includes(m));

  const satisfied: EnforcementRequirement[] = [];
  const missing: EnforcementRequirement[] = [];
  for (const r of required) {
    (SATISFIED_BY[r].some((m) => mechanisms.includes(m)) ? satisfied : missing).push(r);
  }

  return {
    platform: deps.platform,
    mechanisms,
    detectedNotWired,
    satisfied,
    missing,
    usable: missing.length === 0,
    reason: missing.length ? explain(missing, detectedNotWired) : undefined,
  };
}

const ALL_MECHANISMS: EnforcementMechanism[] = [
  'landlock',
  'bpf-lsm',
  'cgroup2',
  'netns',
  'seccomp',
  'seccomp-user-notif',
];

function detect(m: EnforcementMechanism, deps: ProbeDeps): boolean {
  switch (m) {
    case 'landlock':
      /* 内核支持 + 启动器二进制在位. 少一个都强制不了 */
      return (
        (deps.fileExists('/sys/kernel/security/landlock/abi_version') ||
          lsm(deps).includes('landlock')) &&
        deps.fileExists(deps.paths.landlockRun)
      );
    case 'bpf-lsm':
      return lsm(deps).includes('bpf');
    case 'cgroup2':
      /* 只需要 v2 统一层级 + 一个 POSIX shell.
       * 原来还要求 cgexec —— 那是 cgroup v1 的工具, 在 v2 下根本不工作,
       * 要求它等于要求一个用不上的依赖. */
      return (
        deps.fileExists(`${deps.paths.cgroupRoot}/cgroup.controllers`) &&
        deps.fileExists(deps.paths.sh)
      );
    case 'netns':
      return deps.fileExists('/proc/self/ns/net') && deps.fileExists(deps.paths.unshare);
    case 'seccomp':
      return deps.fileExists('/proc/sys/kernel/seccomp/actions_avail');
    case 'seccomp-user-notif':
      /* 判据是内核**支持**哪些 action, 不是"我自己有没有被限制".
       * /proc/self/status 的 Seccomp: 位表示当前进程是否已受限,
       * 不能证明 user_notif 这个 action 可用. */
      return safeRead(deps, '/proc/sys/kernel/seccomp/actions_avail').includes('user_notif');
  }
}

function explain(
  missing: readonly EnforcementRequirement[],
  notWired: readonly EnforcementMechanism[],
): string {
  const base = `无法满足: ${missing.join(', ')}`;
  const hint = notWired.length
    ? ` (内核有 ${notWired.join('/')} 但我们尚未接入, 不能算数)`
    : '';
  return base + hint;
}

function lsm(deps: ProbeDeps): string {
  return safeRead(deps, '/sys/kernel/security/lsm');
}

function safeRead(deps: ProbeDeps, p: string): string {
  try {
    return deps.readFile(p);
  } catch {
    return '';
  }
}
