package slugs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const modelsBody = `{"object":"list","data":[{"id":"model-a"}]}`

func credentialTarget(baseURL string) Target {
	return Target{
		BaseURL: baseURL,
		APIKey:  "sk-secret",
		Headers: map[string]string{"X-Api-Key": "header-secret"},
	}
}

func TestFetchModelsFollowsSameOriginRedirectWithCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			http.Redirect(w, r, "/v1/models/", http.StatusMovedPermanently)
		case "/v1/models/":
			if r.Header.Get("Authorization") != "Bearer sk-secret" || r.Header.Get("X-Api-Key") != "header-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(modelsBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ids, err := FetchModels(context.Background(), credentialTarget(server.URL+"/v1"))
	if err != nil {
		t.Fatalf("FetchModels() error = %v", err)
	}
	if len(ids) != 1 || ids[0] != "model-a" {
		t.Fatalf("ids = %v, want [model-a]", ids)
	}
}

func TestFetchModelsRefusesCrossOriginRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Errorf("redirect target received credentials: Authorization=%q X-Api-Key=%q", r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
		}
		_, _ = w.Write([]byte(modelsBody))
	}))
	defer target.Close()
	targetURL, errParse := url.Parse(target.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}

	tests := []struct {
		name     string
		location string
	}{
		// Same hostname, different port: net/http would forward Authorization.
		{name: "cross-port", location: "http://127.0.0.1:" + targetURL.Port() + "/v1/models"},
		// Different hostname: net/http would forward custom headers like X-Api-Key.
		{name: "cross-host", location: "http://localhost:" + targetURL.Port() + "/v1/models"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetHits.Store(0)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tt.location, http.StatusFound)
			}))
			defer origin.Close()

			ids, err := FetchModels(context.Background(), credentialTarget(origin.URL+"/v1"))
			if err == nil {
				t.Fatalf("FetchModels() = %v, want cross-origin redirect error", ids)
			}
			if !strings.Contains(err.Error(), "cross-origin redirect") {
				t.Fatalf("FetchModels() error = %v, want cross-origin redirect error", err)
			}
			if hits := targetHits.Load(); hits != 0 {
				t.Fatalf("redirect target received %d requests, want 0", hits)
			}
		})
	}
}

func TestSameOriginUsesEffectivePorts(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"http://example.com/v1/models", "http://example.com:80/v1/models/", true},
		{"https://Example.com/v1/models", "https://example.com:443/x", true},
		{"http://example.com/v1/models", "https://example.com/v1/models", false},
		{"https://example.com/v1/models", "https://example.com:8443/v1/models", false},
		{"https://example.com/v1/models", "https://api.example.com/v1/models", false},
	}
	for _, tt := range tests {
		a, errA := url.Parse(tt.a)
		b, errB := url.Parse(tt.b)
		if errA != nil || errB != nil {
			t.Fatalf("parse: %v %v", errA, errB)
		}
		if got := sameOrigin(a, b); got != tt.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
