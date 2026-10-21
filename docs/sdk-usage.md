# CLI Proxy SDK Guide

The `sdk/cliproxy` package exposes the proxy as a Go library so other programs can embed the routing, authentication, hot-reload, and translation layers without running the CLI binary. A complete, compiling example lives in [`examples/custom-provider`](../examples/custom-provider/main.go).

## Import

The module path is `github.com/router-for-me/CLIProxyAPI/v8`.

```go
import (
    "context"
    "errors"

    "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
    "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)
```

## Minimal Embed

```go
cfg, err := config.LoadConfig("config.yaml")
if err != nil { return err }

svc, err := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml"). // absolute or working-dir relative
    Build()
if err != nil { return err }

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
    return err
}
```

The service manages config/auth watching, background token refresh, and graceful shutdown. Cancel the context to stop it.

## Server Options (middleware, routes, logs)

Server options live in `sdk/api` and are passed through `WithServerOptions`:

```go
import (
    "github.com/router-for-me/CLIProxyAPI/v8/sdk/api"
    "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
    "github.com/router-for-me/CLIProxyAPI/v8/sdk/logging"
)

svc, _ := cliproxy.NewBuilder().
  WithConfig(cfg).
  WithConfigPath("config.yaml").
  WithServerOptions(
    // Add global middleware
    api.WithMiddleware(func(c *gin.Context) { c.Header("X-Embed", "1"); c.Next() }),
    // Tweak the gin engine early (CORS, etc.)
    api.WithEngineConfigurator(func(e *gin.Engine) { /* ... */ }),
    // Add your own routes after the defaults (/healthz is already registered)
    api.WithRouterConfigurator(func(e *gin.Engine, _ *handlers.BaseAPIHandler, _ *config.Config) {
      e.GET("/embed/ping", func(c *gin.Context) { c.String(200, "ok") })
    }),
    // Override the request log writer/dir
    api.WithRequestLoggerFactory(func(cfg *config.Config, cfgPath string) logging.RequestLogger {
      return logging.NewFileRequestLoggerWithOptions(true, "logs", filepath.Dir(cfgPath), cfg.ErrorLogsMaxFiles)
    }),
  ).
  Build()
```

## Management API (when embedded)

- `/v8/management` is mounted only when `management.secret-key` is set in `config.yaml` (or `MANAGEMENT_PASSWORD` is in the environment).
- With only `management.secret-key`, non-localhost access additionally requires `management.allow-remote: true`.
- Setting `MANAGEMENT_PASSWORD` always enables remote access, regardless of `management.allow-remote`; either key is then accepted from any client that can reach the port.
- See [management-api-v8.md](management-api-v8.md) for the endpoints.

## Using the Core Auth Manager

The service uses a core `auth.Manager` (`sdk/cliproxy/auth`) for credential selection, execution, and auto-refresh. You can supply your own manager, for example to register extra executors or customize transports:

```go
import (
    sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
    coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

tokenStore := sdkAuth.GetTokenStore()
if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
    dirSetter.SetBaseDir(cfg.AuthDir)
}
core := coreauth.NewManager(tokenStore, nil, nil)

svc, err := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("config.yaml").
    WithCoreAuthManager(core).
    Build()
if err != nil { /* handle */ }

// Optional per-auth transport. Build() always installs the default provider
// (which honors each credential's proxy_url), so set yours after Build().
core.SetRoundTripperProvider(myRTProvider{})
```

A per-auth transport provider:

```go
type myRTProvider struct{}

func (myRTProvider) RoundTripperFor(a *coreauth.Auth) http.RoundTripper {
    if a == nil || a.ProxyURL == "" { return nil }
    u, _ := url.Parse(a.ProxyURL)
    return &http.Transport{Proxy: http.ProxyURL(u)}
}
```

Programmatic execution is available on the manager:

```go
// Non-streaming
resp, err := core.Execute(ctx, []string{"claude"}, req, opts)

// Streaming
result, err := core.ExecuteStream(ctx, []string{"claude"}, req, opts)
for chunk := range result.Chunks { /* ... */ }
```

Built-in provider executors are wired automatically when you run the `Service`. To use a `Manager` without the HTTP server, register executors that implement `auth.ProviderExecutor` yourself.

## Custom Client Sources

Replace the default loaders if credentials live outside the auth directory:

```go
type memoryTokenProvider struct{}

func (p *memoryTokenProvider) Load(ctx context.Context, cfg *config.Config) (*cliproxy.TokenClientResult, error) {
    // Populate from memory or a remote source and return counts
    return &cliproxy.TokenClientResult{}, nil
}

svc, _ := cliproxy.NewBuilder().
  WithConfig(cfg).
  WithConfigPath("config.yaml").
  WithTokenClientProvider(&memoryTokenProvider{}).
  WithAPIKeyClientProvider(cliproxy.NewAPIKeyClientProvider()).
  Build()
```

## Hooks

Observe the lifecycle without patching internals:

```go
hooks := cliproxy.Hooks{
  OnBeforeStart: func(cfg *config.Config) { log.Infof("starting on :%d", cfg.Port) },
  OnAfterStart:  func(s *cliproxy.Service) { log.Info("ready") },
}
svc, _ := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath("config.yaml").WithHooks(hooks).Build()
```

## Shutdown

`Run` calls `Shutdown` when it returns, so cancelling the parent context is enough. To stop manually:

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = svc.Shutdown(ctx)
```

## Notes

- Hot reload: changes to `config.yaml` and the auth directory are picked up automatically.
- Request logging and debug logs can be toggled at runtime through `/v8/management/config` (`observability.logs.request-log`, `observability.logs.debug`).
