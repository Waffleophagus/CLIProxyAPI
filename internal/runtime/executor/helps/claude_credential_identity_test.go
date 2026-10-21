package helps

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestClaudeAgentSessionUUIDPreservesNativeSession(t *testing.T) {
	const sessionID = "11111111-2222-4333-8444-555555555555"
	got := ClaudeAgentSessionUUIDForRequest(http.Header{"X-Claude-Code-Session-Id": {sessionID}}, nil, nil, true)
	if got != sessionID {
		t.Fatalf("ClaudeAgentSessionUUIDForRequest() = %q, want native session %q", got, sessionID)
	}
}

func TestClaudeAgentSessionUUIDIgnoresUnconfirmedClaudeSignals(t *testing.T) {
	const nativeSessionID = "11111111-2222-4333-8444-555555555555"
	metadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "non-native-conversation"}
	got := ClaudeAgentSessionUUIDForRequest(
		http.Header{"X-Claude-Code-Session-Id": {nativeSessionID}},
		[]byte(`{"metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"}}`),
		nil,
		false,
		metadata,
	)
	if got == nativeSessionID {
		t.Fatalf("ClaudeAgentSessionUUIDForRequest() = native session %q for unconfirmed caller", got)
	}
	if repeated := ClaudeAgentSessionUUIDForRequest(nil, nil, nil, false, metadata); repeated != got {
		t.Fatalf("derived session changed: first=%q repeated=%q", got, repeated)
	}
}

func TestClaudeAgentSessionUUIDUsesExecutionAndDerivedIdentity(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
	}{
		{
			name:     "execution session",
			metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "agent-run-1"},
		},
		{
			name:     "derived session",
			metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:conversation-root"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := ClaudeAgentSessionUUID(nil, nil, nil, test.metadata)
			second := ClaudeAgentSessionUUID(nil, nil, nil, test.metadata)
			if first == "" || first != second {
				t.Fatalf("session UUIDs = %q and %q, want equal non-empty values", first, second)
			}
		})
	}
}

func TestApplyClaudeCredentialMetadataUsesCredentialDeviceAndPreservesExtras(t *testing.T) {
	deviceIDs := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		claudeauth.ClaudeDeviceIDsMetadataKey: deviceIDs,
	}}
	const sessionID = "11111111-2222-4333-8444-555555555555"
	body := []byte(`{"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":"{\"device_id\":\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\",\"account_uuid\":\"downstream-account\",\"session_id\":\"downstream-session\",\"parent_session_id\":\"parent-1\",\"extra\":true}"}}`)

	updated, selectedDevice, errApply := ApplyClaudeCredentialMetadata(body, auth, sessionID)
	if errApply != nil {
		t.Fatalf("ApplyClaudeCredentialMetadata() error = %v", errApply)
	}
	userID := gjson.GetBytes(updated, "metadata.user_id").String()
	if got := gjson.Get(userID, "device_id").String(); got != selectedDevice {
		t.Fatalf("device_id = %q, want selected %q", got, selectedDevice)
	}
	if got := gjson.Get(userID, "account_uuid").String(); got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %q, want credential account", got)
	}
	if got := gjson.Get(userID, "session_id").String(); got != sessionID {
		t.Fatalf("session_id = %q, want %q", got, sessionID)
	}
	if got := gjson.Get(userID, "parent_session_id").String(); got != "parent-1" {
		t.Fatalf("parent_session_id = %q, want preserved", got)
	}
	if !gjson.Get(userID, "extra").Bool() {
		t.Fatal("extra metadata was not preserved")
	}
	wantPrefix := `{"device_id":"` + selectedDevice + `","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"` + sessionID + `"`
	if !strings.HasPrefix(userID, wantPrefix) {
		t.Fatalf("metadata.user_id = %q, want credential identity fields first", userID)
	}
}

func TestApplyClaudeCredentialMetadataRejectsDuplicateIdentityContainers(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		claudeauth.ClaudeDeviceIDsMetadataKey: []string{
			"0000000000000000000000000000000000000000000000000000000000000000",
		},
	}}
	const sessionID = "11111111-2222-4333-8444-555555555555"
	tests := []struct {
		name string
		body string
	}{
		{
			name: "invalid request JSON",
			body: `{"messages":[],"metadata":`,
		},
		{
			name: "duplicate top-level metadata",
			body: `{"messages":[],"metadata":{"user_id":"{}"},"metadata":{"user_id":"{}"}}`,
		},
		{
			name: "duplicate metadata user ID",
			body: `{"messages":[],"metadata":{"user_id":"{}","user_id":"{}"}}`,
		},
		{
			name: "duplicate encoded account UUID",
			body: `{"messages":[],"metadata":{"user_id":"{\"account_uuid\":\"first\",\"account_uuid\":\"last\"}"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, errApply := ApplyClaudeCredentialMetadata([]byte(test.body), auth, sessionID)
			if errApply == nil {
				t.Fatal("ApplyClaudeCredentialMetadata() error = nil, want duplicate-key rejection")
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if !errors.As(errApply, &requestErr) || requestErr == nil || !requestErr.IsRequestScoped() {
				t.Fatalf("ApplyClaudeCredentialMetadata() error = %T %v, want request-scoped", errApply, errApply)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errApply, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("ApplyClaudeCredentialMetadata() error = %T %v, want HTTP 400", errApply, errApply)
			}
		})
	}
}

func TestApplyClaudeCredentialMetadataRequiresAccountUUID(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		claudeauth.ClaudeDeviceIDsMetadataKey: []string{
			"0000000000000000000000000000000000000000000000000000000000000000",
		},
	}}
	_, _, errApply := ApplyClaudeCredentialMetadata(
		[]byte(`{"messages":[]}`),
		auth,
		"11111111-2222-4333-8444-555555555555",
	)
	if errApply == nil {
		t.Fatal("ApplyClaudeCredentialMetadata() error = nil, want missing account UUID rejection")
	}
	var requestErr cliproxyexecutor.RequestScopedError
	if errors.As(errApply, &requestErr) && requestErr != nil && requestErr.IsRequestScoped() {
		t.Fatalf("missing credential identity error = %T %v, want credential-scoped", errApply, errApply)
	}
}
