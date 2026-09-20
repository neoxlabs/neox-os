/**
 * main — 镜像的 ENTRYPOINT. 容器里这是 PID 1.
 *
 *   只做一件事: 读 env → boot(). 任何"启动前的小聪明"都不该加在这里,
 *   否则自检就不是第一件事了.
 */
import { boot } from './init.js';
import type { OsMode } from '@neox-os/abi';

const mode = (process.env.NEOX_OS_MODE ?? 'confined') as OsMode;
const volumeRoot = process.env.NEOX_OS_VOLUME ?? '/work';

boot({ mode, volumeRoot }).catch((err: Error) => {
  /* 自检失败就是启动失败. 非 0 退出, 让编排层看得见, 而不是起一个残废的 OS. */
  process.stderr.write(`[boot] 启动失败: ${err.message}\n`);
  process.exit(1);
});
