package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tailnet"
	log "github.com/sirupsen/logrus"
)

// TailnetNode is the part of the embedded tailscale node the API server uses.
type TailnetNode interface {
	// WhoIs returns the tailnet identity behind a remote "ip:port" address.
	WhoIs(ctx context.Context, remoteAddr string) (tailnet.Peer, error)
	// Status reports the node state.
	Status(ctx context.Context) (tailnet.Status, error)
	// Settings returns the settings the node was started with.
	Settings() tailnet.Settings
}

const tailnetPeerContextKey = "tailnet.peer"

// tailnetManagementConfigured reports whether tailnet identities may use the
// management API: the node runs and management-users is non-empty.
func (s *Server) tailnetManagementConfigured(cfg *config.Config) bool {
	return s != nil && s.tailnet != nil && cfg != nil && cfg.Tailscale.Enabled && len(cfg.Tailscale.ManagementUsers) > 0
}

// tailnetPeer resolves the caller's tailnet identity once per request. Only
// connections accepted on a tailnet listener are looked up, always by the
// connection's remote address (never forwarded headers), so LAN callers can
// never assume a tailnet identity.
func (s *Server) tailnetPeer(c *gin.Context) (tailnet.Peer, bool) {
	if value, exists := c.Get(tailnetPeerContextKey); exists {
		peer, _ := value.(*tailnet.Peer)
		if peer == nil {
			return tailnet.Peer{}, false
		}
		return *peer, true
	}
	var resolved *tailnet.Peer
	if s != nil && s.tailnet != nil && c.Request != nil && tailnet.ViaTailnet(c.Request.Context()) {
		peer, errWhoIs := s.tailnet.WhoIs(c.Request.Context(), c.Request.RemoteAddr)
		if errWhoIs != nil {
			log.Debugf("tailscale: whois lookup failed: %v", errWhoIs)
		} else {
			resolved = &peer
		}
	}
	c.Set(tailnetPeerContextKey, resolved)
	if resolved == nil {
		return tailnet.Peer{}, false
	}
	return *resolved, true
}

// tailnetManagementAuthorized reports whether the request came over the tailnet
// from a user listed in tailscale.management-users. Tagged nodes have no user
// identity and are never authorized this way.
//
// Identity is ambient authority: any page open in a browser on an authorized
// device could otherwise use it. So it only applies to requests that are
// same-origin or not from a browser, addressed to one of the node's own names
// (which defeats DNS rebinding). Everything else needs the management key.
func (s *Server) tailnetManagementAuthorized(c *gin.Context) bool {
	if value, exists := c.Get(tailnetManagementAuthorizedContextKey); exists {
		authorized, _ := value.(bool)
		return authorized
	}
	authorized := s.resolveTailnetManagementAuthorized(c)
	c.Set(tailnetManagementAuthorizedContextKey, authorized)
	return authorized
}

const tailnetManagementAuthorizedContextKey = "tailnet.management_authorized"

func (s *Server) resolveTailnetManagementAuthorized(c *gin.Context) bool {
	cfg := s.getConfig()
	if !s.tailnetManagementConfigured(cfg) {
		return false
	}
	peer, ok := s.tailnetPeer(c)
	if !ok || peer.Tagged || !cfg.Tailscale.IsManagementUser(peer.LoginName) {
		return false
	}
	if !sameOriginOrNonBrowser(c.Request) {
		log.Debugf("tailscale: identity auth refused for cross-origin request from %s (Origin %q, Sec-Fetch-Site %q)",
			peer.LoginName, c.Request.Header.Get("Origin"), c.Request.Header.Get("Sec-Fetch-Site"))
		return false
	}
	if !s.tailnetHostAllowed(c.Request) {
		log.Debugf("tailscale: identity auth refused for request to unexpected host %q", c.Request.Host)
		return false
	}
	return true
}

// sameOriginOrNonBrowser reports whether a request was sent same-origin or by
// a non-browser client. Browsers label cross-site requests with
// Sec-Fetch-Site and send Origin on cross-origin and state-changing requests;
// clients like curl send neither. A present Origin must exactly match the
// request's own scheme and host.
func sameOriginOrNonBrowser(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "", "same-origin", "none":
	default:
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) > 1 {
		return false
	}
	origin, errParse := url.Parse(strings.TrimSpace(origins[0]))
	if errParse != nil || origin.Host == "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") ||
		origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin.Scheme, scheme) &&
		normalizeOriginHost(scheme, origin.Host) == normalizeOriginHost(scheme, r.Host)
}

