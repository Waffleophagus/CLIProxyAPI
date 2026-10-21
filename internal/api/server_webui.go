package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/webui"
)

// webUIEnabled reports whether the embedded web UI is served. It follows
// management.disable-ui on hot reload.
func (s *Server) webUIEnabled() bool {
	if s == nil || s.webui == nil {
		return false
	}
	cfg := s.getConfig()
	return cfg == nil || !cfg.RemoteManagement.DisableUI
}

// registerWebUIRoutes serves the static UI under /ui/. The assets are public;
// every piece of data the UI shows comes from the authenticated management API.
func (s *Server) registerWebUIRoutes() {
	if s.webui == nil {
		s.webui = webui.New()
	}
	redirect := func(c *gin.Context) {
		if !s.webUIEnabled() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Redirect(http.StatusMovedPermanently, webui.BasePath)
	}
	serve := func(c *gin.Context) {
		if !s.webUIEnabled() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		s.webui.ServeHTTP(c.Writer, c.Request)
	}
	s.engine.GET("/ui", redirect)
	s.engine.HEAD("/ui", redirect)
	s.engine.GET("/ui/*filepath", serve)
	s.engine.HEAD("/ui/*filepath", serve)
}
