// Package webui serves the embedded management web UI (a static Svelte build).
//
// The build output lives in dist/ (produced by `pnpm -C web build`). When dist/
// holds no index.html the binary serves a small placeholder page instead, so
// plain `go build` works without Node.
package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/klauspost/compress/gzhttp"
)

// BasePath is the URL prefix the UI is served under.
const BasePath = "/ui/"

//go:embed all:dist
var distFS embed.FS

//go:embed placeholder/index.html
var placeholderHTML []byte

// contentSecurityPolicy only allows same-origin resources. Inline styles are
// permitted because Svelte sets style attributes for dynamic layout values.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// Handler serves the UI files with SPA fallback to index.html.
type Handler struct {
	files     fs.FS
	index     []byte
	indexETag string
	built     bool
	next      http.Handler
}

// New returns a handler for the embedded UI build, or the placeholder page
// when the UI was not built into this binary.
func New() *Handler {
	sub, errSub := fs.Sub(distFS, "dist")
	if errSub != nil {
		sub = nil
	}
	return newHandler(sub)
}

func newHandler(files fs.FS) *Handler {
	h := &Handler{files: files, index: placeholderHTML}
	if files != nil {
		if index, errRead := fs.ReadFile(files, "index.html"); errRead == nil {
			h.index = index
			h.built = true
		}
	}
	sum := sha256.Sum256(h.index)
	h.indexETag = `"` + hex.EncodeToString(sum[:16]) + `"`
	h.next = gzhttp.GzipHandler(http.HandlerFunc(h.serve))
	return h
}

// Built reports whether a real UI build (not the placeholder) is embedded.
func (h *Handler) Built() bool { return h != nil && h.built }

// ServeHTTP serves r.URL.Path relative to BasePath.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.next.ServeHTTP(w, r)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(BasePath, "/"))), "/")
	if name == "" || name == "index.html" || !h.built {
		h.serveIndex(w, r)
		return
	}
	data, errRead := fs.ReadFile(h.files, name)
	if errRead != nil {
		// Missing build assets must not fall back to HTML: browsers would report
		// a confusing MIME error instead of a 404.
		if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		h.serveIndex(w, r)
		return
	}
	if ctype := contentType(name); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	if strings.HasPrefix(name, "assets/") {
		// Vite fingerprints everything under assets/, so it never changes.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", h.indexETag)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json", ".map":
		return "application/json"
	case ".webmanifest":
		return "application/manifest+json"
	case ".woff2":
		return "font/woff2"
	case ".ico":
		return "image/x-icon"
	}
	return mime.TypeByExtension(path.Ext(name))
}

// WantsHTML reports whether a request looks like a browser page navigation.
func WantsHTML(r *http.Request) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}
