import { defineConfig } from 'vitest/config';
import { fileURLToPath } from 'node:url';

export default defineConfig({
  resolve: {
    alias: {
      '@neox-os/abi': fileURLToPath(new URL('./packages/abi/src/index.ts', import.meta.url)),
      '@neox-os/boot': fileURLToPath(new URL('./packages/boot/src/index.ts', import.meta.url)),
      '@neox-os/confine': fileURLToPath(new URL('./packages/confine/src/index.ts', import.meta.url)),
      '@neox-os/engine': fileURLToPath(new URL('./packages/engine/src/index.ts', import.meta.url)),
      '@neox-os/init': fileURLToPath(new URL('./packages/init/src/index.ts', import.meta.url)),
    },
  },
  test: {
    // apps 底下多一层(packages/apps/<app>/src), 上面那条 glob 够不着 ——
    // 于是 console 那边写了测试也永远不会被跑到
    include: [
      'packages/*/src/**/__tests__/**/*.test.ts',
      'packages/apps/*/src/**/__tests__/**/*.test.ts', 'packages/apps/*/src/**/__tests__/**/*.test.tsx',
    ],
    environment: 'node',
  },
});
