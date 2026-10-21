package slugs

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const maxModelsResponseBytes = 4 << 20

// FetchModels performs GET {base-url}/models with the group's first key,
// headers and proxy settings, returning data[].id in endpoint order.
// The caller's context carries the per-probe timeout.
func FetchModels(ctx context.Context, target Target) (ids []string, err error) {
	endpoint := strings.TrimSuffix(strings.TrimSpace(target.BaseURL), "/") + "/models"
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("build models request: %w", errRequest)
	}
	req.Header.Set("Accept", "application/json")
	for name, value := range target.Headers {
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		// "$Header" values copy a client request header; a probe has no client.
		if name == "" || value == "" || strings.HasPrefix(value, "$") {
			continue
		}
		req.Header.Set(name, value)
	}
	if target.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+target.APIKey)
	}

	proxyCfg := &config.Config{}
	proxyCfg.ProxyURL = target.GlobalProxyURL
	client := helps.NewProxyAwareHTTPClient(ctx, proxyCfg, &cliproxyauth.Auth{ProxyURL: target.ProxyURL}, 0)
	client.CheckRedirect = sameOriginRedirect
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("GET models: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("close models response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponseBytes))
	if errRead != nil {
		return nil, fmt.Errorf("read models response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("GET models: upstream status %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("GET models: response is not JSON")
	}
	for _, item := range gjson.GetBytes(body, "data").Array() {
		if id := strings.TrimSpace(item.Get("id").String()); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// maxProbeRedirects mirrors net/http's default redirect limit.
const maxProbeRedirects = 10

// sameOriginRedirect lets the probe follow a redirect only when it keeps the
// original scheme, hostname and port. Default net/http handling would forward
// custom credential headers to any host and Authorization to other ports.
func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxProbeRedirects {
		return fmt.Errorf("stopped after %d redirects", maxProbeRedirects)
	}
	if len(via) == 0 {
		return nil
	}
	if from, to := via[0].URL, req.URL; !sameOrigin(from, to) {
		return fmt.Errorf("refusing cross-origin redirect from %s to %s", originString(from), originString(to))
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// originString renders scheme://host:port without path, query or userinfo.
func originString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(u.Hostname(), effectivePort(u))
}
