package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	defaultClaudeFingerprintUserAgent      = "claude-cli/2.1.280 (external, cli)"
	defaultClaudeFingerprintPackageVersion = "0.112.1"
	defaultClaudeFingerprintRuntimeVersion = "v26.3.0"
	defaultClaudeFingerprintOS             = "MacOS"
	defaultClaudeFingerprintArch           = "arm64"
	claudeDeviceProfileTTL                 = 7 * 24 * time.Hour
	claudeDeviceProfileCleanupPeriod       = time.Hour
)

var (
	claudeCLIVersionPattern     = regexp.MustCompile(`^claude-cli/(\d+)\.(\d+)\.(\d+)`)
	claudePackageVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	claudeRuntimeVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

	claudeDeviceProfileCache            = make(map[string]claudeDeviceProfileCacheEntry)
	claudeDeviceProfileCacheMu          sync.RWMutex
	claudeDeviceProfileCacheCleanupOnce sync.Once

	ClaudeDeviceProfileBeforeCandidateStore func(ClaudeDeviceProfile)
)

type claudeCLIVersion struct {
	major int
	minor int
	patch int
}

func (v claudeCLIVersion) Compare(other claudeCLIVersion) int {
	switch {
	case v.major != other.major:
		if v.major > other.major {
			return 1
		}
		return -1
	case v.minor != other.minor:
		if v.minor > other.minor {
			return 1
		}
		return -1
	case v.patch != other.patch:
		if v.patch > other.patch {
			return 1
		}
		return -1
	default:
		return 0
	}
}

type ClaudeDeviceProfile struct {
	UserAgent      string
	PackageVersion string
	RuntimeVersion string
	OS             string
	Arch           string
	version        claudeCLIVersion
	hasVersion     bool
}

type claudeDeviceProfileCacheEntry struct {
	profile ClaudeDeviceProfile
	expire  time.Time
}

func ClaudeDeviceProfileStabilizationEnabled(cfg *config.Config) bool {
	if cfg == nil || cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile == nil {
		return false
	}
	return *cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile
}

func ResetClaudeDeviceProfileCache() {
	claudeDeviceProfileCacheMu.Lock()
	claudeDeviceProfileCache = make(map[string]claudeDeviceProfileCacheEntry)
	claudeDeviceProfileCacheMu.Unlock()
}

func defaultClaudeDeviceProfile(cfg *config.Config) ClaudeDeviceProfile {
	hdrDefault := func(cfgVal, fallback string) string {
		if strings.TrimSpace(cfgVal) != "" {
			return strings.TrimSpace(cfgVal)
		}
		return fallback
	}

	var hd config.ClaudeHeaderDefaults
	if cfg != nil {
		hd = cfg.ClaudeHeaderDefaults
	}

	profile := ClaudeDeviceProfile{
		UserAgent:      hdrDefault(hd.UserAgent, defaultClaudeFingerprintUserAgent),
		PackageVersion: hdrDefault(hd.PackageVersion, defaultClaudeFingerprintPackageVersion),
		RuntimeVersion: hdrDefault(hd.RuntimeVersion, defaultClaudeFingerprintRuntimeVersion),
		OS:             hdrDefault(hd.OS, defaultClaudeFingerprintOS),
		Arch:           hdrDefault(hd.Arch, defaultClaudeFingerprintArch),
	}
	if version, ok := parseClaudeCLIVersion(profile.UserAgent); ok {
		profile.version = version
		profile.hasVersion = true
	}
	return profile
}

// mapStainlessOS maps runtime.GOOS to Stainless SDK OS names.
func mapStainlessOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	default:
		return "Other::" + runtime.GOOS
	}
}

// mapStainlessArch maps runtime.GOARCH to Stainless SDK architecture names.
func mapStainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return "other::" + runtime.GOARCH
	}
}

func parseClaudeCLIVersion(userAgent string) (claudeCLIVersion, bool) {
	matches := claudeCLIVersionPattern.FindStringSubmatch(strings.TrimSpace(userAgent))
	if len(matches) != 4 {
		return claudeCLIVersion{}, false
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return claudeCLIVersion{}, false
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return claudeCLIVersion{}, false
	}
	patch, err := strconv.Atoi(matches[3])
	if err != nil {
		return claudeCLIVersion{}, false
	}
	return claudeCLIVersion{major: major, minor: minor, patch: patch}, true
}

