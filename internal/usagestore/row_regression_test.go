package usagestore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// assertFilesOmit fails when any database file (main, WAL, SHM) contains one
// of the secrets.
func assertFilesOmit(t *testing.T, path string, secrets ...string) {
	t.Helper()
	files, _ := filepath.Glob(path + "*")
	if len(files) == 0 {
		t.Fatal("database files missing")
	}
	for _, file := range files {
		data, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		for _, secret := range secrets {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("%s contains secret %q", filepath.Base(file), secret)
			}
		}
	}
}

func TestUpstreamCredentialsNeverPersisted(t *testing.T) {
	const apiKey = "local-gateway-secret"
	const accessToken = "opaque.oauth.access.value"
	auths := map[string]*coreauth.Auth{
		"compat": {ID: "compat", Provider: "openai-compatibility", Attributes: map[string]string{"api_key": apiKey}},
		"oauth":  {ID: "oauth", Provider: "codex", Metadata: map[string]any{"email": "me@example.com", "access_token": accessToken}},
	}
	path := filepath.Join(t.TempDir(), "usage.db")
	runtime := NewRuntime(func(authID string) AuthInfo { return AuthInfoFor(auths[authID]) })
	runtime.Apply(Settings{Enabled: true, Path: path, RetentionDays: 90}, "")
	store, errStore := runtime.Store()
	if errStore != nil {
		t.Fatal(errStore)
	}
	secrets := map[string]string{"compat": apiKey, "oauth": accessToken}
	for authID, secret := range secrets {
		runtime.HandleUsage(context.Background(), usage.Record{
			Provider: auths[authID].Provider, Model: "m", AuthID: authID,
			Failed: true, Fail: usage.Failure{StatusCode: 401, Body: `{"error":{"message":"Incorrect API key provided: ` + secret + `","type":"invalid_request_error","code":"invalid_api_key"}}`},
		})
		// Echoed without any key-like context: only exact redaction catches it.
		runtime.HandleUsage(context.Background(), usage.Record{
			Provider: auths[authID].Provider, Model: "m", AuthID: authID,
			Failed: true, Fail: usage.Failure{StatusCode: 401, Body: "upstream rejected " + secret + " for this project"},
		})
	}
	flush(t, store)
	if n := countRows(t, store); n != 4 {
		t.Fatalf("rows = %d, want 4", n)
	}
	var errorText string
	if errScan := store.DB().QueryRow("SELECT group_concat(error, ' | ') FROM requests").Scan(&errorText); errScan != nil {
		t.Fatal(errScan)
	}
	if !strings.Contains(errorText, "Incorrect API key provided") {
		t.Fatalf("error summary lost its message: %s", errorText)
	}
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	assertFilesOmit(t, path, apiKey, accessToken)
}

