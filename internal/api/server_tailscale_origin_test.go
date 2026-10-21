package api

import (
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnet"
)

func withHeaders(kv ...string) func(*http.Request) {
	return func(req *http.Request) {
		for i := 0; i+1 < len(kv); i += 2 {
			req.Header.Add(kv[i], kv[i+1])
		}
	}
}

func withHost(host string, tlsConn bool, next func(*http.Request)) func(*http.Request) {
	return func(req *http.Request) {
		req.Host = host
		if tlsConn {
			req.TLS = &tls.ConnectionState{}
		}
		if next != nil {
			next(req)
		}
	}
}

// Identity-authorized management requests must be same-origin or non-browser,
// and addressed to the node itself; otherwise the management key is required.
var (
	identityOriginAllowed = map[string]func(*http.Request){
		"no origin (curl)":           nil,
		"sec-fetch-site none":        withHeaders("Sec-Fetch-Site", "none"),
		"same origin by IP":          withHeaders("Origin", "http://"+tailnetNodeHost, "Sec-Fetch-Site", "same-origin"),
		"same origin https":          withHost("cliproxy.tail1234.ts.net", true, withHeaders("Origin", "https://cliproxy.tail1234.ts.net", "Sec-Fetch-Site", "same-origin")),
		"same origin https port 443": withHost("cliproxy.tail1234.ts.net:443", true, withHeaders("Origin", "https://CLIPROXY.tail1234.ts.net")),
		"same origin http port":      withHost("cliproxy.tail1234.ts.net:8080", false, withHeaders("Origin", "http://cliproxy.tail1234.ts.net:8080")),
		"short magicdns name":        withHost("cliproxy:8080", false, withHeaders("Origin", "http://cliproxy:8080")),
	}
	identityOriginDenied = map[string]func(*http.Request){
		"foreign origin":                     withHeaders("Origin", "https://evil.example"),
		"foreign origin same-origin fetch":   withHeaders("Origin", "https://evil.example", "Sec-Fetch-Site", "same-origin"),
		"null origin":                        withHeaders("Origin", "null"),
		"empty origin":                       withHeaders("Origin", ""),
		"two origins":                        withHeaders("Origin", "http://"+tailnetNodeHost, "Origin", "https://evil.example"),
		"scheme mismatch":                    withHost("cliproxy.tail1234.ts.net", true, withHeaders("Origin", "http://cliproxy.tail1234.ts.net")),
		"port mismatch":                      withHost("cliproxy.tail1234.ts.net:8080", false, withHeaders("Origin", "http://cliproxy.tail1234.ts.net:9090")),
		"cross-site fetch without origin":    withHeaders("Sec-Fetch-Site", "cross-site"),
		"same-site fetch without origin":     withHeaders("Sec-Fetch-Site", "same-site"),
		"same origin but cross-site fetch":   withHeaders("Origin", "http://"+tailnetNodeHost, "Sec-Fetch-Site", "cross-site"),
		"dns rebinding host":                 withHost("evil.example:8080", false, nil),
		"dns rebinding host matching origin": withHost("evil.example:8080", false, withHeaders("Origin", "http://evil.example:8080")),
		"other tailnet node name":            withHost("other.tail1234.ts.net", false, nil),
	}
)

func originTestNode() *fakeTailnetNode {
	return &fakeTailnetNode{peer: tailnet.Peer{LoginName: "matt@github"}, status: tailnet.Status{State: "Running", DNSName: "cliproxy.tail1234.ts.net"}}
}

func TestTailnetIdentityOriginPolicyWithoutSecret(t *testing.T) {
	s := newTailnetTestServer(t, originTestNode(), "matt@github")
	for name, prepare := range identityOriginAllowed {
		if rr := serveTailnetRequest(s, http.MethodGet, statusPath, true, "", prepare); rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200; body=%s", name, rr.Code, rr.Body.String())
		}
	}
	for name, prepare := range identityOriginDenied {
		if rr := serveTailnetRequest(s, http.MethodGet, "/v8/management/config.yaml", true, "", prepare); rr.Code != http.StatusNotFound {
			t.Errorf("%s GET: status = %d, want 404; body=%s", name, rr.Code, rr.Body.String())
		}
		if rr := serveTailnetRequest(s, http.MethodPatch, "/v8/management/config", true, "", prepare); rr.Code != http.StatusNotFound {
			t.Errorf("%s PATCH: status = %d, want 404; body=%s", name, rr.Code, rr.Body.String())
		}
	}
}

func TestTailnetIdentityOriginPolicyWithSecret(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	s := newTailnetTestServer(t, originTestNode(), "matt@github")
	for name, prepare := range identityOriginAllowed {
		if rr := serveTailnetRequest(s, http.MethodGet, statusPath, true, "", prepare); rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200; body=%s", name, rr.Code, rr.Body.String())
		}
	}
	for name, prepare := range identityOriginDenied {
		// Keyless requests count toward the IP ban, so each case gets a fresh server.
		s := newTailnetTestServer(t, originTestNode(), "matt@github")
		if rr := serveTailnetRequest(s, http.MethodGet, "/v8/management/config.yaml", true, "", prepare); rr.Code != http.StatusUnauthorized {
			t.Errorf("%s GET: status = %d, want 401; body=%s", name, rr.Code, rr.Body.String())
		}
		if rr := serveTailnetRequest(s, http.MethodPatch, "/v8/management/config", true, "", prepare); rr.Code != http.StatusUnauthorized {
			t.Errorf("%s PATCH: status = %d, want 401; body=%s", name, rr.Code, rr.Body.String())
		}
	}
	// Key holders are unaffected by the identity origin policy.
	if rr := serveTailnetRequest(s, http.MethodGet, statusPath, true, "test-management-key", identityOriginDenied["foreign origin"]); rr.Code != http.StatusOK {
		t.Fatalf("key with foreign origin: status = %d, want 200", rr.Code)
	}
}

func TestTailnetManagementPreflightGrantsNothing(t *testing.T) {
	s := newTailnetTestServer(t, originTestNode(), "matt@github")
	rr := serveTailnetRequest(s, http.MethodOptions, "/v8/management/config", true, "", withHeaders(
		"Origin", "https://evil.example",
		"Access-Control-Request-Method", http.MethodPatch,
		"Access-Control-Request-Headers", "content-type",
	))
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("preflight Access-Control-Allow-Origin = %q, want none", got)
	}
	if rr.Code < http.StatusBadRequest {
		t.Fatalf("preflight status = %d, want an error status", rr.Code)
	}
}