func shouldUpgradeClaudeDeviceProfile(candidate, current ClaudeDeviceProfile) bool {
	if candidate.UserAgent == "" || !candidate.hasVersion {
		return false
	}
	if current.UserAgent == "" || !current.hasVersion {
		return true
	}
	return candidate.version.Compare(current.version) > 0
}

// plausibleClaudeCLIVersion treats the baseline as a floor for patch releases.
// Claude Code auto-updates in the background; allow newer patch versions in the
// same major/minor line to preserve native passthrough and prompt caching.
func plausibleClaudeCLIVersion(candidate, baseline claudeCLIVersion) bool {
	return candidate.major == baseline.major &&
		candidate.minor == baseline.minor &&
		candidate.patch >= baseline.patch
}

func meetsClaudeDeviceProfileBaseline(candidate, baseline ClaudeDeviceProfile) bool {
	if candidate.UserAgent == "" || !candidate.hasVersion {
		return false
	}
	if baseline.UserAgent == "" || !baseline.hasVersion {
		return false
	}
	return candidate.version.Compare(baseline.version) == 0 &&
		candidate.PackageVersion == baseline.PackageVersion &&
		candidate.RuntimeVersion == baseline.RuntimeVersion
}

func pinClaudeDeviceProfilePlatform(profile, baseline ClaudeDeviceProfile) ClaudeDeviceProfile {
	profile.OS = baseline.OS
	profile.Arch = baseline.Arch
	return profile
}

// normalizeClaudeDeviceProfile pins stabilized profiles to the configured platform
// and replaces any software tuple that does not exactly match the measured baseline.
func normalizeClaudeDeviceProfile(profile, baseline ClaudeDeviceProfile) ClaudeDeviceProfile {
	profile = pinClaudeDeviceProfilePlatform(profile, baseline)
	if !meetsClaudeDeviceProfileBaseline(profile, baseline) {
		profile.UserAgent = baseline.UserAgent
		profile.PackageVersion = baseline.PackageVersion
		profile.RuntimeVersion = baseline.RuntimeVersion
		profile.version = baseline.version
		profile.hasVersion = baseline.hasVersion
	}
	return profile
}

func extractClaudeDeviceProfile(headers http.Header, cfg *config.Config) (ClaudeDeviceProfile, bool) {
	if headers == nil {
		return ClaudeDeviceProfile{}, false
	}

	userAgent := strings.TrimSpace(headers.Get("User-Agent"))
	version, ok := parseClaudeCLIVersion(userAgent)
	if !ok || !claudeCodeNativeUserAgentPattern.MatchString(userAgent) {
		return ClaudeDeviceProfile{}, false
	}

	baseline := defaultClaudeDeviceProfile(cfg)
	packageVersion := firstNonEmptyHeader(headers, "X-Stainless-Package-Version", baseline.PackageVersion)
	if !claudePackageVersionPattern.MatchString(packageVersion) {
		packageVersion = baseline.PackageVersion
	}
	runtimeVersion := firstNonEmptyHeader(headers, "X-Stainless-Runtime-Version", baseline.RuntimeVersion)
	if !claudeRuntimeVersionPattern.MatchString(runtimeVersion) {
		runtimeVersion = baseline.RuntimeVersion
	}
	profile := ClaudeDeviceProfile{
		UserAgent:      userAgent,
		PackageVersion: packageVersion,
		RuntimeVersion: runtimeVersion,
		OS:             firstNonEmptyHeader(headers, "X-Stainless-Os", baseline.OS),
		Arch:           firstNonEmptyHeader(headers, "X-Stainless-Arch", baseline.Arch),
		version:        version,
		hasVersion:     true,
	}
	return profile, true
}

