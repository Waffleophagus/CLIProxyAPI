package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Server) registerManagementRoutes() {
	if s == nil || s.engine == nil || s.mgmt == nil {
		return
	}
	if !s.managementRoutesRegistered.CompareAndSwap(false, true) {
		return
	}

	log.Info("management routes registered")

	s.registerManagementV8Routes()
}

func (s *Server) managementAvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.managementAvailable(c) {
			return
		}
		c.Next()
	}
}

func (s *Server) managementAvailable(c *gin.Context) bool {
	cfg := s.getConfig()
	if s == nil || cfg == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	// Without a management secret, only identity-authorized tailnet callers see
	// the management API; everyone else (including all LAN callers) gets 404.
	if !s.managementRoutesEnabled.Load() && !s.tailnetManagementAuthorized(c) {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	return true
}
