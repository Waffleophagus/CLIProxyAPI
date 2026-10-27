package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
)

// registerManagementV8Routes defines the v8 management API contract.
// Configuration paths mirror the v8 YAML tree; operational routes use its groups.
func (s *Server) registerManagementV8Routes() {
	const prefix = "/v8/management"
	s.engine.GET(prefix+"/oauth/callback", s.managementAvailabilityMiddleware(), s.mgmt.GetOAuthCallbackV8)
	s.engine.POST(prefix+"/oauth/callback", s.managementAvailabilityMiddleware(), s.mgmt.PostOAuthCallbackV8)

	v8 := s.engine.Group(prefix)
	v8.Use(s.managementAvailabilityMiddleware(), s.managementAuthMiddleware(), func(c *gin.Context) {
		c.Set(management.ConfigV8ContextKey, true)
	})
	v8.GET("/config", s.mgmt.ConfigV8)
	v8.PUT("/config", s.mgmt.ConfigV8)
	v8.PATCH("/config", s.mgmt.ConfigV8)
	v8.GET("/config.yaml", s.mgmt.ConfigV8)
	v8.PUT("/config.yaml", s.mgmt.ConfigV8)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		v8.Handle(method, "/config/*path", s.mgmt.ConfigV8)
	}

	v8.POST("/requests/api-call", s.mgmt.APICall)
	v8.POST("/routing/cooldown/reset", s.mgmt.ResetQuota)
	v8.GET("/routing/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
	v8.GET("/routing/slugs", s.mgmt.GetSlugs)
	v8.POST("/routing/slugs/refresh", s.mgmt.RefreshSlugs)

	v8.GET("/observability/logs", s.mgmt.GetLogs)
	v8.DELETE("/observability/logs", s.mgmt.DeleteLogs)
	v8.GET("/observability/logs/errors", s.mgmt.GetRequestErrorLogs)
	v8.GET("/observability/logs/errors/:name", s.mgmt.DownloadRequestErrorLog)
	v8.GET("/observability/logs/requests/:id", s.mgmt.GetRequestLogByID)
	v8.GET("/observability/usage/api-keys", s.mgmt.GetAPIKeyUsage)
	v8.GET("/observability/usage/summary", s.mgmt.GetUsageSummary)
	v8.GET("/observability/usage/timeseries", s.mgmt.GetUsageTimeseries)
	v8.GET("/observability/usage/requests", s.mgmt.GetUsageRequests)
	v8.GET("/observability/usage/pricing", s.mgmt.GetUsagePricing)
	v8.POST("/observability/usage/pricing/refresh", s.mgmt.RefreshUsagePricing)
	v8.GET("/observability/usage/pricing/catalog", s.mgmt.SearchUsagePricingCatalog)
	v8.GET("/observability/usage/pricing/models", s.mgmt.GetUsagePricingModels)
	v8.GET("/observability/usage/pricing/unpriced-local", s.mgmt.GetUnpricedLocalModels)

	v8.GET("/credentials", s.mgmt.ListAuthFiles)
	v8.POST("/credentials", s.mgmt.UploadAuthFileV8)
	v8.DELETE("/credentials", s.mgmt.DeleteAuthFileV8)
	v8.GET("/credentials/models", s.mgmt.GetAuthFileModels)
	v8.GET("/credentials/download", s.mgmt.DownloadAuthFile)
	v8.PATCH("/credentials/status", s.mgmt.PatchAuthFileStatus)
	v8.PATCH("/credentials/fields", s.mgmt.PatchAuthFileFields)
	v8.POST("/credentials/refresh", s.mgmt.RefreshAuthFiles)
	v8.GET("/oauth/auth-url", s.mgmt.StartOAuthV8)
	v8.POST("/oauth/siwc/bind", s.mgmt.StartSIWCBind)
	v8.GET("/oauth/siwc/status", s.mgmt.GetSIWCStatus)
	v8.GET("/credentials/siwc/status", s.mgmt.GetSIWCStatus)
	v8.GET("/oauth/status", s.mgmt.GetAuthStatus)
	v8.DELETE("/oauth/session", s.mgmt.CancelAuthSession)

	v8.GET("/tailscale/status", s.getTailscaleStatus)

}