func firstNonEmptyHeader(headers http.Header, name, fallback string) string {
	if headers == nil {
		return fallback
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	return fallback
}

func claudeDeviceProfileScopeKey(auth *cliproxyauth.Auth, apiKey string) string {
	switch {
	case auth != nil && strings.TrimSpace(auth.ID) != "":
		return "auth:" + strings.TrimSpace(auth.ID)
	case strings.TrimSpace(apiKey) != "":
		return "api_key:" + strings.TrimSpace(apiKey)
	default:
		return "global"
	}
}

// claudeDeviceProfileSubclientScope keeps first-party clients with distinct
// wire identities from replacing one another in a credential's stabilized
// profile. The CLI retains the legacy base scope for cache compatibility.
func claudeDeviceProfileSubclientScope(profile ClaudeDeviceProfile) string {
	entrypoint, _ := parseClaudeCodeUserAgentDetails(profile.UserAgent)
	if entrypoint == "" || entrypoint == "cli" {
		return ""
	}
	if nativeClaudeEntrypoints[entrypoint] {
		return entrypoint
	}
	return "other"
}

func claudeDeviceProfileScopedKey(auth *cliproxyauth.Auth, apiKey string, profile ClaudeDeviceProfile) string {
	key := claudeDeviceProfileScopeKey(auth, apiKey)
	if subclient := claudeDeviceProfileSubclientScope(profile); subclient != "" {
		key += "|subclient:" + subclient
	}
	return key
}

func claudeDeviceProfileCacheKey(auth *cliproxyauth.Auth, apiKey string, profile ClaudeDeviceProfile) string {
	sum := sha256.Sum256([]byte(claudeDeviceProfileScopedKey(auth, apiKey, profile)))
	return hex.EncodeToString(sum[:])
}

func startClaudeDeviceProfileCacheCleanup() {
	go func() {
		ticker := time.NewTicker(claudeDeviceProfileCleanupPeriod)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredClaudeDeviceProfiles()
		}
	}()
}

func purgeExpiredClaudeDeviceProfiles() {
	now := time.Now()
	claudeDeviceProfileCacheMu.Lock()
	for key, entry := range claudeDeviceProfileCache {
		if !entry.expire.After(now) {
			delete(claudeDeviceProfileCache, key)
		}
	}
	claudeDeviceProfileCacheMu.Unlock()
}

func ResolveClaudeDeviceProfile(auth *cliproxyauth.Auth, apiKey string, headers http.Header, cfg *config.Config) ClaudeDeviceProfile {
	profile, errProfile := ResolveClaudeDeviceProfileRequired(context.Background(), auth, apiKey, headers, cfg)
	if errProfile != nil {
		return defaultClaudeDeviceProfile(cfg)
	}
	return profile
}

// ResolveClaudeDeviceProfileRequired resolves a stable Claude Code device profile for request-time paths.
func ResolveClaudeDeviceProfileRequired(_ context.Context, auth *cliproxyauth.Auth, apiKey string, headers http.Header, cfg *config.Config) (ClaudeDeviceProfile, error) {
	return resolveClaudeDeviceProfileLocal(auth, apiKey, headers, cfg), nil
}

func resolveClaudeDeviceProfileLocal(auth *cliproxyauth.Auth, apiKey string, headers http.Header, cfg *config.Config) ClaudeDeviceProfile {
	claudeDeviceProfileCacheCleanupOnce.Do(startClaudeDeviceProfileCacheCleanup)

	now := time.Now()
	baseline := defaultClaudeDeviceProfile(cfg)
	candidate, hasCandidate := extractClaudeDeviceProfile(headers, cfg)
	if hasCandidate {
		candidate = pinClaudeDeviceProfilePlatform(candidate, baseline)
	}
	if hasCandidate && !meetsClaudeDeviceProfileBaseline(candidate, baseline) {
		hasCandidate = false
	}
	cacheProfile := ClaudeDeviceProfile{}
	if hasCandidate {
		cacheProfile = candidate
	}
	cacheKey := claudeDeviceProfileCacheKey(auth, apiKey, cacheProfile)

	claudeDeviceProfileCacheMu.RLock()
	entry, hasCached := claudeDeviceProfileCache[cacheKey]
	cachedValid := hasCached && entry.expire.After(now) && entry.profile.UserAgent != ""
	claudeDeviceProfileCacheMu.RUnlock()

	if hasCandidate {
		if ClaudeDeviceProfileBeforeCandidateStore != nil {
			ClaudeDeviceProfileBeforeCandidateStore(candidate)
		}

		claudeDeviceProfileCacheMu.Lock()
		entry, hasCached = claudeDeviceProfileCache[cacheKey]
		cachedValid = hasCached && entry.expire.After(now) && entry.profile.UserAgent != ""
		if cachedValid {
			entry.profile = normalizeClaudeDeviceProfile(entry.profile, baseline)
		}
		if cachedValid && !shouldUpgradeClaudeDeviceProfile(candidate, entry.profile) {
			entry.expire = now.Add(claudeDeviceProfileTTL)
			claudeDeviceProfileCache[cacheKey] = entry
			claudeDeviceProfileCacheMu.Unlock()
			return entry.profile
		}

		claudeDeviceProfileCache[cacheKey] = claudeDeviceProfileCacheEntry{
			profile: candidate,
			expire:  now.Add(claudeDeviceProfileTTL),
		}
		claudeDeviceProfileCacheMu.Unlock()
		return candidate
	}

	if cachedValid {
		claudeDeviceProfileCacheMu.Lock()
		entry = claudeDeviceProfileCache[cacheKey]
		if entry.expire.After(now) && entry.profile.UserAgent != "" {
			entry.profile = normalizeClaudeDeviceProfile(entry.profile, baseline)
			entry.expire = now.Add(claudeDeviceProfileTTL)
			claudeDeviceProfileCache[cacheKey] = entry
			claudeDeviceProfileCacheMu.Unlock()
			return entry.profile
		}
		claudeDeviceProfileCacheMu.Unlock()
	}

	return baseline
}

