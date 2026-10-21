# Web UI

Svelte 5 + Vite management UI, embedded into the Go binary from
`internal/webui/dist` (served at `/ui/`).

```bash
pnpm -C web install
pnpm -C web build   # writes internal/webui/dist, then rebuild the Go binary
pnpm -C web check   # svelte-check (types + a11y)
pnpm -C web test    # vitest unit tests (src/**/*.test.ts)
pnpm -C web lint    # ESLint (typescript-eslint + eslint-plugin-svelte)
pnpm -C web format  # Prettier (format:check in CI)
CLIPROXY_URL=http://127.0.0.1:8317 pnpm -C web dev
```

`pnpm dev` serves the UI on http://localhost:5173/ui/ and proxies `/v8` and
`/v1` to `CLIPROXY_URL`, stripping the `Origin` header so the backend sees
same-origin requests. In dev mode sign in with the management key.
