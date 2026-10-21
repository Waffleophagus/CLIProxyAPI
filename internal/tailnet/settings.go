package tailnet

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// AuthKeyEnv is the environment variable read when tailscale.auth-key is empty.
const AuthKeyEnv = "TS_AUTHKEY"

// StateDirName is the default state directory name inside the data directory.
const StateDirName = "tailscale"

// Settings is the resolved node configuration. Changing any of these requires
// a restart; management users are read live from the config instead.
// It holds the auth key, so never log a Settings value.
type Settings struct {
	Enabled   bool
	Hostname  string
	AuthKey   string
	StateDir  string
	Ephemeral bool
	HTTPS     bool
	// HTTPPort is the plain HTTP tailnet port; 0 means none.
	HTTPPort int
}

// ResolveSettings applies defaults: hostname "cliproxy", the TS_AUTHKEY
// fallback, state under <data dir>/tailscale (the data dir is
// $WRITABLE_PATH/data or data/ next to the config file), and plain HTTP on the
// main server port when HTTPS is off and no http-port is set.
func ResolveSettings(cfg *config.Config, configFilePath string) Settings {
	if cfg == nil {
		return Settings{}
	}
	ts := cfg.Tailscale
	authKey := strings.TrimSpace(ts.AuthKey)
	if authKey == "" {
		authKey = strings.TrimSpace(os.Getenv(AuthKeyEnv))
	}
	stateDir := util.ResolveDataPath(ts.StateDir, configFilePath)
	if stateDir == "" {
		stateDir = filepath.Join(util.DataDir(configFilePath), StateDirName)
	}
	httpPort := ts.HTTPPort
	if !ts.HTTPSEnabled() && httpPort == 0 {
		httpPort = cfg.Port
	}
	return Settings{
		Enabled:   ts.Enabled,
		Hostname:  ts.EffectiveHostname(),
		AuthKey:   authKey,
		StateDir:  stateDir,
		Ephemeral: ts.Ephemeral,
		HTTPS:     ts.HTTPSEnabled(),
		HTTPPort:  httpPort,
	}
}
