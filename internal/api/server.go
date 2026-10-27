// Package api provides the HTTP API server implementation for the CLI Proxy API.
// It includes the main server struct, routing setup, middleware for CORS and authentication,
// and integration with various AI API handlers (OpenAI, Claude).
// The server supports hot-reloading of clients and configuration.
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	codexlive "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/live"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/webui"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"gopkg.in/yaml.v3"
)

// Server represents the main API server.
// It encapsulates the Gin engine, HTTP server, handlers, and configuration.
type Server struct {
	// engine is the Gin web framework engine instance.
	engine *gin.Engine

	// server is the underlying HTTP server.
	server *http.Server

	// handlers contains the API handlers for processing requests.
	handlers         *handlers.BaseAPIHandler
	codexLiveHandler *codexlive.Handler

	// cfg holds the current server configuration.
	cfgMu sync.RWMutex
	cfg   *config.Config

	// oldConfigYaml stores a YAML snapshot of the previous configuration for change detection.
	// This prevents issues when the config object is modified in place by Management API.
	oldConfigYaml []byte

	// accessManager handles request authentication providers.
	accessManager *sdkaccess.Manager

	// requestLogger is the request logger instance for dynamic configuration updates.
	requestLogger logging.RequestLogger
	loggerToggle  func(bool)

	// configFilePath is the absolute path to the YAML config file for persistence.
	configFilePath string

	// currentPath is the absolute path to the current working directory.
	currentPath string

	// management handler
	mgmt *managementHandlers.Handler

	// managementRoutesRegistered tracks whether the management routes have been attached to the engine.
	managementRoutesRegistered atomic.Bool
	// managementRoutesEnabled controls whether management endpoints serve real handlers.
	managementRoutesEnabled atomic.Bool

	// envManagementSecret indicates whether MANAGEMENT_PASSWORD is configured.
	envManagementSecret bool

	exampleAPIKeySafeModeEnabled bool
	exampleAPIKeySafeModeActive  atomic.Bool

	// webui serves the embedded management web UI under /ui/.
	webui *webui.Handler

	// tailnet resolves tailnet identities and node status; nil when tailscale is off.
	tailnet TailnetNode

	// extraServers tracks http.Servers for additional listeners (see ServeListener).
	extraMu      sync.Mutex
	extraServers map[*http.Server]struct{}
	stopped      bool
}

// NewServer creates and initializes a new API server instance.
// It sets up the Gin engine, middleware, routes, and handlers.
//
// Parameters:
//   - cfg: The server configuration
//   - authManager: core runtime auth manager
//   - accessManager: request authentication manager
//
// Returns:
//   - *Server: A new server instance
func NewServer(cfg *config.Config, authManager *auth.Manager, accessManager *sdkaccess.Manager, configFilePath string, opts ...ServerOption) *Server {
	if cfg != nil {
		cfg = cfg.CloneForRuntime()
	}
	optionState := &serverOptionConfig{
		requestLoggerFactory: defaultRequestLoggerFactory,
	}
	for i := range opts {
		opts[i](optionState)
	}
	// Set gin mode
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create gin engine
	engine := gin.New()
	if errSetTrustedProxies := engine.SetTrustedProxies(cfg.TrustedProxies); errSetTrustedProxies != nil {
		log.WithError(errSetTrustedProxies).Error("invalid trusted-proxies configuration; forwarded client IP headers will be ignored")
		if errDisableTrustedProxies := engine.SetTrustedProxies(nil); errDisableTrustedProxies != nil {
			log.WithError(errDisableTrustedProxies).Error("failed to disable trusted proxy handling")
		}
	}
	if optionState.engineConfigurator != nil {
		optionState.engineConfigurator(engine)
	}

	wd, err := os.Getwd()
	if err != nil {
		wd = configFilePath
	}

	envAdminPassword, envAdminPasswordSet := os.LookupEnv("MANAGEMENT_PASSWORD")
	envAdminPassword = strings.TrimSpace(envAdminPassword)
	envManagementSecret := envAdminPasswordSet && envAdminPassword != ""

	// Create server instance
	s := &Server{
		engine:                       engine,
		handlers:                     handlers.NewBaseAPIHandlers(effectiveSDKConfig(cfg), authManager),
		cfg:                          cfg,
		accessManager:                accessManager,
		configFilePath:               configFilePath,
		currentPath:                  wd,
		envManagementSecret:          envManagementSecret,
		exampleAPIKeySafeModeEnabled: optionState.exampleAPIKeySafeMode,
		tailnet:                      optionState.tailnet,
	}

	// Add middleware
	engine.Use(logging.GinLogrusLogger())
	engine.Use(logging.GinLogrusRecovery())
	engine.Use(logging.CPATraceIDMiddleware())
	engine.Use(usageEndpointMiddleware())
	engine.Use(s.maxRequestBodyBytesMiddleware())
	for _, mw := range optionState.extraMiddleware {
		engine.Use(mw)
	}

	// Add request logging middleware (positioned after recovery, before auth)
	// Resolve logs directory relative to the configuration file directory.
	var requestLogger logging.RequestLogger
	var toggle func(bool)
	if !cfg.CommercialMode {
		if optionState.requestLoggerFactory != nil {
			requestLogger = optionState.requestLoggerFactory(cfg, configFilePath)
		}
		if requestLogger != nil {
			engine.Use(middleware.RequestLoggingMiddleware(requestLogger))
			if setter, ok := requestLogger.(interface{ SetEnabled(bool) }); ok {
				toggle = setter.SetEnabled
			}
		}
	}
	s.requestLogger = requestLogger
	s.loggerToggle = toggle

	engine.Use(corsMiddleware())
	s.exampleAPIKeySafeModeActive.Store(s.exampleAPIKeySafeModeRequired(cfg))
	// Save initial YAML snapshot
	s.oldConfigYaml, _ = yaml.Marshal(cfg)
	s.applyAccessConfig(nil, cfg)
	if authManager != nil {
		authManager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second, cfg.MaxRetryCredentials)
	}
	auth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	auth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
	applyClaudeToolSearchConfig(cfg)
	// Initialize management handler
	s.mgmt = managementHandlers.NewHandler(cfg, configFilePath, authManager)
	s.mgmt.SetConfigReloadHook(optionState.configReloadHook)
	s.mgmt.SetSlugResolver(optionState.slugResolver)
	s.mgmt.SetUsageRuntime(optionState.usageRuntime)
	s.mgmt.SetPricingService(optionState.pricing)
	logDir := logging.ResolveLogDirectory(cfg)
	s.mgmt.SetLogDirectory(logDir)
	if optionState.postAuthHook != nil {
		s.mgmt.SetPostAuthHook(optionState.postAuthHook)
	}
	if optionState.postAuthPersistHook != nil {
		s.mgmt.SetPostAuthPersistHook(optionState.postAuthPersistHook)
	}

	engine.Use(s.exampleAPIKeySafeModeMiddleware())

	// Setup routes
	s.setupRoutes()

	// Apply additional router configurators from options
	if optionState.routerConfigurator != nil {
		optionState.routerConfigurator(engine, s.handlers, cfg)
	}

	// Register management routes when configuration or environment secrets are available,
	// or when tailnet identities may manage the proxy. Without a secret the routes
	// stay unavailable to everyone but identity-authorized tailnet callers.
	hasManagementSecret := cfg.RemoteManagement.SecretKey != "" || envManagementSecret
	s.managementRoutesEnabled.Store(hasManagementSecret)
	if hasManagementSecret || s.tailnetManagementConfigured(cfg) {
		s.registerManagementRoutes()
	}

	// Create HTTP server
	s.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:           wrapUnconsumedRequestBodyHandler(engine),
		ReadHeaderTimeout: inboundReadHeaderTimeout,
	}

	return s
}

