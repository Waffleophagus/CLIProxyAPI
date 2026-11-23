package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIPayloadFailureDoesNotCommitHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Error(errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","error":{"code":"400","message":"rejected"}}`)); errWrite != nil {
			t.Error(errWrite)
		}
	}))
	defer server.Close()
	exec := NewXAIWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
	defer exec.CloseExecutionSession(t.Name())
	state := getXAIWebsocketIDState(exec.idStore, t.Name())
	state.replaceTranscriptWithItems([]byte(`{"type":"message","role":"user","content":"old history"}`))
	before := state.snapshotTranscriptInput()
	result, errExecute := exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(t.Context()), &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}, cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"type":"response.create","input":[{"role":"user","content":"failed reset"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}})
	failed := errExecute != nil
	if result != nil {
		for chunk := range result.Chunks {
			failed = failed || chunk.Err != nil
		}
	}
	if !failed {
		t.Fatal("expected upstream failure")
	}
	if after := state.snapshotTranscriptInput(); !bytes.Equal(before, after) {
		t.Fatalf("failed request committed/reset history: %s", after)
	}
}

func TestXAIPayloadSnapshotUsesTranslatedInput(t *testing.T) {
	exec := NewXAIWebsocketsExecutor(&config.Config{})
	req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"messages":[{"role":"user","content":"hero"},{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-1","content":"result"}]}`)}
	prepared, errPrepare := exec.prepareResponsesWebsocketRequest(t.Context(), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	snapshot := xaiJSONRawMessages(gjson.GetBytes(prepared.body, "input"))
	state := &xaiWebsocketIDState{}
	state.recordTranscriptTurn(snapshot, []byte(`{"response":{"output":[]}}`), true)
	input := gjson.ParseBytes(state.snapshotTranscriptInput()).Array()
	if len(input) != 3 || input[0].Get("role").String() != "user" || input[1].Get("type").String() != "function_call" || input[1].Get("call_id").String() != "call-1" || input[2].Get("type").String() != "function_call_output" || input[2].Get("call_id").String() != "call-1" {
		t.Fatalf("history must use translated provider input, not raw messages: %s", state.snapshotTranscriptInput())
	}
}

// TestXAIPayloadTransportRetryReusesFinalFrame is adapted from upstream
// b4410dd59 with filter/override rules in place of the replace rules the fork
// does not have: a transport retry must resend the exact finalized frame
// without evaluating payload rules again, and the transcript must keep the
// unconfigured input, including items the rules filtered out.
func TestXAIPayloadTransportRetryReusesFinalFrame(t *testing.T) {
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			return // The first connection intentionally fails before writing.
		}
		captured <- bytes.Clone(body)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r","output":[]}}`)); errWrite != nil {
			t.Error(errWrite)
		}
	}))
	defer server.Close()
	var writes atomic.Int32
	testWebsocketWritePayloadHook = func(conn *websocket.Conn) {
		if writes.Add(1) == 1 {
			_ = conn.SetWriteDeadline(time.Now().Add(-time.Second))
		}
	}
	defer func() { testWebsocketWritePayloadHook = nil }()
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"metadata.marker": "configured"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"input.0"}}},
	}}
	exec := NewXAIWebsocketsExecutor(cfg)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
	defer exec.CloseExecutionSession(t.Name())
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.WithValue(t.Context(), "gin", ginCtx))
	finalized := 0
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey:                t.Name(),
		cliproxyexecutor.FinalizedResponseFormatCallbackMetadataKey: func(string) { finalized++ },
	}}
	before := helps.PayloadConfigEvaluationCount()
	result, errExecute := exec.ExecuteStream(ctx, &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}, cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"input":[{"role":"assistant","content":"drop"},{"role":"user","content":"hero"},{"role":"user","content":"last"}]}`)}, opts)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	if writes.Load() != 2 || finalized != 1 || helps.PayloadConfigEvaluationCount()-before != 1 {
		t.Fatalf("writes=%d finalizations=%d evaluations=%d, want 2/1/1", writes.Load(), finalized, helps.PayloadConfigEvaluationCount()-before)
	}
	body := <-captured
	if gjson.GetBytes(body, "input.#").Int() != 2 || gjson.GetBytes(body, "input.0.content").String() != "hero" || gjson.GetBytes(body, "metadata.marker").String() != "configured" {
		t.Fatalf("payload rules repeated or lost during retry: %s", body)
	}
	value, _ := ginCtx.Get(logging.DeferredAPIRequestContextKey)
	requests, ok := value.([]logging.DeferredAPIRequest)
	if !ok || len(requests) != 2 {
		t.Fatalf("expected both send attempts in request log, got %T", value)
	}
	for _, request := range requests {
		if !bytes.Contains(request(), body) {
			t.Fatalf("attempt body differs from final frame: %s", request())
		}
	}
	if got := getXAIWebsocketIDState(exec.idStore, t.Name()).snapshotTranscriptInput(); bytes.Contains(got, []byte("configured")) || len(gjson.ParseBytes(got).Array()) != 3 {
		t.Fatalf("history must contain unconfigured input, including filtered items: %s", got)
	}
}
