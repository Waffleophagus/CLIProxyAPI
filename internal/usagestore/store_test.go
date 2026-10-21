package usagestore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// fakeClock is a controllable clock for retention tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// openTestStore opens a store whose flush ticker only fires when the test sends on tick.
func openTestStore(t *testing.T, opts Options) (*Store, chan time.Time) {
	t.Helper()
	tick := make(chan time.Time)
	if opts.Path == "" {
		opts.Path = filepath.Join(t.TempDir(), "usage.db")
	}
	opts.Tick = tick
	store, errOpen := Open(opts)
	if errOpen != nil {
		t.Fatalf("Open: %v", errOpen)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, tick
}

func countRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if errScan := s.DB().QueryRow("SELECT COUNT(*) FROM requests").Scan(&n); errScan != nil {
		t.Fatalf("count rows: %v", errScan)
	}
	return n
}

func flush(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if errFlush := s.Flush(ctx); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}
}

func TestOpenMigratesEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "usage.db")
	store, _ := openTestStore(t, Options{Path: path})
	var version int
	if errScan := store.DB().QueryRow("PRAGMA user_version").Scan(&version); errScan != nil {
		t.Fatal(errScan)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	var journal string
	if errScan := store.DB().QueryRow("PRAGMA journal_mode").Scan(&journal); errScan != nil || journal != "wal" {
		t.Fatalf("journal_mode = %q (%v), want wal", journal, errScan)
	}
	var indexes int
	if errScan := store.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'requests' AND name LIKE 'idx_%'").Scan(&indexes); errScan != nil || indexes != 6 {
		t.Fatalf("indexes = %d (%v), want 6", indexes, errScan)
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	// Reopening an up-to-date database is a no-op migration.
	reopened, errOpen := Open(Options{Path: path, Tick: make(chan time.Time)})
	if errOpen != nil {
		t.Fatalf("reopen: %v", errOpen)
	}
	if errClose := reopened.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	store, _ := openTestStore(t, Options{Path: path})
	if _, errExec := store.DB().Exec("PRAGMA user_version = 99"); errExec != nil {
		t.Fatal(errExec)
	}
	_ = store.Close()
	if _, errOpen := Open(Options{Path: path, Tick: make(chan time.Time)}); errOpen == nil || !strings.Contains(errOpen.Error(), "newer") {
		t.Fatalf("Open newer schema error = %v", errOpen)
	}
}

func TestWriterBatchesAndFlushesOnClose(t *testing.T) {
	store, _ := openTestStore(t, Options{BatchSize: 2})
	for i := 0; i < 5; i++ {
		store.Enqueue(Row{TS: int64(i + 1), RequestedModel: "m"})
	}
	// Two full batches are written without any tick; the fifth row waits.
	deadline := time.Now().Add(10 * time.Second)
	for store.Stats().Written < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("written = %d, want 4", store.Stats().Written)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := countRows(t, store); n != 4 {
		t.Fatalf("rows before close = %d, want 4", n)
	}
	path := store.Path()
	if errClose := store.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	reopened, _ := openTestStore(t, Options{Path: path})
	if n := countRows(t, reopened); n != 5 {
		t.Fatalf("rows after close = %d, want 5", n)
	}
}

