package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	// CacheFileName is the catalog cache file inside the data directory.
	CacheFileName = "models-dev.json"
	// RefreshInterval is how old the catalog may get before it is refetched.
	RefreshInterval = 24 * time.Hour
	// retryInterval spaces out refresh attempts after a failure.
	retryInterval = time.Hour
	// refreshFloor rate-limits manual refreshes.
	refreshFloor   = time.Minute
	maxCatalogSize = 64 << 20
)

// Status describes the loaded catalog.
type Status struct {
	Enabled   bool      `json:"enabled"`
	Source    string    `json:"source"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Models    int       `json:"models"`
	Error     string    `json:"error,omitempty"`
}

type cacheFile struct {
	Source    string          `json:"source"`
	FetchedAt time.Time       `json:"fetched_at"`
	Catalog   json.RawMessage `json:"catalog"`
}

// Service keeps the models.dev catalog fresh and publishes Pricer snapshots
// built from it and the pricing config.
type Service struct {
	mu        sync.Mutex
	cfg       config.UsagePricingConfig
	enabled   bool
	cachePath string
	catalog   *Catalog
	pricer    *Pricer
	status    Status
	lastTry   time.Time

	// loop is the refresh goroutine for loopSource.
	loopSource string
	loopCancel context.CancelFunc
	loopWake   chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time
	// fetch reads a catalog source; replaced in tests.
	fetch func(ctx context.Context, source string) ([]byte, error)
}

// NewService creates a disabled service; call Apply to configure it.
func NewService() *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{ctx: ctx, cancel: cancel, now: time.Now, fetch: readSource}
}

// Apply reconfigures the service. cachePath is the catalog cache file used
// for URL sources. A disabled service returns a nil Pricer.
func (s *Service) Apply(enabled bool, cfg config.UsagePricingConfig, cachePath string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	enabled = enabled && cfg.IsEnabled()
	source := cfg.EffectiveSource()
	sourceChanged := source != s.status.Source || cachePath != s.cachePath
	s.cfg, s.enabled, s.cachePath = cfg, enabled, cachePath
	s.status.Enabled = enabled
	if sourceChanged {
		s.catalog = nil
		s.status = Status{Enabled: enabled, Source: source}
	}
	s.rebuildLocked()
	if !enabled {
		s.stopLoopLocked()
		return
	}
	if s.loopCancel != nil && s.loopSource == source && !sourceChanged {
		return
	}
	s.stopLoopLocked()
	loopCtx, cancel := context.WithCancel(s.ctx)
	s.loopSource, s.loopCancel, s.loopWake = source, cancel, make(chan struct{}, 1)
	go s.run(loopCtx, source, s.loopWake)
}

func (s *Service) stopLoopLocked() {
	if s.loopCancel != nil {
		s.loopCancel()
		s.loopCancel, s.loopSource, s.loopWake = nil, "", nil
	}
}

func (s *Service) rebuildLocked() {
	if !s.enabled {
		s.pricer = nil
		return
	}
	s.pricer = NewPricer(s.catalog, s.cfg)
}

// Close stops background refreshes.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.cancel()
}

// Pricer returns the current snapshot, or nil when pricing is disabled.
func (s *Service) Pricer() *Pricer {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pricer
}

// Catalog returns the loaded catalog (nil until one has loaded).
func (s *Service) Catalog() *Catalog {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog
}

// Status reports the catalog state.
func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// ErrRefreshTooSoon is returned by Refresh within a minute of the last attempt.
var ErrRefreshTooSoon = errors.New("pricing: catalog was refreshed less than a minute ago")

// Refresh asks the refresh loop to refetch the catalog now.
func (s *Service) Refresh() error {
	if s == nil {
		return errors.New("pricing: unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.loopWake == nil {
		return errors.New("pricing: disabled")
	}
	if !s.lastTry.IsZero() && s.now().Sub(s.lastTry) < refreshFloor {
		return ErrRefreshTooSoon
	}
	select {
	case s.loopWake <- struct{}{}:
	default:
	}
	return nil
}

func (s *Service) run(ctx context.Context, source string, wake <-chan struct{}) {
	if fromFile := filepath.IsAbs(source); !fromFile {
		s.loadCache(source)
	}
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	force := false
	for {
		if force || s.due(source) {
			s.refresh(ctx, source)
		}
		force = false
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
			force = true
		}
	}
}

// due reports whether the catalog for source is missing or stale. Failed
// attempts are retried once per retryInterval by the ticker.
func (s *Service) due(source string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Source != source {
		return false
	}
	return s.catalog == nil || s.now().Sub(s.status.FetchedAt) >= RefreshInterval
}

func (s *Service) loadCache(source string) {
	s.mu.Lock()
	path := s.cachePath
	s.mu.Unlock()
	if path == "" {
		return
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if !errors.Is(errRead, os.ErrNotExist) {
			log.Warnf("pricing: read catalog cache: %v", errRead)
		}
		return
	}
	var cached cacheFile
	if errDecode := json.Unmarshal(data, &cached); errDecode != nil || cached.Source != source {
		return
	}
	catalog, errParse := ParseCatalog(cached.Catalog)
	if errParse != nil {
		log.Warnf("pricing: ignoring catalog cache: %v", errParse)
		return
	}
	s.publish(source, catalog, cached.FetchedAt)
}

func (s *Service) refresh(ctx context.Context, source string) {
	s.mu.Lock()
	s.lastTry = s.now()
	s.mu.Unlock()
	data, errFetch := s.fetch(ctx, source)
	var catalog *Catalog
	if errFetch == nil {
		catalog, errFetch = ParseCatalog(data)
	}
	if ctx.Err() != nil {
		return
	}
	if errFetch != nil {
		log.Warnf("pricing: catalog refresh from %s failed; keeping last catalog: %v", source, errFetch)
		s.mu.Lock()
		if s.status.Source == source {
			s.status.Error = errFetch.Error()
		}
		s.mu.Unlock()
		return
	}
	fetchedAt := s.now()
	s.publish(source, catalog, fetchedAt)
	log.Infof("pricing: loaded %d priced models from %s", catalog.Len(), source)
	if filepath.IsAbs(source) {
		return
	}
	s.mu.Lock()
	path := s.cachePath
	s.mu.Unlock()
	if errWrite := writeCache(path, cacheFile{Source: source, FetchedAt: fetchedAt, Catalog: data}); errWrite != nil {
		log.Warnf("pricing: write catalog cache: %v", errWrite)
	}
}

func (s *Service) publish(source string, catalog *Catalog, fetchedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Source != source {
		return
	}
	s.catalog = catalog
	s.status.FetchedAt = fetchedAt
	s.status.Models = catalog.Len()
	s.status.Error = ""
	s.rebuildLocked()
}

func writeCache(path string, cached cacheFile) error {
	if path == "" {
		return nil
	}
	data, errEncode := json.Marshal(cached)
	if errEncode != nil {
		return errEncode
	}
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
		return errMkdir
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o644); errWrite != nil {
		return errWrite
	}
	return os.Rename(tmp, path)
}

// readSource reads an absolute file path or fetches a URL.
func readSource(ctx context.Context, source string) ([]byte, error) {
	var reader io.ReadCloser
	if filepath.IsAbs(source) {
		file, errOpen := os.Open(source)
		if errOpen != nil {
			return nil, errOpen
		}
		reader = file
	} else {
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if errRequest != nil {
			return nil, errRequest
		}
		resp, errDo := http.DefaultClient.Do(req)
		if errDo != nil {
			return nil, errDo
		}
		reader = resp.Body
		if resp.StatusCode != http.StatusOK {
			_ = reader.Close()
			return nil, fmt.Errorf("catalog HTTP status %d", resp.StatusCode)
		}
	}
	defer func() {
		if errClose := reader.Close(); errClose != nil {
			log.Debugf("pricing: close catalog source: %v", errClose)
		}
	}()
	data, errRead := io.ReadAll(io.LimitReader(reader, maxCatalogSize+1))
	if errRead != nil {
		return nil, errRead
	}
	if len(data) > maxCatalogSize {
		return nil, errors.New("catalog exceeds size limit")
	}
	return data, nil
}
