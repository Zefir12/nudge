import vue from '@vitejs/plugin-vue';
import { defineConfig } from 'vite-plus';

// nudge's web UI. `vp dev` proxies the API to a nudge server on localhost:8080
// (NUDGE_DEV_API to change it); `vp build` writes dist/, which `make web-build` and the
// Dockerfile copy into backend/internal/web/dist for embedding.
const api = process.env.NUDGE_DEV_API ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': { target: api, changeOrigin: false, ws: true },
    },
  },
  build: {
    outDir: 'dist',
    // The server's CSP will allow no inline scripts or styles.
    assetsInlineLimit: 0,
  },
  test: {
    environment: 'jsdom',
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
