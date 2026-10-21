package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type issue6332Writer struct {
	*httptest.ResponseRecorder
	cancel     context.CancelFunc
	event      string
	failWrite  bool
	shortWrite bool
	failFlush  bool
}

func (w *issue6332Writer) Write(p []byte) (int, error) {
	if w.failWrite && strings.Contains(string(p), w.event) {
		if w.shortWrite {
			return len(p) - 1, nil
		}
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}
func (w *issue6332Writer) Flush() {
	w.ResponseRecorder.Flush()
	if strings.Contains(w.Body.String(), w.event) {
		w.cancel()
	}
}

func (w *issue6332Writer) FlushError() error {
	w.Flush()
	if w.failFlush && strings.Contains(w.Body.String(), w.event) {
		return io.ErrClosedPipe
	}
	return nil
}

func TestIssue6332PendingCompletionWriteFailure(t *testing.T) {
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	writer := &issue6332Writer{ResponseRecorder: httptest.NewRecorder(), cancel: func() {}, event: "response.completed", failWrite: true}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	data := make(chan []byte, 1)
	data <- []byte(`data: {"type":"response.completed"}`)
	close(data)
	var outcome error
	h.forwardResponsesStream(c, c.Writer, func(err error) { outcome = err }, data, nil, nil)
	if outcome == nil {
		t.Fatal("buffered completion write failure was reported as success")
	}
}

func TestIssue6332MiddlewareFlushError(t *testing.T) {
	for _, requestLogging := range []bool{false, true} {
		name := "trace"
		if requestLogging {
			name = "trace_and_request_log"
		}
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) { logging.SetGinRequestID(c, "6332"); c.Next() })
			router.Use(logging.CPATraceIDMiddleware())
			logsDir := t.TempDir()
			if requestLogging {
				router.Use(middleware.RequestLoggingMiddleware(logging.NewFileRequestLogger(true, logsDir, "", 0)))
			}
			router.POST("/v1/responses", func(c *gin.Context) {
				logging.SetGinCPATraceID(c, "test-auth")
				c.Header("Content-Type", "text/event-stream")
				// Flush before the first write must still commit Gin status and trace headers.
				if errFlush := flushResponsesSSE(c.Writer); !errors.Is(errFlush, io.ErrClosedPipe) {
					t.Errorf("middleware flush error = %v, want closed pipe", errFlush)
				}
				if !c.Writer.Written() {
					t.Error("flush did not commit Gin headers")
				}
				_, _ = c.Writer.Write([]byte("data: logged-response\n\n"))
			})
			writer := &issue6332Writer{ResponseRecorder: httptest.NewRecorder(), cancel: func() {}, failFlush: true}
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"stream":true}`)))
			if writer.Result().Header.Get(logging.CPATraceIDHeader) == "" {
				t.Error("flush skipped trace header behavior")
			}
			if requestLogging {
				files, errReadDir := os.ReadDir(logsDir)
				if errReadDir != nil {
					t.Fatal(errReadDir)
				}
				found := false
				for _, file := range files {
					data, errReadFile := os.ReadFile(filepath.Join(logsDir, file.Name()))
					if errReadFile != nil {
						t.Fatal(errReadFile)
					}
					found = found || strings.Contains(string(data), "logged-response")
				}
				if !found {
					t.Error("middleware did not capture response body")
				}
			}
		})
	}
}
