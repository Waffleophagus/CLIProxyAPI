// Package main provides the entry point for the CLI Proxy API server.
// This server acts as a proxy that provides OpenAI/Claude compatible API interfaces
// for CLI models, allowing CLI models to be used with tools and libraries designed for standard AI APIs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/safemode"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var (
	Version           = "dev"
	Commit            = "none"
	BuildDate         = "unknown"
	DefaultConfigPath = ""
)

// init initializes the shared logger setup.
func init() {
	logging.SetupBaseLogger()
	buildinfo.Version = Version
	buildinfo.Commit = Commit
	buildinfo.BuildDate = BuildDate
}

func shouldEnableExampleAPIKeySafeMode(cfg *config.Config, commandMode bool) bool {
	if cfg == nil || commandMode {
		return false
	}
	return safemode.HasExampleAPIKeys(cfg.APIKeys)
}

// main is the entry point of the application.
// It parses command-line flags, loads configuration, and starts the appropriate
// service based on the provided flags (login commands or server mode).
func main() {
	fmt.Printf("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)

	// Command-line flags to control the application's behavior.
	var codexLogin bool
	var codexDeviceLogin bool
	var claudeLogin bool
	var noBrowser bool
	var oauthCallbackPort int
	var kimiLogin bool
	var kimiAILogin bool
	var xaiLogin bool
	var devinLogin bool
	var metaLogin bool
	var siwcBind string
	var configPath string
	var localModel bool

	// Define command-line flags for different operation modes.
	flag.BoolVar(&codexLogin, "codex-login", false, "Login to Codex using OAuth")
	flag.BoolVar(&codexDeviceLogin, "codex-device-login", false, "Login to Codex using device code flow")
	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.IntVar(&oauthCallbackPort, "oauth-callback-port", 0, "Override OAuth callback port (defaults to provider-specific port)")
	flag.BoolVar(&kimiLogin, "kimi-login", false, "Login to Kimi (.com) using OAuth")
	flag.BoolVar(&kimiAILogin, "kimi-ai-login", false, "Login to Kimi.ai using OAuth")
	flag.BoolVar(&xaiLogin, "xai-login", false, "Login to xAI using OAuth")
	flag.BoolVar(&devinLogin, "devin-login", false, "Login to Devin using OAuth")
	flag.BoolVar(&metaLogin, "meta-login", false, "Login to Meta using OAuth")
	flag.StringVar(&configPath, "config", DefaultConfigPath, "Configure File Path")
	flag.StringVar(&siwcBind, "siwc-bind", "", "Bind Sign in with ChatGPT (SIWC) credentials to a Codex credential file")
	flag.BoolVar(&localModel, "local-model", false, "Use embedded model catalogs unless models.catalog, models.codex-catalog, or models.devin-catalog explicitly overrides the source")

	// Parse the command-line flags.
	flag.Parse()

	wd, err := os.Getwd()
	if err != nil {
		log.Errorf("failed to get working directory: %v", err)
		return
	}

	// Load environment variables from .env if present.
	if errLoad := godotenv.Load(filepath.Join(wd, ".env")); errLoad != nil {
		if !errors.Is(errLoad, os.ErrNotExist) {
			log.WithError(errLoad).Warn("failed to load .env file")
		}
	}

	// Determine and load the configuration file.
	configFilePath := configPath
	if configFilePath == "" {
		configFilePath = filepath.Join(wd, "config.yaml")
	}
	cfg, err := config.LoadConfig(configFilePath)
	if err != nil {
		log.Errorf("failed to load config: %v", err)
		return
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	coreauth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)

	if err = logging.ConfigureLogOutput(cfg); err != nil {
		log.Errorf("failed to configure log output: %v", err)
		return
	}

	log.Infof("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)

	// Set the log level based on the configuration.
	util.SetLogLevel(cfg)

	if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
		log.Errorf("failed to resolve auth directory: %v", errResolveAuthDir)
		return
	} else {
		cfg.AuthDir = resolvedAuthDir
	}

	// Create login options to be used in authentication flows.
	options := &cmd.LoginOptions{
		NoBrowser:    noBrowser,
		CallbackPort: oauthCallbackPort,
	}

	commandMode := siwcBind != "" || codexLogin || codexDeviceLogin || claudeLogin || kimiLogin || kimiAILogin || xaiLogin || devinLogin || metaLogin
	serverOptions := []api.ServerOption(nil)
	if shouldEnableExampleAPIKeySafeMode(cfg, commandMode) {
		matches := safemode.ExampleAPIKeys(cfg.APIKeys)
		log.WithField("api_keys", strings.Join(matches, ",")).Error("unsafe example API key configured; proxy API endpoints disabled until api-keys is updated")
		serverOptions = append(serverOptions, api.WithExampleAPIKeySafeMode())
	}

	// Register the shared token store once so all components use the same persistence backend.
	sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())

	// Register built-in access providers before constructing services.
	configaccess.Register(&cfg.SDKConfig)

	// Handle different command modes based on the provided flags.
	switch {
	case siwcBind != "":
		cmd.DoSIWCBind(cfg, siwcBind, options)
	case codexLogin:
		cmd.DoCodexLogin(cfg, options)
	case codexDeviceLogin:
		cmd.DoCodexDeviceLogin(cfg, options)
	case claudeLogin:
		cmd.DoClaudeLogin(cfg, options)
	case kimiLogin:
		cmd.DoKimiLogin(cfg, options)
	case kimiAILogin:
		cmd.DoKimiAILogin(cfg, options)
	case xaiLogin:
		cmd.DoXAILogin(cfg, options)
	case devinLogin:
		cmd.DoDevinLogin(cfg, options)
	case metaLogin:
		cmd.DoMetaLogin(cfg, options)
	default:
		if localModel {
			log.Info("Local model mode: using embedded catalogs unless an explicit catalog source is configured")
		}
		registry.SetLocalModelCatalogs(localModel)
		cmd.StartService(cfg, configFilePath, serverOptions...)
	}
}
