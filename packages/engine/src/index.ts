/**
 * @neox-os/engine — 全机唯一的执行引擎.
 *
 *   执行体是轻的: 上下文页表 + 状态 + 能力集 + 一个 ReAct 游标.
 *   引擎是重的、共享的: 页存储 / prompt 组装 / 连接池 / 调度.
 */
export {
  PageStore,
  ContextSpace,
  measureSharing,
  stableStringify,
  PageFault,
  type Page,
  type PageId,
  type PageKind,
} from './pageTable.js';
export {
  assemble,
  planBreakpoint,
  type AssembleResult,
  type AssembleOptions,
} from './assembler.js';
