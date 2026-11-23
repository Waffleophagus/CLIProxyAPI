package executor

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