func TestTickFlushesPartialBatch(t *testing.T) {
	store, tick := openTestStore(t, Options{BatchSize: 100})
	store.Enqueue(Row{TS: 1})
	// The writer may see the tick before the row; keep ticking until it lands.
	deadline := time.Now().Add(10 * time.Second)
	for store.Stats().Written < 1 {
		if time.Now().After(deadline) {
			t.Fatal("tick never flushed the pending row")
		}
		tick <- time.Now()
	}
	if n := countRows(t, store); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestEnqueueDropsWhenFullWithoutBlocking(t *testing.T) {
	// No writer drains this store, so the one-slot queue stays full.
	store := &Store{in: make(chan Row, 1), now: time.Now}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			store.Enqueue(Row{TS: int64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}
	if got := store.Stats().Dropped; got != 999 {
		t.Fatalf("dropped = %d, want 999", got)
	}
}

func TestEnqueueAfterCloseIsDropped(t *testing.T) {
	store, _ := openTestStore(t, Options{})
	if errClose := store.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	store.Enqueue(Row{TS: 1}) // must not panic or block
	if errFlush := store.Flush(context.Background()); errFlush != nil {
		t.Fatalf("Flush after close: %v", errFlush)
	}
}

func TestRetentionPrunesAtOpenAndDaily(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "usage.db")
	day := 24 * time.Hour
	seed, _ := openTestStore(t, Options{Path: path, RetentionDays: -1})
	for _, age := range []time.Duration{10 * day, 2 * day, 12 * time.Hour} {
		seed.Enqueue(Row{TS: clock.Now().Add(-age).UnixMilli()})
	}
	flush(t, seed)
	if errClose := seed.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	store, tick := openTestStore(t, Options{Path: path, RetentionDays: 3, Now: clock.Now})
	if n := countRows(t, store); n != 2 {
		t.Fatalf("rows after startup prune = %d, want 2", n)
	}

	// Within a day of the last prune nothing is removed, even if rows expired.
	clock.Advance(23 * time.Hour)
	tick <- clock.Now()
	flush(t, store)
	if n := countRows(t, store); n != 2 {
		t.Fatalf("rows after early tick = %d, want 2", n)
	}

	// A day after the last prune, the 2-day-old row (now ~3d23h old) expires.
	clock.Advance(2 * time.Hour)
	tick <- clock.Now()
	flush(t, store)
	if n := countRows(t, store); n != 1 {
		t.Fatalf("rows after daily prune = %d, want 1", n)
	}

	// Shrinking retention prunes on the next tick without waiting a day.
	store.SetRetentionDays(1)
	clock.Advance(time.Hour)
	tick <- clock.Now()
	flush(t, store)
	if n := countRows(t, store); n != 0 {
		t.Fatalf("rows after retention change = %d, want 0", n)
	}
}

func TestRowFromRecordMapsFields(t *testing.T) {
	requestedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	record := usage.Record{
		RequestID:       "req-1",
		Provider:        "openai-compatibility",
		Model:           "GLM-5.3-Flash",
		Alias:           "DGX-SPARK",
		BaseURL:         "https://user:pass@example.com/v1?key=secret",
		APIKey:          "sk-client-0123456789abcdef",
		AuthID:          "auth-1",
		AuthType:        "api_key",
		Endpoint:        "/v1/chat/completions",
		Stream:          true,
		RequestedAt:     requestedAt,
		Latency:         1500 * time.Millisecond,
		TTFT:            200 * time.Millisecond,
		ReasoningEffort: "high",
		ServiceTier:     "auto",
		Detail: usage.Detail{
			TokenBreakdown: usage.NewSubsetTokenBreakdown(100, 30, 5, 50, 20, 150),
		},
	}
	row := RowFromRecord(record, AuthInfo{Label: "label"}, time.Now())
	if row.TS != requestedAt.UnixMilli() || row.RequestedModel != "DGX-SPARK" || row.UpstreamModel != "GLM-5.3-Flash" {
		t.Fatalf("row identity = %+v", row)
	}
	if row.BaseURL != "https://example.com/v1" {
		t.Fatalf("base_url = %q", row.BaseURL)
	}
	if row.InputTokens != 100 || row.OutputTokens != 50 || row.ReasoningTokens != 20 || row.CacheReadTokens != 30 || row.CacheCreationTokens != 5 || row.TotalTokens != 150 {
		t.Fatalf("tokens = %+v", row)
	}
	if !row.Success || row.Status != 200 || row.LatencyMs != 1500 || row.TTFTMs != 200 || !row.Stream {
		t.Fatalf("outcome = %+v", row)
	}
	if len(row.ClientKeyID) != 8 || row.ClientKeyMasked != "sk-…cdef" {
		t.Fatalf("client key = %q %q", row.ClientKeyID, row.ClientKeyMasked)
	}

	// Raw detail fallback when no canonical breakdown is attached.
	record.Detail = usage.Detail{InputTokens: 7, OutputTokens: 3, CachedTokens: 2}
	record.Alias = ""
	row = RowFromRecord(record, AuthInfo{}, time.Now())
	if row.InputTokens != 7 || row.OutputTokens != 3 || row.CacheReadTokens != 2 || row.TotalTokens != 10 || row.RequestedModel != "GLM-5.3-Flash" {
		t.Fatalf("fallback row = %+v", row)
	}
}

func TestSummarizeErrorScrubsAndTruncates(t *testing.T) {
	body := `{"error":{"message":"Invalid key sk-proj-ABCDEFGHIJKLMNOP for Bearer eyJhbGciOi.payload.sig, api_key=hunter2hunter2 token: abc123xyz ` + strings.Repeat("x", 400) + `"}}`
	got := SummarizeError(body, "hunter2hunter2")
	for _, secret := range []string{"sk-proj-ABCDEFGHIJKLMNOP", "eyJhbGciOi", "hunter2hunter2", "abc123xyz"} {
		if strings.Contains(got, secret) {
			t.Fatalf("summary leaks %q: %s", secret, got)
		}
	}
	if !strings.HasPrefix(got, "Invalid key") {
		t.Fatalf("summary did not extract error.message: %s", got)
	}
	if n := len([]rune(got)); n > maxErrorLength {
		t.Fatalf("summary length = %d, want <= %d", n, maxErrorLength)
	}
	if got := SummarizeError("upstream timeout after 30s"); got != "upstream timeout after 30s" {
		t.Fatalf("plain summary = %q", got)
	}
}

func TestRawClientKeyNeverPersisted(t *testing.T) {
	const clientKey = "sk-RAWCLIENTKEY-do-not-store-0123456789"
	const upstreamKey = "sk-UPSTREAMKEY-never-persist-9876543210"
	path := filepath.Join(t.TempDir(), "usage.db")
	runtime := NewRuntime(func(string) AuthInfo { return AuthInfo{Label: "acct@example.com"} })
	runtime.newStore = func(opts Options) (*Store, error) {
		opts.Tick = make(chan time.Time)
		return Open(opts)
	}
	runtime.Apply(Settings{Enabled: true, Path: path, RetentionDays: 90}, "")
	store, errStore := runtime.Store()
	if errStore != nil {
		t.Fatal(errStore)
	}
	for _, failed := range []bool{false, true} {
		runtime.HandleUsage(context.Background(), usage.Record{
			Provider: "openai-compatibility",
			Model:    "m",
			APIKey:   clientKey,
			Source:   upstreamKey,
			AuthID:   "auth-1",
			Failed:   failed,
			Fail: usage.Failure{
				StatusCode: 401,
				Body:       `{"error":{"message":"bad key ` + clientKey + ` / Bearer ` + upstreamKey + `"}}`,
			},
		})
	}
	flush(t, store)
	if n := countRows(t, store); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	files, _ := filepath.Glob(path + "*")
	if len(files) == 0 {
		t.Fatal("database files missing")
	}
	for _, file := range files {
		data, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		for _, secret := range []string{clientKey, upstreamKey, "RAWCLIENTKEY", "UPSTREAMKEY"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("%s contains secret %q", filepath.Base(file), secret)
			}
		}
	}
}

