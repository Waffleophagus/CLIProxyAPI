// Package cmd provides command-line interface functionality for the CLI Proxy API server.
// It includes authentication flows for various AI service providers, service startup,
// and other command-line operations.
package cmd

import (
	"context"
	"errors"
	"os/signal"
	"syscall"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	log "github.com/sirupsen/logrus"
)

// StartService builds and runs the proxy service using the exported SDK.
// It creates a new proxy service instance, sets up signal handling for graceful shutdown,
// and starts the service with the provided configuration.
//
// Parameters:
//   - cfg: The application configuration
//   - configPath: The path to the configuration file
//   - serverOptions: Optional additional server options
//
// Startup and runtime failures are returned after cleanup; cancellation is a normal shutdown.
func StartService(cfg *config.Config, configPath string, serverOptions ...api.ServerOption) error {
	builder := cliproxy.NewBuilder().
		WithConfig(cfg).
		WithConfigPath(configPath)
	if len(serverOptions) > 0 {
		builder = builder.WithServerOptions(serverOptions...)
	}

	ctxSignal, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	service, err := builder.Build()
	if err != nil {
		log.Errorf("failed to build proxy service: %v", err)
		return err
	}

	err = service.Run(ctxSignal)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Errorf("proxy service exited with error: %v", err)
		return err
	}
	return nil
}
