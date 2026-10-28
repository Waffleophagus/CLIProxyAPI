package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// TestResponsesInterruptInFlight verifies that a Codex response.interrupt sent while
// generation is running is forwarded unchanged to the current upstream socket, and that
// the same socket accepts the next response.create. Idle, malformed, and disabled-auth
// interrupts must fail locally without opening or writing that socket.
func TestResponsesInterruptInFlight(t *testing.T) {
	for _, steering := range []bool{false, true} {
		t.Run(fmt.Sprintf("steering_%t", steering), func(t *testing.T) {
			interrupt := []byte(`{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items","extension":"keep"}`)
			var connections atomic.Int32
			upstreamDone := make(chan struct{})
			var upstreamDoneOnce sync.Once
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connections.Add(1)
				defer upstreamDoneOnce.Do(func() { close(upstreamDone) })
				c, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade upstream: %v", errUpgrade)
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				read := func() []byte {
					_, payload, errRead := c.ReadMessage()
					if errRead != nil {
						t.Errorf("upstream read: %v", errRead)
					}
					return payload
				}
				write := func(payload string) {
					if errWrite := c.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Errorf("upstream write: %v", errWrite)
					}
				}
				if payload := read(); gjson.GetBytes(payload, "type").String() != "response.create" {
					t.Errorf("expected response.create, got %s", payload)
					return
				}
				write(`{"type":"response.created","response":{"id":"r1"}}`)
				// Hold completion until the in-flight interrupt arrives unchanged.
				if payload := read(); !bytes.Equal(payload, interrupt) {
					t.Errorf("interrupt changed: %s", payload)
					return
				}
				write(`{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"usage":{"output_tokens":7},"output":[]}}`)
				if payload := read(); gjson.GetBytes(payload, "type").String() != "response.create" {
					t.Errorf("expected follow-up response.create, got %s", payload)
					return
				}
				write(`{"type":"response.created","response":{"id":"r2"}}`)
				write(`{"type":"response.completed","response":{"id":"r2","status":"completed","output":[]}}`)
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()

			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = steering
			cfg.CodexResponseSteering = steering
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := fmt.Sprintf("interrupt-%t", steering)
			model := "interrupt-model-" + authID
			_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
				ID:       authID,
				Provider: "codex",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"api_key":    "test-key",
					"base_url":   upstream.URL,
					"websockets": "true",
				},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)

			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.GET("/v1/responses", handler.ResponsesWebsocket)
			downstream := httptest.NewServer(router)
			defer downstream.Close()

			client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() { _ = client.Close() }()
			_ = client.SetReadDeadline(time.Now().Add(8 * time.Second))
			send := func(payload []byte) {
				t.Helper()
				if errSend := client.WriteMessage(websocket.TextMessage, payload); errSend != nil {
					t.Fatal(errSend)
				}
			}
			expect := func(event string) {
				t.Helper()
				_, payload, errRead := client.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				if got := gjson.GetBytes(payload, "type").String(); got != event {
					t.Fatalf("expected %s, got %s", event, payload)
				}
			}

			send([]byte(`{"type":"response.interrupt"}`))
			expect("error")
			send(interrupt)
			expect("error")
			if connections.Load() != 0 {
				t.Fatal("idle interrupt opened an upstream connection")
			}

			send([]byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)))
			expect("response.created")
			send([]byte(`{"type":"response.interrupt","response_id":42}`))
			expect("error")
			auth, _ := manager.GetByID(authID)
			auth.Disabled = true
			if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			send(interrupt)
			expect("error")
			auth.Disabled = false
			if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			send(interrupt)
			expect("response.incomplete")
			send([]byte(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"r1","input":[]}`, model)))
			expect("response.created")
			expect("response.completed")
			_ = client.Close()
			select {
			case <-upstreamDone:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not close")
			}
			if got := connections.Load(); got != 1 {
				t.Fatalf("upstream connections=%d, want 1", got)
			}
		})
	}
}

type blockingHTTPInterruptExecutor struct {
	calls atomic.Int32
	// canceled is closed once the first (blocking) stream observes its context
	// being canceled. Cancellation reaches the stream goroutine asynchronously,
	// so tests must wait on this channel rather than sample a flag.
	canceled chan struct{}
}

func (*blockingHTTPInterruptExecutor) Identifier() string { return "codex" }

func (*blockingHTTPInterruptExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *blockingHTTPInterruptExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	call := e.calls.Add(1)
	chunks := make(chan coreexecutor.StreamChunk, 2)
	go func() {
		defer close(chunks)
		if call > 1 {
			chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.created","response":{"id":"r-http-2"}}`)}
			chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"r-http-2","status":"completed","output":[]}}`)}
			return
		}
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.created","response":{"id":"r-http"}}`)}
		<-ctx.Done()
		close(e.canceled)
	}()
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*blockingHTTPInterruptExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, errors.New("not implemented")
}

func (*blockingHTTPInterruptExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*blockingHTTPInterruptExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (*blockingHTTPInterruptExecutor) InterruptExecutionSession(context.Context, string, []byte) error {
	return coreexecutor.ErrNoActiveUpstreamWebsocket
}

// TestResponsesInterruptStopsHTTPUpstream verifies that an in-flight interrupt
// cancels an HTTP upstream turn and lets the same socket continue.
func TestResponsesInterruptStopsHTTPUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &blockingHTTPInterruptExecutor{canceled: make(chan struct{})}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{})
	manager.RegisterExecutor(executor)
	const authID = "interrupt-http-auth"
	const model = "interrupt-http-model"
	_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:         authID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "test-key"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)

	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() { _ = client.Close() }()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if errSend := client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))); errSend != nil {
		t.Fatal(errSend)
	}
	_, created, errRead := client.ReadMessage()
	if errRead != nil {
		t.Fatal(errRead)
	}
	if got := gjson.GetBytes(created, "type").String(); got != "response.created" {
		t.Fatalf("expected response.created, got %s", created)
	}
	if errSend := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"r-http","mode":"discard_partial_items"}`)); errSend != nil {
		t.Fatal(errSend)
	}
	_, interrupted, errInterrupted := client.ReadMessage()
	if errInterrupted != nil {
		t.Fatal(errInterrupted)
	}
	if got := gjson.GetBytes(interrupted, "type").String(); got != "response.incomplete" {
		t.Fatalf("expected response.incomplete, got %s", interrupted)
	}
	if got := gjson.GetBytes(interrupted, "response.id").String(); got != "r-http" {
		t.Fatalf("interrupted response id = %q", got)
	}
	if got := gjson.GetBytes(interrupted, "response.incomplete_details.reason").String(); got != "interrupted" {
		t.Fatalf("interrupt reason = %q, payload %s", got, interrupted)
	}
	select {
	case <-executor.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("http upstream was not canceled")
	}
	if errSend := client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"r-http","input":[]}`, model))); errSend != nil {
		t.Fatal(errSend)
	}
	_, followCreated, errFollow := client.ReadMessage()
	if errFollow != nil {
		t.Fatal(errFollow)
	}
	if got := gjson.GetBytes(followCreated, "type").String(); got != "response.created" {
		t.Fatalf("expected follow-up response.created, got %s", followCreated)
	}
	_, followDone, errDone := client.ReadMessage()
	if errDone != nil {
		t.Fatal(errDone)
	}
	if got := gjson.GetBytes(followDone, "type").String(); got != "response.completed" {
		t.Fatalf("expected follow-up response.completed, got %s", followDone)
	}
}
