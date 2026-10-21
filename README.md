# CLIProxyAPI (personal fork)

A lean, single-user LLM inference proxy. It exposes OpenAI-, Responses- and
Claude-compatible APIs on one port and routes requests across OAuth accounts,
upstream API keys and OpenAI-compatible servers, with round-robin load
balancing, cooldowns and retries. It is meant to run in Docker and be reached
over a LAN or Tailscale.

This is a hard fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
by Luis Pater and the Router-For.ME contributors, released under the MIT license
(see [LICENSE](LICENSE)). All credit for the original project goes to them;
this fork removes features its owner does not use and does not track upstream.

## Supported upstreams

| Provider | OAuth login | API keys (`api-keys.*`) |
| --- | --- | --- |
| Claude (Anthropic) | yes | yes, including Messages-compatible gateways |
| Codex (ChatGPT / OpenAI) | yes, incl. device code and Sign in with ChatGPT binding | yes |
| xAI (Grok), incl. video and TTS | yes | yes |
| Kimi (kimi.com / kimi.ai) | yes | via Claude-compatible keys |
| Meta | yes | yes |
| Devin | yes | no |
| Any OpenAI-compatible server (vLLM, SGLang, llama.cpp, OpenRouter, ...) | n/a | `api-keys.openai-compatibility` |

## Inbound APIs

All on port `8317`, authenticated with a key from `access.api-keys`:

- OpenAI: `/v1/chat/completions`, `/v1/completions`, `/v1/models`,
  `/v1/images/{generations,edits}`, `/v1/audio/speech`, `/v1/realtime*`
- OpenAI Responses: `/v1/responses` (HTTP and WebSocket), `/v1/responses/compact`
- Claude Messages: `/v1/messages`, `/v1/messages/count_tokens`
- xAI video/TTS: `/v1/videos*`, `/v1/tts`; OpenAI-style video at `/openai/v1/videos*`
- Codex direct: `/backend-api/codex/responses`
- `GET /healthz` (no auth)

## Quick start (Docker)

```bash
cp config.example.yaml config.yaml
# Edit config.yaml: replace the access.api-keys placeholders (the server refuses
# proxy requests while they are present), set management.secret-key to a strong
# secret, and set management.allow-remote: true (see below).
mkdir -p auths logs data
docker compose up -d --build
curl http://localhost:8317/healthz
```

`docker-compose.yml` builds the image locally (`cliproxy:local`) and mounts
`./config.yaml`, `./auths` (credential files), `./logs` and `./data` (the
usage database, `data/usage.db`). It also publishes the
Claude (`54545`) and Codex (`1455`) OAuth callback ports. Set `TZ` to change the
container timezone (default UTC).

Management access in Docker: only `127.0.0.1`/`::1` count as local, and through
Docker's published ports even requests from the Docker host itself arrive from
the bridge gateway address, so they are remote. With only
`management.secret-key` set and `allow-remote: false`, every management request
returns `403`. Either set `management.allow-remote: true` with a strong
`secret-key`, or set `MANAGEMENT_PASSWORD` in the container environment (for
example via `.env` and the commented `env_file` in `docker-compose.yml`), which
always enables remote management regardless of `allow-remote`. Either way the
management API, which can read and write credentials and configuration, is then
reachable by anyone who can reach port `8317` and knows the key, so keep the
port on a trusted network (LAN/Tailscale) and use a long random key.

Without Docker:

```bash
go build -o cli-proxy-api ./cmd/server
./cli-proxy-api --config config.yaml
```

Clients then use `http://<host>:8317/v1` as the base URL and one of your
`access.api-keys` as the API key.

## Adding accounts

### OAuth from the command line

Run the binary with a login flag (inside Docker:
`docker compose exec cli-proxy-api ./CLIProxyAPI <flag> --no-browser`). The
resulting credential file is written to `oauth.auth-dir` and picked up by a
running server automatically.

