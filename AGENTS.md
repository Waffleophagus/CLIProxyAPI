# AGENTS.md

Go 1.26+ personal inference proxy providing OpenAI (Chat Completions + Responses) and Claude Messages compatible APIs in front of OAuth accounts, upstream API keys and OpenAI-compatible servers, with round-robin load balancing, cooldowns and retries. Single user, runs in Docker, reached over LAN/Tailscale.

## Repository
- Origin: ssh://git@git.waffleophagus.com:2223/waffleophagus/CLIProxyAPI.git (private Gitea)
- Hard fork of https://github.com/router-for-me/CLIProxyAPI (MIT). Upstream is credited but never merged again.
- Module path stays `github.com/router-for-me/CLIProxyAPI/v8`.

## Hard-fork scope
Do not reintroduce removed subsystems: Google providers (Gemini CLI, Antigravity, AI Studio/wsrelay, Vertex) and the Gemini/Interactions inbound API, the Kimi and Devin providers (and the Interactions translators), CLIProxyAPIHome mode, the native plugin system, Redis queue/port multiplexer, Postgres/git/object-store backends, mDNS discovery, TUI, management.html downloader and GitHub version checks, the `/v0/management` API, and upstream release/registry automation. `/v8/management` is the only management API.

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go vet ./... # Vet
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
docker compose up -d --build # Build and run the local image (cliproxy:local)
```
- Flags: `--config <path>`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`, login flags `--claude-login`, `--codex-login`, `--codex-device-login`, `--siwc-bind <file>`, `--xai-login`, `--meta-login`
- The server has no cgo dependencies; the Docker image builds with `CGO_ENABLED=0`.
- `.github/scripts/refresh-model-catalogs.sh` refreshes the embedded model catalogs in `internal/registry/models/`.

## Config
- Default config: `config.yaml` in the working directory (template: `config.example.yaml`, v8 layout). `internal/config/config_v8_test.go` parses the template, including the commented `api-keys` examples, so keep it valid.
- `.env` is auto-loaded from the working directory (`MANAGEMENT_PASSWORD`).
- Credentials are JSON files under `oauth.auth-dir` (default `~/.cli-proxy-api`; `/root/.cli-proxy-api` in Docker).
- Placeholder client keys (`your-api-key-N`) put the server in safe mode; proxy endpoints refuse requests until they are replaced.

## Architecture
- `cmd/server/` — Server entrypoint and login flags
- `cmd/fetch_codex_models/`, `cmd/validate_codex_models/` — Model catalog maintenance utilities
- `internal/api/` — Gin HTTP server, routes, middleware; `handlers/management/` implements `/v8/management`
- `internal/access/` — Client API-key access providers and reconciliation
- `internal/auth/` — Provider OAuth flows (claude, codex incl. SIWC, xai, meta)
- `internal/cmd/` — CLI login commands and service startup
- `internal/client/` — Client-specific compatibility (Claude models, Codex apply-patch/live/multi-agent/tool-schema, Grok Build)
- `internal/config/` — Config types, v8 layout/migration, load/save with comment preservation
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (Claude, Codex incl. WebSocket/images/live, xAI, Meta, OpenAI-compatible); helpers in `helps/`
- `internal/translator/` — Protocol translators (openai, claude, codex) and shared `common`
- `internal/slugs/` — Follow-mode slugs: polls OpenAI-compatible `/models` and resolves `follow: true` aliases into the effective runtime config (`Service.cfg`; the loaded config stays in `Service.sourceCfg`)
- `internal/tailnet/` — Embedded Tailscale node (`tailscale.com/tsnet`): tailnet listeners served via `api.Server.ServeListener` (requests marked via-tailnet), WhoIs identity for `tailscale.management-users`, node status
- `internal/registry/` — Model registry, embedded catalogs and remote updaters (`StartModelCatalogUpdaters`); `--local-model` disables remote updates
- `internal/pricing/` — Usage cost estimates: models.dev catalog (cached, daily refresh), model→price resolution and cost classes (paid API / subscription / local hardware)
- `internal/cache/` — Thinking/reasoning replay and web-search result caches
- `internal/signature/` — Request signature handling
- `internal/watcher/` — Config/auth hot reload and auth update dispatch
- `internal/logging/` — Logrus setup, request logging, Gin logging middleware
- `internal/safemode/` — Placeholder API-key detection
- `sdk/cliproxy/` — Embeddable service (builder, auth manager/conductor, executors, watcher wiring, usage, sessions)
- `sdk/api/`, `sdk/access/`, `sdk/auth/`, `sdk/config/`, `sdk/translator/`, `sdk/logging/` — Public SDK surfaces (see `docs/sdk-*.md`)
- `test/` — Cross-module integration tests
- `examples/` — SDK usage examples (must compile)
- `docs/` — `management-api-v8.md` and SDK guides

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only; translate any non-English comments you touch
- For user-visible strings, keep the existing language used in that file/area
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Payload configuration MUST be the final semantic barrier before sending requests in every executor, including streaming, WebSocket, continuation, retry/fallback, image, and token-count paths where applicable. Complete all built-in payload translation, normalization, injection, and cleanup first, then evaluate and apply user payload rules exactly once to the final business payload. No subsequent logic may overwrite configured values or restore filtered fields. Only necessary transport framing, serialization, signature calculation, and read-only validation may follow without changing business payload semantics. Add regression tests when introducing or changing request-building paths to enforce this ordering.
- Timeouts are allowed only during credential acquisition and connection establishment (e.g. the opt-in `requests.tls-handshake-timeout-seconds` uTLS handshake bound); after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_*.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, the follow-mode slug `/models` probe timeout in `internal/slugs` (a control-plane poll, never inference traffic), and the `cmd/fetch_*_models` utility timeouts.
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.
- New features that touch management go into `/v8/management` only.

### Web UI (`web/`)
- Svelte 5 (runes) + Vite + TypeScript, plain Svelte with a small custom router (not SvelteKit); pnpm only. `pnpm -C web build` writes `internal/webui/dist` (embedded via `go:embed`).
- Use the official Svelte MCP server (registered in `.mcp.json` as `svelte`): `list-sections`/`get-documentation` for Svelte 5 docs, and run `svelte-autofixer` on every component you change until it reports no issues. Without MCP access, use the CLI: `npx -y @sveltejs/mcp svelte-autofixer <file>`.
- Before committing web changes run `pnpm -C web lint && pnpm -C web check && pnpm -C web test`; format with `pnpm -C web format` (CI runs `pnpm -C web format:check` and `pnpm -C web build`).
