import vue from '@vitejs/plugin-vue';
import { defineConfig } from 'vite-plus';

import { mockApi } from './mock/api';

// nudge's web UI. `vp dev` proxies the API to a nudge server on localhost:8080
// (NUDGE_DEV_API to change it); `vp build` writes dist/, which `make web-build` and the
// Dockerfile copy into backend/internal/web/dist for embedding. NUDGE_DEV_MOCK=1 answers
// the API from fake data in mock/ instead (`make web-mock`).
const api = process.env.NUDGE_DEV_API ?? 'http://localhost:8080';
const mock = process.env.NUDGE_DEV_MOCK === '1';

export default defineConfig({
  plugins: [vue(), ...(mock ? [mockApi()] : [])],
  server: {
    proxy: mock ? {} : { '/api': { target: api, changeOrigin: false, ws: true } },
  },
  build: {
    outDir: 'dist',
    // The server's CSP will allow no inline scripts or styles.
    assetsInlineLimit: 0,
  },
  test: {
    environment: 'jsdom',
    // One jsdom per worker instead of per file (it was ~60% of the run); files stay isolated.
    pool: 'vmThreads',
    include: ['src/**/*.test.ts'],
  },
  lint: {
    ignorePatterns: ['dist/**', 'node_modules/**'],
  },
  fmt: {
    ignorePatterns: ['dist/**', 'node_modules/**', 'package-lock.json'],
    singleQuote: true,
    semi: true,
    printWidth: 110,
  },
});
