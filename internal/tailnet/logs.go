package tailnet

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

var (
	authKeyPattern = regexp.MustCompile(`tskey-[A-Za-z0-9_-]+`)
	authURLPattern = regexp.MustCompile(`(https?://[^\s/]+/a/)[A-Za-z0-9]+`)
	loginURLLine   = regexp.MustCompile(`go to: (https?://\S+)`)
)

// redact removes auth keys and one-time login URL tokens from a log line.
func redact(msg string) string {
	msg = authKeyPattern.ReplaceAllString(msg, "tskey-REDACTED")
	return authURLPattern.ReplaceAllString(msg, "${1}REDACTED")
}

// logSink routes tsnet logs through logrus. Backend logs go to debug, user
// logs to info, and the interactive login URL to warn once per distinct URL.
type logSink struct {
	mu           sync.Mutex
	lastLoginURL string
	// onLoginURL is called with each newly announced login URL (tests).
	onLoginURL func(string)
}

// backendLogf is tsnet.Server.Logf: verbose engine/backend logs.
func (l *logSink) backendLogf(format string, args ...any) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	log.Debug("tailscale: " + redact(strings.TrimSpace(fmt.Sprintf(format, args...))))
}

// userLogf is tsnet.Server.UserLogf: status messages meant for the operator.
func (l *logSink) userLogf(format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if match := loginURLLine.FindStringSubmatch(msg); match != nil {
		// tsnet repeats this every few seconds while login is pending.
		l.announceLoginURL(match[1])
		return
	}
	log.Info("tailscale: " + redact(msg))
}

func (l *logSink) announceLoginURL(url string) {
	l.mu.Lock()
	repeated := url == l.lastLoginURL
	l.lastLoginURL = url
	hook := l.onLoginURL
	l.mu.Unlock()
	if repeated {
		return
	}
	log.Warnf("tailscale: login required; open %s to add this node to your tailnet (or set tailscale.auth-key / %s and restart)", url, AuthKeyEnv)
	if hook != nil {
		hook(url)
	}
}
