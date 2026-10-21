package management

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// RefreshAuthFiles triggers active refresh for a single auth file or all auth files.
// Accepts query parameters (?all=true, ?name=file.json) or JSON body ({"all": true, "name": "file.json"}).
func (h *Handler) RefreshAuthFiles(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		All       bool   `json:"all"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if errBind := c.ShouldBindJSON(&req); errBind != nil && !errors.Is(errBind, io.EOF) {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errBind, http.StatusBadRequest), gin.H{"error": "invalid request body: " + errBind.Error()})
			return
		}
	}
	if c.Query("all") == "true" {
		req.All = true
	}
	if queryName := strings.TrimSpace(c.Query("name")); queryName != "" && req.Name == "" {
		req.Name = queryName
	}
	if queryAuthIndex := strings.TrimSpace(c.Query("auth_index")); queryAuthIndex != "" && req.AuthIndex == "" {
		req.AuthIndex = queryAuthIndex
	}

	ctx := c.Request.Context()
	// Responses describe credentials; never let an intermediary cache them.
	c.Header("Cache-Control", "no-store")

	if req.All {
		results := h.authManager.ForceRefreshAll(ctx)
		c.JSON(http.StatusOK, gin.H{
			"ok":      true,
			"results": results,
		})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name or all=true is required"})
		return
	}

	targetAuth, ok := h.lookupAuthFile(name, req.AuthIndex)
	if !ok || targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	refreshed, err := h.authManager.ForceRefreshAuth(ctx, targetAuth.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	if refreshed == nil {
		refreshed = targetAuth
	}
	summary := refreshedAuthSummary(refreshed)
	if summary["auth_index"] == "" {
		summary["auth_index"] = targetAuth.Index
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":   true,
		"auth": summary,
	})
}

// refreshedAuthSummary describes a credential after a refresh without any
// secret material: no metadata (access/refresh/id tokens), attributes (API
// keys) or proxy URL (may embed credentials).
func refreshedAuthSummary(auth *coreauth.Auth) gin.H {
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	summary := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"name":           name,
		"provider":       strings.TrimSpace(auth.Provider),
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
	}
	if email := authEmail(auth); email != "" {
		summary["email"] = email
	}
	if !auth.LastRefreshedAt.IsZero() {
		summary["last_refresh"] = auth.LastRefreshedAt.UTC().Format(time.RFC3339)
	}
	if !auth.NextRefreshAfter.IsZero() {
		summary["next_refresh_after"] = auth.NextRefreshAfter.UTC().Format(time.RFC3339)
	}
	if expiry, ok := auth.AccessTokenExpirationTime(); ok && !expiry.IsZero() {
		summary["expires_at"] = expiry.UTC().Format(time.RFC3339)
	}
	return summary
}