| Flag | Provider |
| --- | --- |
| `--claude-login` | Claude |
| `--codex-login` | Codex (browser) |
| `--codex-device-login` | Codex (device code) |
| `--siwc-bind <file>` | Bind Sign in with ChatGPT to an existing Codex credential file |
| `--xai-login` | xAI |
| `--kimi-login` / `--kimi-ai-login` | Kimi (kimi.com / kimi.ai) |
| `--meta-login` | Meta |
| `--devin-login` | Devin |

Modifiers: `--no-browser` (print the URL instead of opening a browser),
`--oauth-callback-port <port>` (override the callback port). Other flags:
`--config <path>`, `--local-model` (use embedded model catalogs only).

### OAuth through the management API

With management enabled (see the Docker note above for remote access):

```bash
curl -H "Authorization: Bearer $MGMT_KEY" \
  "http://<host>:8317/v8/management/oauth/auth-url?provider=claude&is_webui=true"
```

Open the returned `url`, finish the login, then poll
`/v8/management/oauth/status?state=<state>` until it reports `ok` (`wait` means
still pending). Providers: `claude`, `codex`, `kimi`, `kimi-ai`, `xai`, `meta`,
`devin`.

- `kimi`, `kimi-ai`, `xai` and `meta` use a device-code flow: open the URL
  (the response may also include a `user_code`); no callback is involved.
- `claude` and `codex` redirect the browser to `http://localhost:54545/callback`
  and `http://localhost:1455/auth/callback`. With `is_webui=true` the server
  starts a temporary callback forwarder on that port (Claude `54545`, Codex
  `1455`), which redirects the browser to
  `http://127.0.0.1:<server.port>/{anthropic,codex}/callback`. Without
  `is_webui=true` no listener is started. In Docker the forwarder ports must be
  published (`docker-compose.yml` does this), and the forwarder only helps when
  the browser runs on the Docker host and can also reach the proxy at
  `127.0.0.1:<server.port>`.
- `devin` redirects to `http://127.0.0.1:<server.port>/callback`, so it
  completes directly when the browser is on the proxy host.

When the browser is not on the proxy host, the final redirect to
`localhost`/`127.0.0.1` fails to load. Copy the full URL from the address bar
and submit it (no management key needed; it is matched to the pending login by
its `state`):

```bash
curl -X POST "http://<host>:8317/v8/management/oauth/callback" \
  -H "Content-Type: application/json" \
  -d '{"redirect_url": "http://localhost:54545/callback?code=...&state=..."}'
```

### API keys and local servers

Add upstream keys and OpenAI-compatible servers under `api-keys` in
`config.yaml`; see the commented examples in
[config.example.yaml](config.example.yaml), including an
`openai-compatibility` group for a local inference server.

## Tailscale

