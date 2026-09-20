/**
 * frames — 帧耗时采样.
 *
 *   "不卡顿"必须是量出来的.
 *   看 fps 会骗人 (空闲时也是 60), 真正该看的是 **长帧**:
 *   一帧超过 50ms 用户就感觉到顿了, 超过 16.7ms 就掉帧.
 */

export interface FrameStats {
  fps: number;
  p95: number;
  worst: number;
  longFrames: number;
  samples: number;
}

export function startFrameMeter(onSample: (stats: FrameStats) => void): () => void {
  const window_ = 120;
  const durations: number[] = [];
  let last = performance.now();
  let longFrames = 0;
  let samples = 0;
  let raf = 0;
  let reportAt = last;

  const tick = (now: number) => {
    const delta = now - last;
    last = now;
    samples += 1;
    if (delta > 50) longFrames += 1;
    durations.push(delta);
    if (durations.length > window_) durations.shift();
    if (now - reportAt > 500) {
      reportAt = now;
      const sorted = [...durations].sort((a, b) => a - b);
      const p95 = sorted[Math.floor(sorted.length * 0.95)] ?? 0;
      const worst = sorted[sorted.length - 1] ?? 0;
      const mean = durations.reduce((a, b) => a + b, 0) / Math.max(1, durations.length);
      onSample({ fps: Math.round(1000 / Math.max(1, mean)), p95: Math.round(p95), worst: Math.round(worst), longFrames, samples });
    }
    raf = requestAnimationFrame(tick);
  };
  raf = requestAnimationFrame(tick);
  return () => cancelAnimationFrame(raf);
}
