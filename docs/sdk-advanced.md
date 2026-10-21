# SDK Advanced: Executors & Translators

This guide explains how to extend the embedded proxy with custom providers and schemas. You will:
- Implement a provider executor that talks to your upstream API
- Register request/response translators for schema conversion
- Register models so they appear in `/v1/models`

[`examples/custom-provider`](../examples/custom-provider/main.go) is a compiling version of everything below.

## Concepts

- Provider executor: a runtime component implementing `auth.ProviderExecutor` that performs outbound calls for a provider key (e.g. `claude`, `codex`, `xai`). Executors can also implement `PrepareRequest` to inject credentials into raw HTTP requests.
- Translator registry: schema conversion functions routed by `sdk/translator`. The built-in translators convert between the OpenAI Chat Completions, OpenAI Responses, Claude Messages, and Codex formats; you can register new ones. Inbound formats are identified by the `sdktr.Format*` constants (`FormatOpenAI` = `"openai"` for Chat Completions, `FormatOpenAIResponse` = `"openai-response"`, `FormatClaude` = `"claude"`, `FormatCodex` = `"codex"`).
- The manager does not translate payloads for an executor. `req.Payload` is the inbound request in `opts.SourceFormat`; the executor translates it to its provider format and translates the response back to `clipexec.ResponseFormatOrSource(opts)`, as the built-in executors do.
- Model registry: publishes the available models per credential/provider to power `/v1/models` and routing.

## 1) Implement a Provider Executor

```go
package myprov

import (
  "context"
  "errors"
  "net/http"

  coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
  clipexec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
  sdktr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type Executor struct{}

func (Executor) Identifier() string { return "myprov" }

// Optional: mutate outbound HTTP requests with credentials.
func (Executor) PrepareRequest(req *http.Request, a *coreauth.Auth) error {
  // req.Header.Set("Authorization", "Bearer "+a.Attributes["api_key"])
  return nil
}

func (Executor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
  // req.Payload is the untranslated inbound request (opts.SourceFormat).
  translated := sdktr.TranslateRequest(opts.SourceFormat, FMyProv, req.Model, req.Payload, false)
  upstream := []byte(`{"ok":true}`) // send translated upstream and read the response here
  var param any
  out := sdktr.TranslateNonStream(ctx, FMyProv, clipexec.ResponseFormatOrSource(opts), req.Model, opts.OriginalRequest, translated, upstream, &param)
  return clipexec.Response{Payload: out}, nil
}

func (Executor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
  translated := sdktr.TranslateRequest(opts.SourceFormat, FMyProv, req.Model, req.Payload, true)
  responseFormat := clipexec.ResponseFormatOrSource(opts)
  ch := make(chan clipexec.StreamChunk, 1)
  go func() {
    defer close(ch)
    var param any
    event := []byte("data: {\"done\":true}\n\n") // one upstream SSE event
    for _, chunk := range sdktr.TranslateStream(ctx, FMyProv, responseFormat, req.Model, opts.OriginalRequest, translated, event, &param) {
      ch <- clipexec.StreamChunk{Payload: chunk}
    }
  }()
  return &clipexec.StreamResult{Chunks: ch}, nil
}

func (Executor) CountTokens(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
  return clipexec.Response{}, errors.New("not implemented")
}

func (Executor) HttpRequest(ctx context.Context, a *coreauth.Auth, req *http.Request) (*http.Response, error) {
  return http.DefaultClient.Do(req.WithContext(ctx))
}

func (Executor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
  return a, nil // refresh tokens here if needed
}
```

Register the executor with the core manager before starting the service:

```go
core := coreauth.NewManager(tokenStore, nil, nil)
core.RegisterExecutor(myprov.Executor{})
svc, _ := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(cfgPath).WithCoreAuthManager(core).Build()
```

Auth entries whose provider is `"myprov"` are routed to this executor.

## 2) Register Translators

To support a new provider format, register translation functions in the `sdk/translator` default registry. Registering only makes them available; your executor applies them (section 1).

Direction matters:
- `Register(from, to, ...)` takes the inbound schema as `from` and the provider schema as `to`.
- Request: `sdktr.TranslateRequest(inbound, provider, ...)`
- Response: `sdktr.TranslateNonStream` / `sdktr.TranslateStream(ctx, provider, inbound, ...)` (arguments reversed)

```go
package myprov

import (
  "context"

  sdktr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// FMyProv is your provider's schema; any unique name works.
const FMyProv = sdktr.Format("myprov.chat")

func init() {
  // sdktr.FormatOpenAI ("openai") is the /v1/chat/completions inbound format.
  sdktr.Register(sdktr.FormatOpenAI, FMyProv,
    // Request transform
    func(model string, raw []byte, stream bool) ([]byte, error) { return convertOpenAIToMyProv(model, raw, stream) },
    // Response transforms
    sdktr.ResponseTransform{
      Stream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) [][]byte {
        return convertStreamMyProvToOpenAI(model, originalReq, translatedReq, raw)
      },
      NonStream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) []byte {
        return convertMyProvToOpenAI(model, originalReq, translatedReq, raw)
      },
    },
  )
}
```

These transforms run only when your executor calls `TranslateRequest` / `TranslateNonStream` / `TranslateStream` as in section 1. If no transform is registered for a pair, the payload passes through unchanged (only the request `model` field is rewritten), so register a pair for every inbound format you accept (for example `sdktr.FormatOpenAIResponse` or `sdktr.FormatClaude`) or reject the others in the executor. [`examples/translator`](../examples/translator/main.go) shows how to call the built-in translators directly.

## 3) Register Models

Expose models under `/v1/models` by registering them in the global model registry with the auth ID and provider name:

```go
models := []*cliproxy.ModelInfo{
  {ID: "myprov-pro-1", Object: "model", Type: "myprov", DisplayName: "MyProv Pro 1"},
}
cliproxy.GlobalModelRegistry().RegisterClient(authID, "myprov", models)
```

The service does this automatically for built-in providers; for custom providers, register during startup (e.g. in `Hooks.OnAfterStart`) or when auths are registered.

## Credentials & Transports

- `Manager.SetRoundTripperProvider` injects a per-auth `http.RoundTripper` (e.g. a proxy). `Builder.Build()` installs the default provider (which honors each credential's `proxy_url`), so call it after `Build()`.
- For raw HTTP flows, implement `PrepareRequest` and/or call `Manager.InjectCredentials(req, authID)`. [`examples/http-request`](../examples/http-request/main.go) demonstrates `Manager.HttpRequest`.

## Testing Tips

- Toggle request logging with `PATCH /v8/management/config` and `{"observability":{"logs":{"request-log":true}}}`.
- Toggle debug logs the same way with `observability.logs.debug`.
- Changes to `config.yaml` and the auth directory are hot-reloaded by the watcher.
