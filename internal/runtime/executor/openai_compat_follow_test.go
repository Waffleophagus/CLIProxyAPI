package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatFollowPendingFailsFastWithoutCooldown(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "dgx-spark",
		BaseURL: server.URL + "/v1",
		Models:  []config.OpenAICompatibilityModel{{Name: "DGX-SPARK", Alias: "DGX-SPARK", Follow: true, FollowPending: true}},
	}}}
	exec := NewOpenAICompatExecutor("dgx-spark", cfg)
	auth := &cliproxyauth.Auth{ID: "a", Provider: "dgx-spark", Attributes: map[string]string{
		"base_url": server.URL + "/v1", "compat_name": "dgx-spark", "provider_key": "dgx-spark",
	}}
	req := cliproxyexecutor.Request{Model: "DGX-SPARK(high)", Payload: []byte(`{"model":"DGX-SPARK","messages":[{"role":"user","content":"hi"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}

	_, errExecute := exec.Execute(context.Background(), auth, req, opts)
	_, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	for _, err := range []error{errExecute, errStream} {
		var pending openAICompatFollowPendingErr
		if !errors.As(err, &pending) || pending.StatusCode() != http.StatusServiceUnavailable || !pending.IsRequestScoped() || pending.IsCredentialScoped() {
			t.Fatalf("error = %T %v, want request-scoped 503", err, err)
		}
		if !strings.Contains(err.Error(), "slug DGX-SPARK") {
			t.Fatalf("error message = %q", err.Error())
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream contacted %d times for a pending slug", hits.Load())
	}
}
