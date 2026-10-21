package webui

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const testIndex = `<!doctype html><html><head><script type="module" src="/ui/assets/index-abc123.js"></script></head><body><div id="app"></div></body></html>`

func builtHandler() *Handler {
	return newHandler(fstest.MapFS{
		"index.html":              {Data: []byte(testIndex)},
		"favicon.svg":             {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)},
		"assets/index-abc123.js":  {Data: []byte(strings.Repeat("console.log('ui');\n", 200))},
		"assets/index-abc123.css": {Data: []byte("body{color:red}")},
	})
}

func serve(h http.Handler, method, target string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestServesIndexWithNoCacheAndSecurityHeaders(t *testing.T) {
	h := builtHandler()
	for _, target := range []string{"/ui/", "/ui/index.html"} {
		rr := serve(h, http.MethodGet, target, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", target, rr.Code)
		}
		if got := rr.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("%s: content type = %q", target, got)
		}
		if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("%s: cache-control = %q", target, got)
		}
		if rr.Body.String() != testIndex {
			t.Fatalf("%s: unexpected body %q", target, rr.Body.String())
		}
		assertSecurityHeaders(t, rr.Header())
	}
}

func assertSecurityHeaders(t *testing.T, h http.Header) {
	t.Helper()
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff")
	}
	if h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing X-Frame-Options")
	}
	if h.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing Referrer-Policy")
	}
}

func TestServesHashedAssetsWithLongCache(t *testing.T) {
	h := builtHandler()
	rr := serve(h, http.MethodGet, "/ui/assets/index-abc123.js", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache-control = %q", got)
	}
	assertSecurityHeaders(t, rr.Header())

	css := serve(h, http.MethodGet, "/ui/assets/index-abc123.css", nil)
	if got := css.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("css content type = %q", got)
	}
	icon := serve(h, http.MethodGet, "/ui/favicon.svg", nil)
	if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/svg+xml" || icon.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("favicon: status=%d type=%q cache=%q", icon.Code, icon.Header().Get("Content-Type"), icon.Header().Get("Cache-Control"))
	}
}

func TestGzipsCompressibleAssets(t *testing.T) {
	h := builtHandler()
	rr := serve(h, http.MethodGet, "/ui/assets/index-abc123.js", map[string]string{"Accept-Encoding": "gzip"})
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip encoding, got %q", rr.Header().Get("Content-Encoding"))
	}
	zr, errReader := gzip.NewReader(rr.Body)
	if errReader != nil {
		t.Fatalf("gzip reader: %v", errReader)
	}
	body, errRead := io.ReadAll(zr)
	if errRead != nil || !strings.HasPrefix(string(body), "console.log") {
		t.Fatalf("unexpected decoded body: %v %q", errRead, body)
	}
}

func TestSPAFallbackAndMissingAssets(t *testing.T) {
	h := builtHandler()
	for _, target := range []string{"/ui/usage", "/ui/settings/advanced", "/ui/accounts?x=1"} {
		rr := serve(h, http.MethodGet, target, nil)
		if rr.Code != http.StatusOK || rr.Body.String() != testIndex {
			t.Fatalf("%s: want index fallback, got %d %q", target, rr.Code, rr.Body.String())
		}
		if rr.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: fallback must not be cached", target)
		}
	}
	for _, target := range []string{"/ui/assets/missing-123.js", "/ui/missing.css"} {
		if rr := serve(h, http.MethodGet, target, nil); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", target, rr.Code)
		}
	}
	// Path traversal stays inside the embedded tree.
	if rr := serve(h, http.MethodGet, "/ui/../../etc/passwd", nil); rr.Code == http.StatusOK && rr.Body.String() != testIndex {
		t.Fatalf("traversal served unexpected content")
	}
}

func TestRejectsNonGetMethods(t *testing.T) {
	rr := serve(builtHandler(), http.MethodPost, "/ui/", nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rr.Code)
	}
	head := serve(builtHandler(), http.MethodHead, "/ui/", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD: status=%d body=%d", head.Code, head.Body.Len())
	}
}

func TestPlaceholderWhenNotBuilt(t *testing.T) {
	h := newHandler(fstest.MapFS{".keep": {Data: nil}})
	if h.Built() {
		t.Fatalf("expected placeholder handler")
	}
	for _, target := range []string{"/ui/", "/ui/usage", "/ui/assets/index.js"} {
		rr := serve(h, http.MethodGet, target, nil)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "UI not built") {
			t.Fatalf("%s: want placeholder, got %d %q", target, rr.Code, rr.Body.String())
		}
		assertSecurityHeaders(t, rr.Header())
	}
}

func TestEmbeddedHandlerServesSomething(t *testing.T) {
	// Works with both the committed placeholder and a real build.
	rr := serve(New(), http.MethodGet, "/ui/", nil)
	if rr.Code != http.StatusOK || !strings.Contains(strings.ToLower(rr.Body.String()), "<!doctype html>") {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestWantsHTML(t *testing.T) {
	cases := []struct {
		method, accept string
		want           bool
	}{
		{http.MethodGet, "text/html,application/xhtml+xml,*/*;q=0.8", true},
		{http.MethodGet, "application/json", false},
		{http.MethodGet, "*/*", false},
		{http.MethodGet, "", false},
		{http.MethodPost, "text/html", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/", nil)
		if c.accept != "" {
			req.Header.Set("Accept", c.accept)
		}
		if got := WantsHTML(req); got != c.want {
			t.Fatalf("%s %q: got %v want %v", c.method, c.accept, got, c.want)
		}
	}
}
