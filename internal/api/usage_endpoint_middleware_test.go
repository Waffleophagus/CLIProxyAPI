package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageEndpointMiddlewareStoresRouteInRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(usageEndpointMiddleware())
	var got string
	engine.POST("/v1/models/:model", func(c *gin.Context) {
		got = coreusage.EndpointFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/models/abc", nil))
	if got != "/v1/models/:model" {
		t.Fatalf("endpoint = %q, want the route template", got)
	}
}
