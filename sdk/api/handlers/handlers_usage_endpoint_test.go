package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// TestGetContextWithCancelCapturesEndpointBeforeGinContextReuse reads the
// usage endpoint from a request context after its handler returned and while
// gin serves another request (typically reusing the pooled gin.Context), as a
// streaming executor goroutine publishing usage late would.
func TestGetContextWithCancelCapturesEndpointBeforeGinContextReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	engine := gin.New()

	var firstCtx context.Context
	var cancelFirst APIHandlerCancelFunc
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		firstCtx, cancelFirst = handler.GetContextWithCancel(nil, c, context.Background())
		c.Status(http.StatusOK)
	})
	read := make(chan string, 1)
	engine.POST("/v1/messages", func(c *gin.Context) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			read <- coreusage.EndpointFromContext(firstCtx)
		}()
		<-done
		c.Status(http.StatusOK)
	})

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	defer cancelFirst()

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if got := <-read; got != "/v1/chat/completions" {
		t.Fatalf("endpoint = %q, want the route captured on the request goroutine", got)
	}
}