func (s *Server) getConfig() *config.Config {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// Handler returns the HTTP handler used by the server.
func (s *Server) Handler() http.Handler {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Handler
}

// Start begins listening for and serving HTTP or HTTPS requests.
// It's a blocking call and will only return on an unrecoverable error.
//
// Returns:
//   - error: An error if the server fails to start
func (s *Server) Start() error {
	if s == nil || s.server == nil {
		return fmt.Errorf("failed to start HTTP server: server not initialized")
	}

	addr := s.server.Addr
	listener, errListen := net.Listen("tcp", addr)
	if errListen != nil {
		return fmt.Errorf("failed to start HTTP server: %v", errListen)
	}

	cfg := s.getConfig()
	useTLS := cfg != nil && cfg.TLS.Enable
	if useTLS {
		certPath := strings.TrimSpace(cfg.TLS.Cert)
		keyPath := strings.TrimSpace(cfg.TLS.Key)
		if certPath == "" || keyPath == "" {
			if errClose := listener.Close(); errClose != nil {
				log.Errorf("failed to close listener after TLS validation failure: %v", errClose)
			}
			return fmt.Errorf("failed to start HTTPS server: tls.cert or tls.key is empty")
		}
		certPair, errLoad := tls.LoadX509KeyPair(certPath, keyPath)
		if errLoad != nil {
			if errClose := listener.Close(); errClose != nil {
				log.Errorf("failed to close listener after TLS key pair load failure: %v", errClose)
			}
			return fmt.Errorf("failed to start HTTPS server: %v", errLoad)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{certPair},
			NextProtos:   []string{"h2", "http/1.1"},
		}
		s.server.TLSConfig = tlsConfig
		if errHTTP2 := http2.ConfigureServer(s.server, &http2.Server{}); errHTTP2 != nil {
			log.Warnf("failed to configure HTTP/2: %v", errHTTP2)
		}
		listener = tls.NewListener(listener, tlsConfig)
		log.Debugf("Starting API server on %s with TLS", addr)
	} else {
		log.Debugf("Starting API server on %s", addr)
	}

	errServe := s.server.Serve(listener)
	if errServe == nil || errors.Is(errServe, http.ErrServerClosed) || errors.Is(errServe, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("failed to start HTTP server: %v", errServe)
}

// Stop closes listeners and immediately shuts down the API server without waiting for active connections.
//
// Parameters:
//   - ctx: Context passed for compatibility.
//
// Returns:
//   - error: An error if the server fails to stop
func (s *Server) Stop(ctx context.Context) error {
	log.Debug("Stopping API server...")

	// Close the HTTP server immediately without graceful draining.
	var errCloseServer error
	if s.server != nil {
		errCloseServer = s.server.Close()
		if errors.Is(errCloseServer, http.ErrServerClosed) || errors.Is(errCloseServer, net.ErrClosed) {
			errCloseServer = nil
		}
	}
	errCloseExtra := s.closeExtraListeners()
	if s.codexLiveHandler != nil {
		s.codexLiveHandler.Close()
	}
	if errCloseServer == nil {
		errCloseServer = errCloseExtra
	}
	if errCloseServer != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", errCloseServer)
	}

	log.Debug("API server stopped")
	return nil
}