func ApplyClaudeDeviceProfileHeaders(r *http.Request, profile ClaudeDeviceProfile) {
	if r == nil {
		return
	}
	for _, headerName := range []string{
		"User-Agent",
		"X-Stainless-Package-Version",
		"X-Stainless-Runtime-Version",
		"X-Stainless-Os",
		"X-Stainless-Arch",
	} {
		r.Header.Del(headerName)
	}
	r.Header.Set("User-Agent", profile.UserAgent)
	r.Header.Set("X-Stainless-Package-Version", profile.PackageVersion)
	r.Header.Set("X-Stainless-Runtime-Version", profile.RuntimeVersion)
	r.Header.Set("X-Stainless-Os", profile.OS)
	r.Header.Set("X-Stainless-Arch", profile.Arch)
}

// DefaultClaudeVersion returns the version string (e.g. "2.1.280") from the
// current baseline device profile. It extracts the version from the User-Agent.
func DefaultClaudeVersion(cfg *config.Config) string {
	profile := defaultClaudeDeviceProfile(cfg)
	if version, ok := parseClaudeCLIVersion(profile.UserAgent); ok {
		return strconv.Itoa(version.major) + "." + strconv.Itoa(version.minor) + "." + strconv.Itoa(version.patch)
	}
	return "2.1.280"
}

func ApplyClaudeDefaultDeviceProfileHeaders(r *http.Request, cfg *config.Config) {
	ApplyClaudeDeviceProfileHeaders(r, defaultClaudeDeviceProfile(cfg))
}

func ApplyClaudeLegacyDeviceHeaders(r *http.Request, ginHeaders http.Header, cfg *config.Config, confirmedClaudeCode bool) {
	if r == nil {
		return
	}
	profile := defaultClaudeDeviceProfile(cfg)
	miscEnsure := func(name, fallback string, valid func(string) bool) {
		if current := strings.TrimSpace(r.Header.Get(name)); current != "" && (valid == nil || valid(current)) {
			return
		}
		if incoming := strings.TrimSpace(ginHeaders.Get(name)); incoming != "" && (valid == nil || valid(incoming)) {
			r.Header.Set(name, incoming)
			return
		}
		r.Header.Set(name, fallback)
	}

	if confirmedClaudeCode {
		miscEnsure("X-Stainless-Runtime-Version", profile.RuntimeVersion, func(value string) bool { return value == profile.RuntimeVersion })
		miscEnsure("X-Stainless-Package-Version", profile.PackageVersion, func(value string) bool { return value == profile.PackageVersion })
		miscEnsure("X-Stainless-Os", mapStainlessOS(), nil)
		miscEnsure("X-Stainless-Arch", mapStainlessArch(), nil)
		if clientUA := strings.TrimSpace(ginHeaders.Get("User-Agent")); plausibleClaudeCodeUserAgent(clientUA, cfg) {
			r.Header.Set("User-Agent", clientUA)
			return
		}
	}

	// Unconfirmed clients must not leak a copied or third-party software profile
	// into the upstream Claude Code SDK fingerprint.
	r.Header.Set("X-Stainless-Runtime-Version", profile.RuntimeVersion)
	r.Header.Set("X-Stainless-Package-Version", profile.PackageVersion)
	r.Header.Set("X-Stainless-Os", profile.OS)
	r.Header.Set("X-Stainless-Arch", profile.Arch)
	r.Header.Set("User-Agent", profile.UserAgent)
}
