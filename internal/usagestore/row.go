package usagestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/redact"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// maxErrorLength caps the stored error summary (in characters).
const maxErrorLength = 300

// Row is one persisted upstream attempt. It never carries a raw client or
// upstream credential.
type Row struct {
	TS                  int64
	RequestID           string
	TraceID             string
	Provider            string
	AuthID              string
	AuthLabel           string
	AuthType            string
	RequestedModel      string
	UpstreamModel       string
	ResponseModel       string
	BaseURL             string
	ClientKeyID         string
	ClientKeyMasked     string
	Endpoint            string
	SessionID           string
	Stream              bool
	Success             bool
	Status              int
	Error               string
	LatencyMs           int64
	TTFTMs              int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	ReasoningEffort     string
	ServiceTier         string
}

// AuthInfo describes the upstream credential of a record at conversion time.
type AuthInfo struct {
	// Label is the account label (email, or configured name with a masked key).
	Label string
	// Secrets are the credential's raw values (API key, OAuth tokens). They are
	// only used to redact persisted text and are never stored.
	Secrets []string
}

// AuthInfoFor builds the AuthInfo of an auth record.
func AuthInfoFor(auth *coreauth.Auth) AuthInfo {
	if auth == nil {
		return AuthInfo{}
	}
	return AuthInfo{Label: AuthLabel(auth), Secrets: coreauth.CredentialSecrets(auth)}
}

// RowFromRecord converts a usage record. auth carries the resolved account
// label and the upstream credential values to redact; now is used when the
// record has no start time. Every persisted text field is scrubbed of the
// client key and the upstream credential, and session IDs are stored hashed.
func RowFromRecord(record usage.Record, auth AuthInfo, now time.Time) Row {
	ts := record.RequestedAt
	if ts.IsZero() {
		ts = now
	}
	secrets := make([]string, 0, len(auth.Secrets)+1)
	secrets = append(secrets, auth.Secrets...)
	secrets = append(secrets, record.APIKey)
	clean := func(value string) string {
		return redact.Secrets(strings.TrimSpace(value), secrets...)
	}
	row := Row{
		TS:              ts.UnixMilli(),
		RequestID:       clean(record.RequestID),
		TraceID:         clean(record.TraceID),
		Provider:        clean(record.Provider),
		AuthID:          clean(record.AuthID),
		AuthLabel:       clean(auth.Label),
		AuthType:        clean(record.AuthType),
		RequestedModel:  clean(record.Alias),
		UpstreamModel:   clean(record.Model),
		ResponseModel:   clean(record.ResponseModel),
		BaseURL:         clean(sanitizeBaseURL(record.BaseURL)),
		Endpoint:        clean(record.Endpoint),
		SessionID:       HashSessionID(record.SessionID),
		Stream:          record.Stream,
		Success:         !record.Failed,
		LatencyMs:       record.Latency.Milliseconds(),
		TTFTMs:          record.TTFT.Milliseconds(),
		ReasoningEffort: clean(record.ReasoningEffort),
		ServiceTier:     clean(record.ServiceTier),
	}
	if row.RequestedModel == "" {
		row.RequestedModel = row.UpstreamModel
	}
	if tier := clean(record.ResponseServiceTier); tier != "" {
		row.ServiceTier = tier
	}
	row.ClientKeyID, row.ClientKeyMasked = ClientKeyIdentity(record.APIKey)
	if record.Failed {
		row.Status = record.Fail.StatusCode
		row.Error = SummarizeError(record.Fail.Body, secrets...)
	} else {
		row.Status = 200
	}
	applyTokens(&row, record.Detail)
	return row
}

// HashSessionID returns the first 16 hex characters of the SHA-256 of a
// session ID: enough to correlate requests without storing the raw value.
func HashSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])[:16]
}

// applyTokens prefers the canonical non-overlapping breakdown, where input
// includes cache reads/writes and output includes reasoning.
func applyTokens(row *Row, d usage.Detail) {
	b := d.TokenBreakdown
	if b.Valid() && b.Quality != usage.TokenAccountingQualityInconsistent {
		row.InputTokens = b.Input.TotalTokens
		row.OutputTokens = b.Output.TotalTokens
		row.ReasoningTokens = b.Output.ReasoningTokens
		row.CacheReadTokens = b.Input.CacheReadTokens
		row.CacheCreationTokens = b.Input.CacheWriteTokens
		row.TotalTokens = b.TotalTokens
		return
	}
	row.InputTokens = d.InputTokens
	row.OutputTokens = d.OutputTokens
	row.ReasoningTokens = d.ReasoningTokens
	row.CacheReadTokens = d.CacheReadTokens
	if row.CacheReadTokens == 0 {
		row.CacheReadTokens = d.CachedTokens
	}
	row.CacheCreationTokens = d.CacheCreationTokens
	row.TotalTokens = d.TotalTokens
	if b.Valid() && b.TotalTokens > row.TotalTokens {
		row.TotalTokens = b.TotalTokens
	}
	if row.TotalTokens == 0 {
		row.TotalTokens = row.InputTokens + row.OutputTokens
	}
}

