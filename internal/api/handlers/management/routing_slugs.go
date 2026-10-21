package management

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/slugs"
)

// SetSlugResolver sets the follow-mode slug resolver used by the slug endpoints.
func (h *Handler) SetSlugResolver(resolver *slugs.Manager) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.slugResolver = resolver
	h.mu.Unlock()
}

func (h *Handler) currentSlugResolver() *slugs.Manager {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.slugResolver
}

// GetSlugs lists every OpenAI-compatible alias with its resolved upstream model.
func (h *Handler) GetSlugs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"slugs": h.currentSlugResolver().Statuses()})
}

// RefreshSlugs re-polls follow-mode groups immediately (all, or the group named
// by the "group" query parameter or JSON field) and returns the updated slugs.
func (h *Handler) RefreshSlugs(c *gin.Context) {
	resolver := h.currentSlugResolver()
	if resolver == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "slug resolver unavailable"})
		return
	}
	group := strings.TrimSpace(c.Query("group"))
	if group == "" && c.Request.Body != nil {
		var body struct {
			Group string `json:"group"`
		}
		if errBind := c.ShouldBindJSON(&body); errBind != nil && !errors.Is(errBind, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		group = strings.TrimSpace(body.Group)
	}
	if errRefresh := resolver.Refresh(c.Request.Context(), group); errRefresh != nil {
		if errors.Is(errRefresh, slugs.ErrGroupNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("openai-compatibility group %q not found", group)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("refresh slugs: %v", errRefresh)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"slugs": resolver.Statuses()})
}