func TestSanitizeBaseURLNeverReturnsRawInput(t *testing.T) {
	cases := map[string]string{
		"":                                       "",
		"https://user:pw@example.com/v1?key=x#f": "https://example.com/v1",
		"https://user:password@example.com/%zz?key=some-secret": invalidURL,
		"user:password@example.com/v1?key=some-secret":          invalidURL,
		"not a url with sk-secretvalue123":                      invalidURL,
		"mailto:someone@example.com?key=some-secret":            invalidURL,
		"https://example.com/v1/sk-proj-ABCDEFGHIJKLMNOP/x":     "https://example.com/v1/[redacted]/x",
	}
	for raw, want := range cases {
		if got := sanitizeBaseURL(raw); got != want {
			t.Fatalf("sanitizeBaseURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestKnownSecretsRedactedFromEveryTextField(t *testing.T) {
	const clientKey = "client-key-value-1234"
	const upstreamKey = "upstream-key-value-5678"
	record := usage.Record{
		RequestID:       "req-" + clientKey,
		TraceID:         "trace-" + upstreamKey,
		Provider:        "openai-compatibility",
		Model:           "m-" + upstreamKey,
		Alias:           "alias-" + clientKey,
		ResponseModel:   "resp-" + clientKey,
		AuthID:          "auth-1",
		AuthType:        "api_key",
		APIKey:          clientKey,
		Endpoint:        "/v1/" + clientKey,
		SessionID:       "session-" + clientKey,
		ReasoningEffort: "high " + upstreamKey,
		ServiceTier:     "auto " + clientKey,
		BaseURL:         "https://example.com/" + upstreamKey,
		Failed:          true,
		Fail:            usage.Failure{StatusCode: 500, Body: "boom " + clientKey + " " + upstreamKey},
	}
	row := RowFromRecord(record, AuthInfo{Label: "acct " + upstreamKey, Secrets: []string{upstreamKey}}, time.Now())
	fields := []string{row.RequestID, row.TraceID, row.Provider, row.AuthID, row.AuthLabel, row.AuthType,
		row.RequestedModel, row.UpstreamModel, row.ResponseModel, row.BaseURL, row.ClientKeyID,
		row.ClientKeyMasked, row.Endpoint, row.SessionID, row.Error, row.ReasoningEffort, row.ServiceTier}
	for _, field := range fields {
		if strings.Contains(field, clientKey) || strings.Contains(field, upstreamKey) {
			t.Fatalf("row field leaks a known secret: %q (row %+v)", field, row)
		}
	}
	if row.SessionID != HashSessionID(record.SessionID) || len(row.SessionID) != 16 {
		t.Fatalf("session_id = %q, want 16-hex hash", row.SessionID)
	}
	if HashSessionID("same") != HashSessionID(" same ") || HashSessionID("") != "" {
		t.Fatal("HashSessionID is not stable")
	}
}

func TestShortClientKeysAreFullyMasked(t *testing.T) {
	cases := map[string]string{
		"a":            "****",
		"ab":           "****",
		"abcdefg":      "****",
		"abcdefgh":     "…gh",
		"sk-abcdef123": "sk-…f123",
	}
	for key, want := range cases {
		if got := MaskSecret(key); got != want {
			t.Fatalf("MaskSecret(%q) = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"a", "ab"} {
		row := RowFromRecord(usage.Record{Model: "m", APIKey: key}, AuthInfo{}, time.Now())
		if row.ClientKeyMasked != "****" || len(row.ClientKeyID) != 8 {
			t.Fatalf("short key %q row = %q %q", key, row.ClientKeyID, row.ClientKeyMasked)
		}
	}
}

// TestRuntimeStoreSwapWaitsForInFlightRecord pauses a delivery after it has
// picked the store (inside the auth resolver) and checks that a concurrent
// Apply cannot swap stores until the record is enqueued, so the record lands
// in the old store, which is flushed on close.
func TestRuntimeStoreSwapWaitsForInFlightRecord(t *testing.T) {
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	entered := make(chan struct{})
	release := make(chan struct{})
	runtime := NewRuntime(func(string) AuthInfo {
		close(entered)
		<-release
		return AuthInfo{Label: "acct"}
	})
	runtime.newStore = func(opts Options) (*Store, error) {
		opts.Tick = make(chan time.Time)
		return Open(opts)
	}
	runtime.Apply(Settings{Enabled: true, Path: pathA, RetentionDays: 90}, "")

	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		runtime.HandleUsage(context.Background(), usage.Record{Model: "m", AuthID: "auth-1"})
	}()
	<-entered
	// The delivery holds a lease on store A: a swap must not be possible now.
	if runtime.mu.TryLock() {
		runtime.mu.Unlock()
		close(release)
		t.Fatal("store swap possible while a record is being delivered")
	}
	applied := make(chan struct{})
	go func() {
		defer close(applied)
		runtime.Apply(Settings{Enabled: true, Path: pathB, RetentionDays: 90}, "")
	}()
	close(release)
	<-delivered
	<-applied

	storeB, errStore := runtime.Store()
	if errStore != nil || storeB.Path() != pathB {
		t.Fatalf("store after swap = %v, %v", storeB, errStore)
	}
	if errClose := runtime.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	reopened, errOpen := Open(Options{Path: pathA, Tick: make(chan time.Time)})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() { _ = reopened.Close() }()
	if n := countRows(t, reopened); n != 1 {
		t.Fatalf("rows in store A = %d, want the in-flight record", n)
	}
}
