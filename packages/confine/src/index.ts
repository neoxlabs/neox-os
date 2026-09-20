/**
 * @neox-os/confine — 能力 → 内核强制约束.
 *
 *   三步, 前两步是纯函数 (任何平台可测), 第三步 Linux-only:
 *     1. planFor()          能力集 → ConfinementPlan
 *     2. buildLaunch()      plan → 启动命令行
 *     3. probeEnforcement() 内核能不能真的执行. 不能就 fail-closed
 */

export {
  scopeMatches,
  pathWithin,
  hostMatches,
  normalizePath,
  splitHostPort,
} from './scope.js';
export {
  planFor,
  resolveInVolume,
  resolveRuntimeFs,
  DEFAULT_RUNTIME_FS,
  type PlanOptions,
} from './plan.js';
export { probeEnforcement, type ProbeDeps } from './probe.js';
export {
  buildLaunch,
  DEFAULT_PATHS,
  type LaunchPlan,
  type LauncherPaths,
} from './launcher.js';
