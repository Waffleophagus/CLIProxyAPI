package usagestore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// DefaultFileName is the database file name inside the data directory.
const DefaultFileName = "usage.db"

// ErrDisabled is returned by Runtime.Store when usage tracking is disabled.
var ErrDisabled = errors.New("usage tracking is disabled (observability.usage.enabled: false)")

// Settings is the resolved usage tracking configuration.
type Settings struct {
	Enabled       bool
	Path          string
	RetentionDays int
}

// ResolveSettings derives the effective settings from the config. The default
// database lives in $WRITABLE_PATH/data, else in a data/ directory next to the
// config file; it is deliberately kept out of the auth directory, whose watcher
// would otherwise react to every WAL write.
func ResolveSettings(cfg *config.Config, configFilePath string) Settings {
	if cfg == nil {
		return Settings{}
	}
	u := cfg.UsageTracking
	path := util.ResolveDataPath(u.Path, configFilePath)
	if path == "" {
		path = filepath.Join(util.DataDir(configFilePath), DefaultFileName)
	}
	return Settings{
		Enabled:       u.IsEnabled(),
		Path:          filepath.Clean(path),
		RetentionDays: u.EffectiveRetentionDays(),
	}
}

// Runtime owns the active Store, swaps it on config changes and receives usage
// records from the dispatcher. It implements usage.Plugin.
type Runtime struct {
	// mu guards the fields below. HandleUsage holds the read lock until the row
	// is enqueued, so a store swap in Apply waits for in-flight deliveries and
	// every record lands in the old store (flushed on close) or the new one.
	mu       sync.RWMutex
	store    *Store
	settings Settings
	openErr  error
	applied  bool

	resolveAuth func(authID string) AuthInfo
	now         func() time.Time
	// newStore is replaced in tests.
	newStore func(Options) (*Store, error)
}

// NewRuntime creates a runtime. resolveAuth maps an auth ID to its account
// label and credential values (used for redaction only) and may be nil.
func NewRuntime(resolveAuth func(authID string) AuthInfo) *Runtime {
	return &Runtime{resolveAuth: resolveAuth, now: time.Now, newStore: Open}
}

// Apply opens, closes, or reconfigures the store to match settings. authDir is
// used only to warn about a database placed inside the auth directory.
func (r *Runtime) Apply(settings Settings, authDir string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.applied && r.settings == settings && (r.store != nil || !settings.Enabled) {
		return
	}
	prev := r.settings
	wasApplied := r.applied
	r.applied = true
	r.settings = settings

	if r.store != nil && settings.Enabled && r.store.Path() == settings.Path {
		if prev.RetentionDays != settings.RetentionDays {
			r.store.SetRetentionDays(settings.RetentionDays)
			log.Infof("usage tracking retention set to %d days", settings.RetentionDays)
		}
		return
	}
	if r.store != nil {
		if errClose := r.store.Close(); errClose != nil {
			log.Warnf("usage tracking: failed to close %s: %v", r.store.Path(), errClose)
		}
		log.Infof("usage tracking store closed: %s", r.store.Path())
		r.store = nil
	}
	r.openErr = nil
	if !settings.Enabled {
		if wasApplied {
			log.Info("usage tracking disabled")
		}
		return
	}
	if insideDir(settings.Path, authDir) {
		log.Warnf("usage tracking database %s is inside the auth directory; move it (observability.usage.path) to avoid auth watcher churn", settings.Path)
	}
	store, errOpen := r.newStore(Options{Path: settings.Path, RetentionDays: settings.RetentionDays, Now: r.now})
	if errOpen != nil {
		r.openErr = errOpen
		log.Errorf("usage tracking unavailable: %v", errOpen)
		return
	}
	r.store = store
	log.Infof("usage tracking enabled: %s (retention %d days)", settings.Path, settings.RetentionDays)
}

// Store returns the active store, ErrDisabled, or the error that prevented
// the store from opening.
func (r *Runtime) Store() (*Store, error) {
	if r == nil {
		return nil, ErrDisabled
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.store != nil {
		return r.store, nil
	}
	if r.openErr != nil {
		return nil, r.openErr
	}
	return nil, ErrDisabled
}

// HandleUsage implements usage.Plugin. It runs on the dispatcher goroutine,
// never on the request path, and never blocks on the writer (Enqueue drops
// when the queue is full).
func (r *Runtime) HandleUsage(_ context.Context, record usage.Record) {
	if r == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.store == nil {
		return
	}
	var auth AuthInfo
	if r.resolveAuth != nil && strings.TrimSpace(record.AuthID) != "" {
		auth = r.resolveAuth(record.AuthID)
	}
	r.store.Enqueue(RowFromRecord(record, auth, r.now()))
}

// Close flushes and closes the active store.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	store := r.store
	r.store = nil
	r.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.Close()
}

func insideDir(path, dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	if resolved, errResolve := util.ResolveAuthDir(dir); errResolve == nil && resolved != "" {
		dir = resolved
	}
	absDir, errDir := filepath.Abs(dir)
	absPath, errPath := filepath.Abs(path)
	if errDir != nil || errPath != nil {
		return false
	}
	rel, errRel := filepath.Rel(absDir, absPath)
	return errRel == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
