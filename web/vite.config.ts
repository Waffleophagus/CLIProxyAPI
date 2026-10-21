/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { fileURLToPath } from 'node:url';

import type { ProxyOptions } from 'vite';

// `pnpm dev` proxies API calls to a locally running proxy. Override the target
// with CLIPROXY_URL (default http://127.0.0.1:8317). Dev mode signs in with the
// management key (tailnet identity auth only applies to same-origin requests on
// the tailnet listener).
const target = process.env.CLIPROXY_URL ?? 'http://127.0.0.1:8317';

// The backend must see the request as same-origin: rewrite Host and drop the
// dev server's Origin header.
const proxy: ProxyOptions = {
  target,
  changeOrigin: true,
  configure: (p) => {
    p.on('proxyReq', (req) => req.removeHeader('origin'));
  }
};

export default defineConfig({
  base: '/ui/',
  plugins: [svelte()],
  build: {
    outDir: fileURLToPath(new URL('../internal/webui/dist', import.meta.url)),
    // scripts/clean-dist.mjs empties it but keeps the committed .keep file.
    emptyOutDir: false,
    assetsInlineLimit: 0,
    target: 'es2022',
    sourcemap: false
  },
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node'
  },
  server: {
    port: 5173,
    proxy: {
      '/v8': proxy,
      '/v1': proxy,
      '/healthz': proxy
    }
  }
});