The proxy can join your tailnet as its own machine using the embedded
[tsnet](https://tailscale.com/kb/1244/tsnet) library: no tailscaled sidecar,
host networking or extra container capabilities. The LAN port keeps working.

```yaml
tailscale:
  enabled: true
  hostname: cliproxy
  management-users: ["you@github"] # optional
```

- **Joining**: set `tailscale.auth-key` (or the `TS_AUTHKEY` env var) to an auth
  key from the admin console, or leave it empty and open the login URL that is
  logged once at warn level (also returned by
  `GET /v8/management/tailscale/status`). The node state, including its key,
  lives in `data/tailscale` (the Docker data volume); the auth key is only
  needed for the first join.
- **HTTPS**: with `https: true` (default) the proxy serves tailnet port 443 with
  an auto-provisioned `*.ts.net` certificate. Enable MagicDNS and HTTPS
  certificates on the DNS page of the tailnet admin console. Clients then use
  `https://cliproxy.<tailnet>.ts.net/v1` with a key from `access.api-keys`.
  `http-port` adds a plain HTTP listener on the tailnet.
- **Management by identity**: tailnet users listed in `management-users` may call
  `/v8/management` over the tailnet without the management key (tagged devices
  never qualify). This works even without `management.secret-key`; LAN access
  still needs the key. Identity only counts for same-origin or non-browser
  requests addressed to the node's own name or IP; cross-origin pages need the
  key, and the management API sends no CORS headers (see
  [docs/management-api-v8.md](docs/management-api-v8.md)). Inference endpoints
  always require an API key.
- **Logs**: the node's backend logs go only to the proxy log (redacted);
  Tailscale's log upload (logtail) is disabled, and `TS_DEBUG_REGISTER` is
  ignored because it would log the auth key.

Changing tailscale settings (other than `management-users`) requires a restart.

## Management API

`/v8/management` is the only management API: read and edit the configuration
(JSON or YAML, hot-reloaded), manage credential files, run OAuth logins, reset
cooldowns, read logs, and query usage history (requests, tokens, latency and
errors per model, credential and client key, recorded in SQLite under
`observability.usage`). See [docs/management-api-v8.md](docs/management-api-v8.md).
It is disabled unless `management.secret-key` or `MANAGEMENT_PASSWORD` is set.
Only localhost clients may call it unless `management.allow-remote: true` is set;
setting `MANAGEMENT_PASSWORD` always allows remote access, regardless of
`allow-remote`.

## Web UI

The binary serves a management UI at `http://<host>:8317/ui/` (browsers opening
`/` are redirected there): health overview, slugs, accounts and OAuth sign-in,
client keys, usage explorer, logs and settings. The page itself is public; every
action uses the management API, so sign in with the management key (or
automatically as a `tailscale.management-users` user over the tailnet). Set
`management.disable-ui: true` to turn it off. The UI source is in [web/](web/)
(Svelte, built with pnpm); `pnpm -C web build` writes `internal/webui/dist`,
which `go build` embeds. Without that step the binary serves a "UI not built"
placeholder. The Docker image builds it automatically.

## CI/CD

[.github/workflows/ci.yml](.github/workflows/ci.yml) runs on Forgejo Actions
(and unchanged on GitHub Actions) for pull requests, pushes to `main` and `v*`
tags, and manual runs:

- **go**: gofmt check, `go build`, `go vet`, `go test`.
- **web**: ESLint, Prettier check, svelte-check, vitest, production build.
- **image**: builds the Docker image, starts it with the example config and
  checks `/healthz`, `/ui/` and that `/v1/models` refuses unauthenticated calls.

Pushes to `main` skip **go** and **web**, since the pull request already ran
them and the image build compiles both. Pull requests never write to the
registry. When publishing, the image is built once, smoke-tested, and that same
digest is then tagged.

Images are published to the forge's container registry
(`<forge-host>/<owner>/cliproxyapi`) when the repository secrets
`REGISTRY_USERNAME` and `REGISTRY_TOKEN` (a token with package write access)
are set: pushes to `main` publish `latest` and `sha-<commit>`, `v*` tags publish
the tag name, and a manual run on any other branch publishes `branch-<name>`.
Without the secrets the image is still built and tested, just not pushed.

## Embedding

The proxy can be embedded as a Go library (`sdk/cliproxy`); see
[docs/sdk-usage.md](docs/sdk-usage.md), [docs/sdk-advanced.md](docs/sdk-advanced.md),
[docs/sdk-access.md](docs/sdk-access.md) and [examples/](examples/).

## Removed from upstream

- Google providers: Gemini CLI, Antigravity, AI Studio, Vertex, and the
  Gemini/Interactions inbound APIs
- CLIProxyAPIHome control-plane mode and the native plugin system
- Redis queue / port multiplexer, Postgres, git and object-store backends
  (credentials are plain files in `oauth.auth-dir`)
- mDNS discovery, the terminal UI, the downloaded management web page (replaced
  by the embedded UI above) and GitHub update checks
- The legacy `/v0/management` API
- Upstream release, Docker Hub and project-management automation
