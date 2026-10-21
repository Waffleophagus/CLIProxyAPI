// Package tailnet embeds a Tailscale node (tailscale.com/tsnet) so the proxy
// joins a tailnet as its own machine. It serves the proxy's HTTP handler on
// tailnet listeners and exposes WhoIs identity lookups and node status for the
// management API. The node uses userspace networking, so no tailscaled, TUN
// device or extra capabilities are required.
package tailnet

import (
	"context"
	"errors"
)

// Peer is the tailnet identity behind a remote address.
type Peer struct {
	// LoginName is the owner's login name (e.g. "user@github"). Tagged nodes
	// have no user identity, so it is empty for them.
	LoginName string
	// DisplayName is the owner's display name, when known.
	DisplayName string
	// NodeName is the peer's MagicDNS name without the trailing dot.
	NodeName string
	// Tagged reports whether the peer node is owned by tags rather than a user.
	Tagged bool
}

// Status is a snapshot of the local node's state.
type Status struct {
	// State is the backend state: NoState, NeedsLogin, NeedsMachineAuth,
	// Stopped, Starting or Running.
	State string
	// Hostname is the requested tailnet machine name.
	Hostname string
	// DNSName is the node's MagicDNS name without the trailing dot.
	DNSName string
	// Tailnet is the tailnet name.
	Tailnet string
	// MagicDNSSuffix is the tailnet's MagicDNS suffix (e.g. "tail1234.ts.net").
	MagicDNSSuffix string
	// MagicDNSEnabled reports whether MagicDNS is enabled for the tailnet.
	MagicDNSEnabled bool
	// IPs lists the node's tailnet addresses.
	IPs []string
	// CertDomains lists the names the node can get TLS certificates for. It is
	// empty when HTTPS is disabled in the tailnet admin console.
	CertDomains []string
	// AuthURL is the interactive login URL while the node needs login.
	AuthURL string
	// SelfUser is the login name of the node owner; empty for tagged nodes.
	SelfUser string
	// Tags lists the node's ACL tags.
	Tags []string
}

// ErrNotRunning is returned by lookups before the node has started.
var ErrNotRunning = errors.New("tailscale node is not running")

type viaTailnetKey struct{}

// WithViaTailnet marks ctx as belonging to a connection accepted on a tailnet
// listener. Only the tailnet listener's http.Server sets it.
func WithViaTailnet(ctx context.Context) context.Context {
	return context.WithValue(ctx, viaTailnetKey{}, true)
}

// ViaTailnet reports whether ctx belongs to a connection accepted on a
// tailnet listener.
func ViaTailnet(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	via, _ := ctx.Value(viaTailnetKey{}).(bool)
	return via
}