// normalizeOriginHost lowercases host[:port] and drops the scheme's default port.
func normalizeOriginHost(scheme, host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	switch scheme {
	case "https":
		return strings.TrimSuffix(host, ":443")
	case "http":
		return strings.TrimSuffix(host, ":80")
	}
	return host
}

// tailnetHostAllowed reports whether the request's Host names this node: an
// IP literal (a browser only sends one when it connected to that address) or
// the node's MagicDNS name, full or short. Any other name may be an
// attacker's domain rebound to the node's tailnet address.
func (s *Server) tailnetHostAllowed(r *http.Request) bool {
	if s == nil || s.tailnet == nil || r == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if h, _, errSplit := net.SplitHostPort(host); errSplit == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return false
	}
	if _, errAddr := netip.ParseAddr(host); errAddr == nil {
		return true
	}
	st, errStatus := s.tailnet.Status(r.Context())
	if errStatus != nil {
		return false
	}
	name := strings.ToLower(strings.TrimSuffix(st.DNSName, "."))
	if name == "" {
		return false
	}
	short, _, _ := strings.Cut(name, ".")
	return host == name || host == short
}

// managementAuthMiddleware admits identity-authorized tailnet callers and sends
// everyone else through the management key middleware unchanged.
func (s *Server) managementAuthMiddleware() gin.HandlerFunc {
	keyAuth := s.mgmt.Middleware()
	return func(c *gin.Context) {
		if s.tailnetManagementAuthorized(c) {
			c.Header("X-CPA-VERSION", buildinfo.Version)
			c.Header("X-CPA-COMMIT", buildinfo.Commit)
			c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)
			c.Next()
			return
		}
		keyAuth(c)
	}
}

// getTailscaleStatus serves GET /v8/management/tailscale/status.
func (s *Server) getTailscaleStatus(c *gin.Context) {
	cfg := s.getConfig()
	wanted := tailnet.ResolveSettings(cfg, s.configFilePath)

	caller := gin.H{"via_tailnet": tailnet.ViaTailnet(c.Request.Context())}
	if peer, ok := s.tailnetPeer(c); ok {
		setIfNotEmpty(caller, "login_name", peer.LoginName)
		setIfNotEmpty(caller, "node_name", peer.NodeName)
		caller["tagged"] = peer.Tagged
		caller["management_identity"] = s.tailnetManagementAuthorized(c)
	}
	resp := gin.H{
		"enabled": wanted.Enabled,
		"active":  s.tailnet != nil,
		"caller":  caller,
	}
	if s.tailnet == nil {
		resp["state"] = "Disabled"
		resp["restart_required"] = wanted.Enabled
		resp["hostname"] = wanted.Hostname
		c.JSON(http.StatusOK, resp)
		return
	}

	active := s.tailnet.Settings()
	resp["restart_required"] = restartRequired(active, wanted)
	st, errStatus := s.tailnet.Status(c.Request.Context())
	if errStatus != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "tailscale_status_unavailable", "message": errStatus.Error()})
		return
	}
	resp["state"] = st.State
	resp["hostname"] = st.Hostname
	setIfNotEmpty(resp, "dns_name", st.DNSName)
	setIfNotEmpty(resp, "tailnet", st.Tailnet)
	setIfNotEmpty(resp, "magic_dns_suffix", st.MagicDNSSuffix)
	resp["magic_dns_enabled"] = st.MagicDNSEnabled
	resp["ips"] = nonNilStrings(st.IPs)
	resp["https_enabled"] = active.HTTPS
	resp["cert_domains"] = nonNilStrings(st.CertDomains)
	if active.HTTPPort > 0 {
		resp["http_port"] = active.HTTPPort
	}
	if st.State == "NeedsLogin" {
		setIfNotEmpty(resp, "auth_url", st.AuthURL)
	}
	setIfNotEmpty(resp, "self_user", st.SelfUser)
	if len(st.Tags) > 0 {
		resp["tags"] = st.Tags
	}
	urls := []string{}
	if st.DNSName != "" {
		if active.HTTPS {
			urls = append(urls, "https://"+st.DNSName)
		}
		if active.HTTPPort > 0 {
			urls = append(urls, fmt.Sprintf("http://%s:%d", st.DNSName, active.HTTPPort))
		}
	}
	resp["urls"] = urls
	c.JSON(http.StatusOK, resp)
}

// restartRequired compares settings without exposing the auth key.
func restartRequired(active, wanted tailnet.Settings) bool {
	return active != wanted
}

func setIfNotEmpty(m gin.H, key, value string) {
	if value != "" {
		m[key] = value
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