func TestRuntimeApplyTogglesStore(t *testing.T) {
	dir := t.TempDir()
	runtime := NewRuntime(nil)
	if _, errStore := runtime.Store(); errStore != ErrDisabled {
		t.Fatalf("unapplied Store error = %v, want ErrDisabled", errStore)
	}
	first := Settings{Enabled: true, Path: filepath.Join(dir, "a.db"), RetentionDays: 90}
	runtime.Apply(first, "")
	storeA, errStore := runtime.Store()
	if errStore != nil {
		t.Fatal(errStore)
	}
	runtime.Apply(Settings{Enabled: true, Path: first.Path, RetentionDays: 7}, "")
	if same, _ := runtime.Store(); same != storeA || same.RetentionDays() != 7 {
		t.Fatalf("retention change reopened the store or did not apply")
	}
	runtime.Apply(Settings{Enabled: true, Path: filepath.Join(dir, "b.db"), RetentionDays: 7}, "")
	storeB, _ := runtime.Store()
	if storeB == nil || storeB == storeA || storeB.Path() != filepath.Join(dir, "b.db") {
		t.Fatalf("path change did not reopen the store")
	}
	runtime.Apply(Settings{Enabled: false, Path: filepath.Join(dir, "b.db"), RetentionDays: 7}, "")
	if _, errStore = runtime.Store(); errStore != ErrDisabled {
		t.Fatalf("disabled Store error = %v", errStore)
	}
	runtime.HandleUsage(context.Background(), usage.Record{Model: "m"}) // no store: ignored
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}

func TestRuntimeReportsOpenError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if errWrite := os.WriteFile(blocker, []byte("x"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	runtime := NewRuntime(nil)
	runtime.Apply(Settings{Enabled: true, Path: filepath.Join(blocker, "usage.db")}, "")
	if _, errStore := runtime.Store(); errStore == nil || errStore == ErrDisabled {
		t.Fatalf("Store error = %v, want open failure", errStore)
	}
}

func TestResolveSettingsPaths(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")
	configPath := filepath.Join(t.TempDir(), "conf", "config.yaml")
	configDir := filepath.Dir(configPath)
	cfg := &config.Config{AuthDir: filepath.Join(configDir, "auths")}

	got := ResolveSettings(cfg, configPath)
	if !got.Enabled || got.RetentionDays != config.DefaultUsageRetentionDays || got.Path != filepath.Join(configDir, "data", DefaultFileName) {
		t.Fatalf("defaults = %+v", got)
	}
	if insideDir(got.Path, cfg.AuthDir) {
		t.Fatalf("default path %s is inside the auth dir", got.Path)
	}

	writable := t.TempDir()
	t.Setenv("WRITABLE_PATH", writable)
	if got = ResolveSettings(cfg, configPath); got.Path != filepath.Join(writable, "data", DefaultFileName) {
		t.Fatalf("WRITABLE_PATH default = %s", got.Path)
	}

	disabled := false
	cfg.UsageTracking = config.UsageTrackingConfig{Enabled: &disabled, Path: "stats/u.db", RetentionDays: -1}
	got = ResolveSettings(cfg, configPath)
	if got.Enabled || got.RetentionDays != 0 || got.Path != filepath.Join(configDir, "stats", "u.db") {
		t.Fatalf("explicit = %+v", got)
	}
	if !insideDir(filepath.Join(cfg.AuthDir, "usage.db"), cfg.AuthDir) {
		t.Fatal("insideDir missed a nested path")
	}
}