// ClientKeyIdentity returns a stable fingerprint (first 8 hex chars of the
// SHA-256) and a masked display form of a client API key. The raw key is never
// returned.
func ClientKeyIdentity(key string) (id, masked string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8], MaskSecret(key)
}

// minVisibleSecretLength is the shortest secret that shows any characters.
const minVisibleSecretLength = 8

// MaskSecret shows at most a short prefix and the last four characters.
// Secrets shorter than minVisibleSecretLength are fully masked.
func MaskSecret(secret string) string {
	runes := []rune(strings.TrimSpace(secret))
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) < minVisibleSecretLength:
		return "****"
	case len(runes) < 12:
		return "…" + string(runes[len(runes)-2:])
	default:
		return string(runes[:3]) + "…" + string(runes[len(runes)-4:])
	}
}

// AuthLabel returns a human-readable account label for a credential: the
// OAuth account email, else the configured label, plus a masked API key.
func AuthLabel(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	kind, value := auth.AccountInfo()
	label := strings.TrimSpace(auth.Label)
	switch kind {
	case "oauth":
		if value != "" {
			return value
		}
	case "api_key":
		if masked := MaskSecret(value); masked != "" {
			if label == "" {
				return masked
			}
			return label + " (" + masked + ")"
		}
	}
	return label
}

var whitespacePattern = regexp.MustCompile(`\s+`)

// SummarizeError extracts a short message from an upstream error body, removes
// the given known secrets and anything credential-shaped, and truncates it to
// maxErrorLength characters.
func SummarizeError(body string, secrets ...string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	msg := body
	if gjson.Valid(body) {
		for _, path := range []string{"error.message", "message", "error.msg", "detail", "error"} {
			if v := gjson.Get(body, path); v.Exists() && v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
				msg = v.String()
				break
			}
		}
	}
	msg = redact.String(msg, secrets...)
	msg = strings.TrimSpace(whitespacePattern.ReplaceAllString(msg, " "))
	if utf8.RuneCountInString(msg) > maxErrorLength {
		runes := []rune(msg)
		msg = string(runes[:maxErrorLength-1]) + "…"
	}
	return msg
}

// invalidURL replaces a base URL that cannot be sanitized.
const invalidURL = "invalid-url"

// sanitizeBaseURL drops credentials, query strings and fragments from an
// upstream URL. It never returns unsanitized input: unparsable or host-less
// values become invalidURL.
func sanitizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, errParse := url.Parse(raw)
	if errParse != nil || u.Host == "" || u.Opaque != "" {
		return invalidURL
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return redact.Tokens(u.String())
}

const insertSQL = `INSERT INTO requests (
	ts, request_id, trace_id, provider, auth_id, auth_label, auth_type,
	requested_model, upstream_model, response_model, base_url,
	client_key_id, client_key_masked, endpoint, session_id,
	stream, success, status, error, latency_ms, ttft_ms,
	input_tokens, output_tokens, reasoning_tokens, cache_read_tokens,
	cache_creation_tokens, total_tokens, reasoning_effort, service_tier
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func (s *Store) insert(rows []Row) error {
	ctx := context.Background()
	tx, errBegin := s.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return fmt.Errorf("begin: %w", errBegin)
	}
	stmt, errPrepare := tx.PrepareContext(ctx, insertSQL)
	if errPrepare != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare: %w", errPrepare)
	}
	defer func() { _ = stmt.Close() }()
	for i := range rows {
		r := &rows[i]
		if _, errExec := stmt.ExecContext(ctx,
			r.TS, r.RequestID, r.TraceID, r.Provider, r.AuthID, r.AuthLabel, r.AuthType,
			r.RequestedModel, r.UpstreamModel, r.ResponseModel, r.BaseURL,
			r.ClientKeyID, r.ClientKeyMasked, r.Endpoint, r.SessionID,
			boolInt(r.Stream), boolInt(r.Success), r.Status, r.Error, r.LatencyMs, r.TTFTMs,
			r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CacheReadTokens,
			r.CacheCreationTokens, r.TotalTokens, r.ReasoningEffort, r.ServiceTier,
		); errExec != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert: %w", errExec)
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return fmt.Errorf("commit: %w", errCommit)
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
