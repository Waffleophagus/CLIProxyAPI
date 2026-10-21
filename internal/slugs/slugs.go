// Package slugs resolves follow-mode OpenAI-compatible model aliases ("slugs").
//
// A follow entry names a stable client alias (e.g. DGX-SPARK) and lets the proxy
// discover the upstream model id from GET {base-url}/models. The Manager polls
// each provider group with follow entries, keeps the last successfully resolved
// id per alias in memory, and produces an effective runtime config in which
// follow entries carry the resolved id as their upstream name.
package slugs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	// ModeFollow marks an entry whose upstream model is resolved by polling.
	ModeFollow = "follow"
	// ModePinned marks an entry with a fixed upstream model name.
	ModePinned = "pinned"

	// pollTimeout bounds one control-plane /models probe so a hung endpoint
	// cannot wedge the poller. It never applies to inference traffic.
	pollTimeout = 10 * time.Second
)

// ErrGroupNotFound is returned by Refresh for an unknown provider group.
var ErrGroupNotFound = errors.New("openai-compatibility group not found")

// Target describes how to reach one provider's /models endpoint.
type Target struct {
	BaseURL        string
	APIKey         string
	ProxyURL       string
	GlobalProxyURL string
	Headers        map[string]string
}

// Fetcher lists the model ids an endpoint reports, in endpoint order.
type Fetcher func(ctx context.Context, target Target) ([]string, error)

// Ticker abstracts time.Ticker so tests can drive polling deterministically.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// TickerFactory creates a Ticker for the given interval.
type TickerFactory func(time.Duration) Ticker

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

func newRealTicker(d time.Duration) Ticker { return realTicker{t: time.NewTicker(d)} }

// Options configures a Manager. Zero values use production defaults.
type Options struct {
	// Fetch lists upstream models. Defaults to an HTTP GET {base-url}/models.
	Fetch Fetcher
	// NewTicker creates poll tickers. Defaults to time.NewTicker.
	NewTicker TickerFactory
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// OnChange is called after a resolved upstream id changes. It runs on a
	// dedicated goroutine, never while the Manager holds its lock.
	OnChange func(ctx context.Context)
}

// Status is the management view of one alias entry.
type Status struct {
	Group          string     `json:"group"`
	Alias          string     `json:"alias"`
	BaseURL        string     `json:"base_url"`
	Mode           string     `json:"mode"`
	Match          string     `json:"match,omitempty"`
	ResolvedModel  string     `json:"resolved_model"`
	PollInterval   string     `json:"poll_interval,omitempty"`
	LastCheckAt    *time.Time `json:"last_check_at,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	ReportedModels []string   `json:"reported_models"`
}

type entrySpec struct {
	alias  string
	match  string
	pinned string
	follow bool
}

func (e entrySpec) following() bool { return e.follow && e.pinned == "" }

type groupSpec struct {
	key      string
	name     string
	target   Target
	interval time.Duration
	entries  []entrySpec
}

func (g groupSpec) polls() bool {
	for _, entry := range g.entries {
		if entry.following() {
			return true
		}
	}
	return false
}

func (g groupSpec) equal(other groupSpec) bool {
	if g.key != other.key || g.name != other.name || g.interval != other.interval || len(g.entries) != len(other.entries) {
		return false
	}
	if g.target.BaseURL != other.target.BaseURL || g.target.APIKey != other.target.APIKey ||
		g.target.ProxyURL != other.target.ProxyURL || g.target.GlobalProxyURL != other.target.GlobalProxyURL ||
		len(g.target.Headers) != len(other.target.Headers) {
		return false
	}
	for k, v := range g.target.Headers {
		if other.target.Headers[k] != v {
			return false
		}
	}
	for i := range g.entries {
		if g.entries[i] != other.entries[i] {
			return false
		}
	}
	return true
}

type group struct {
	spec groupSpec

	cancel  context.CancelFunc
	done    chan struct{}
	trigger chan chan struct{}

	resolved    map[string]string
	models      []string
	lastCheck   time.Time
	lastSuccess time.Time
	lastError   string
	warned      map[string]bool
}

// Manager owns the follow-mode pollers and their resolved state.
type Manager struct {
	fetch     Fetcher
	newTicker TickerFactory
	now       func() time.Time
	onChange  func(ctx context.Context)

	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	started    bool
	stopped    bool
	groups     map[string]*group
	order      []string
	changed    chan struct{}
	changeSeq  uint64
	appliedSeq uint64
	appliedCh  chan struct{}
	wg         sync.WaitGroup
}

// NewManager creates an idle Manager. Call Reconfigure and Start to poll.
func NewManager(opts Options) *Manager {
	m := &Manager{
		fetch:     opts.Fetch,
		newTicker: opts.NewTicker,
		now:       opts.Now,
		onChange:  opts.OnChange,
		groups:    make(map[string]*group),
		changed:   make(chan struct{}, 1),
		appliedCh: make(chan struct{}),
	}
	if m.fetch == nil {
		m.fetch = FetchModels
	}
	if m.newTicker == nil {
		m.newTicker = newRealTicker
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m
}

// SetOnChange replaces the change callback. Call it before Start.
func (m *Manager) SetOnChange(fn func(ctx context.Context)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func groupKey(compat *config.OpenAICompatibility) string {
	return strings.ToLower(strings.TrimSpace(compat.Name)) + "|" + strings.TrimSpace(compat.BaseURL)
}

func normalizeAlias(alias string) string {
	return strings.ToLower(strings.TrimSpace(alias))
}

func buildSpecs(cfg *config.Config) ([]groupSpec, map[string]groupSpec) {
	if cfg == nil {
		return nil, nil
	}
	ordered := make([]groupSpec, 0, len(cfg.OpenAICompatibility))
	byKey := make(map[string]groupSpec, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled || strings.TrimSpace(compat.BaseURL) == "" {
			continue
		}
		key := groupKey(compat)
		if _, exists := byKey[key]; exists {
			continue
		}
		spec := groupSpec{
			key:      key,
			name:     strings.TrimSpace(compat.Name),
			interval: compat.FollowPollInterval(),
			target: Target{
				BaseURL:        strings.TrimSpace(compat.BaseURL),
				GlobalProxyURL: strings.TrimSpace(cfg.ProxyURL),
			},
		}
		if len(compat.APIKeyEntries) > 0 {
			spec.target.APIKey = strings.TrimSpace(compat.APIKeyEntries[0].APIKey)
			spec.target.ProxyURL = strings.TrimSpace(compat.APIKeyEntries[0].ProxyURL)
		}
		if len(compat.Headers) > 0 {
			spec.target.Headers = make(map[string]string, len(compat.Headers))
			for k, v := range compat.Headers {
				spec.target.Headers[k] = v
			}
		}
		for j := range compat.Models {
			model := &compat.Models[j]
			alias := strings.TrimSpace(model.Alias)
			if alias == "" {
				continue
			}
			spec.entries = append(spec.entries, entrySpec{
				alias:  alias,
				match:  strings.TrimSpace(model.Match),
				pinned: strings.TrimSpace(model.Name),
				follow: model.Follow,
			})
		}
		if len(spec.entries) == 0 {
			continue
		}
		ordered = append(ordered, spec)
		byKey[key] = spec
	}
	return ordered, byKey
}

// Reconfigure applies a new config: pollers for removed or changed groups stop
// (and are waited for), new or changed groups start when the Manager runs.
// Resolved ids survive a reconfigure as long as the group name and base-url stay.
func (m *Manager) Reconfigure(cfg *config.Config) {
	if m == nil {
		return
	}
	ordered, byKey := buildSpecs(cfg)
	var stopping []chan struct{}
	m.mu.Lock()
	for key, existing := range m.groups {
		next, keep := byKey[key]
		if keep && existing.spec.equal(next) {
			continue
		}
		if existing.cancel != nil {
			existing.cancel()
			stopping = append(stopping, existing.done)
			existing.cancel = nil
		}
		if !keep {
			delete(m.groups, key)
		}
	}
	m.order = m.order[:0]
	for _, spec := range ordered {
		m.order = append(m.order, spec.key)
		existing := m.groups[spec.key]
		if existing != nil && existing.spec.equal(spec) {
			continue
		}
		if existing == nil {
			existing = &group{resolved: make(map[string]string), warned: make(map[string]bool)}
			m.groups[spec.key] = existing
		}
		existing.spec = spec
		m.pruneResolvedLocked(existing)
		if !spec.polls() {
			existing.models = nil
			existing.lastCheck, existing.lastSuccess, existing.lastError = time.Time{}, time.Time{}, ""
		}
		if len(existing.models) > 0 {
			m.applySelectionLocked(existing, existing.models)
		}
		if m.started && !m.stopped {
			m.startGroupLocked(existing)
		}
	}
	m.mu.Unlock()
	for _, done := range stopping {
		<-done
	}
}

func (m *Manager) pruneResolvedLocked(g *group) {
	wanted := make(map[string]bool, len(g.spec.entries))
	for _, entry := range g.spec.entries {
		if entry.following() {
			wanted[normalizeAlias(entry.alias)] = true
		}
	}
	for alias := range g.resolved {
		if !wanted[alias] {
			delete(g.resolved, alias)
		}
	}
}

// Start launches pollers for all configured follow groups. Each poller resolves
// immediately, then every poll interval.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.stopped {
		return
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.started = true
	m.wg.Add(1)
	go m.notifyLoop(m.ctx)
	for _, key := range m.order {
		if g := m.groups[key]; g != nil {
			m.startGroupLocked(g)
		}
	}
}

// Stop cancels all pollers and waits for them to exit. It is idempotent.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	if m.cancel != nil {
		m.cancel()
	}
	for _, g := range m.groups {
		g.cancel = nil
	}
	close(m.appliedCh)
	m.appliedCh = make(chan struct{})
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) startGroupLocked(g *group) {
	if g.cancel != nil || !g.spec.polls() {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	g.cancel = cancel
	g.done = make(chan struct{})
	g.trigger = make(chan chan struct{})
	m.wg.Add(1)
	go m.run(ctx, g, g.spec, g.done, g.trigger)
}

func (m *Manager) run(ctx context.Context, g *group, spec groupSpec, done chan struct{}, trigger chan chan struct{}) {
	defer m.wg.Done()
	defer close(done)
	m.poll(ctx, g, spec)
	ticker := m.newTicker(spec.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			m.poll(ctx, g, spec)
		case ack := <-trigger:
			m.poll(ctx, g, spec)
			close(ack)
		}
	}
}

func (m *Manager) poll(ctx context.Context, g *group, spec groupSpec) {
	if ctx.Err() != nil {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, pollTimeout)
	ids, errFetch := m.fetch(probeCtx, spec.target)
	cancel()
	if ctx.Err() != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || m.groups[spec.key] != g {
		// The group was reconfigured or removed while the probe ran; Reconfigure
		// cancels under m.mu, so this check cannot race with a restart.
		return
	}
	g.lastCheck = m.now()
	previousError := g.lastError
	switch {
	case errFetch != nil:
		g.lastError = errFetch.Error()
	case len(ids) == 0:
		g.lastError = "endpoint reported no models"
	default:
		g.models = ids
		g.lastSuccess = g.lastCheck
		g.lastError = ""
		if m.applySelectionLocked(g, ids) {
			m.changeSeq++
			select {
			case m.changed <- struct{}{}:
			default:
			}
		}
	}
	if g.lastError != "" && g.lastError != previousError {
		log.Warnf("slug group %s: %s/models check failed, keeping last resolved models: %s", spec.name, spec.target.BaseURL, g.lastError)
	} else if g.lastError == "" && previousError != "" {
		log.Infof("slug group %s: %s/models reachable again", spec.name, spec.target.BaseURL)
	}
}

// applySelectionLocked selects the upstream id for every following entry and
// reports whether any resolved id changed.
func (m *Manager) applySelectionLocked(g *group, ids []string) bool {
	changed := false
	var selectionErrors []string
	for _, entry := range g.spec.entries {
		if !entry.following() {
			continue
		}
		key := normalizeAlias(entry.alias)
		id, ok := Select(ids, entry.match)
		if !ok {
			selectionErrors = append(selectionErrors, fmt.Sprintf("no reported model matches %q for %s", entry.match, entry.alias))
			continue
		}
		if entry.match == "" && len(ids) > 1 && !g.warned[key] {
			g.warned[key] = true
			log.Warnf("slug %s: %s/models lists %d models and no match is set; using the first (%s)", entry.alias, g.spec.target.BaseURL, len(ids), id)
		}
		previous := g.resolved[key]
		if previous == id {
			continue
		}
		g.resolved[key] = id
		changed = true
		was := previous
		if was == "" {
			was = "unresolved"
		}
		log.Infof("slug %s now -> %s (was %s)", entry.alias, id, was)
	}
	if len(selectionErrors) > 0 {
		g.lastError = strings.Join(selectionErrors, "; ")
	}
	return changed
}

// Select picks the followed model: the first id matching the glob when match is
// set, otherwise the first id. It reports false when nothing qualifies.
func Select(ids []string, match string) (string, bool) {
	match = strings.TrimSpace(match)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if match == "" || matchGlob(match, id) {
			return id, true
		}
	}
	return "", false
}

// matchGlob performs case-insensitive matching where '*' matches any substring.
func matchGlob(pattern, value string) bool {
	pattern = strings.ToLower(pattern)
	value = strings.ToLower(value)
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(value, part)
		if index < 0 {
			return false
		}
		value = value[index+len(part):]
	}
	return strings.HasSuffix(value, last)
}

func (m *Manager) notifyLoop(ctx context.Context) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.changed:
		}
		m.mu.Lock()
		seq := m.changeSeq
		onChange := m.onChange
		m.mu.Unlock()
		if onChange != nil {
			onChange(ctx)
		}
		m.mu.Lock()
		if seq > m.appliedSeq {
			m.appliedSeq = seq
		}
		close(m.appliedCh)
		m.appliedCh = make(chan struct{})
		m.mu.Unlock()
	}
}

// Refresh polls all follow groups (or the named group) immediately and returns
// after the result, including any routing update, has been applied.
func (m *Manager) Refresh(ctx context.Context, groupName string) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	groupName = strings.TrimSpace(groupName)
	type runner struct {
		trigger chan chan struct{}
		done    chan struct{}
	}
	var runners []runner
	found := groupName == ""
	m.mu.Lock()
	for _, key := range m.order {
		g := m.groups[key]
		if g == nil {
			continue
		}
		if groupName != "" && !strings.EqualFold(g.spec.name, groupName) {
			continue
		}
		found = true
		if g.cancel != nil {
			runners = append(runners, runner{trigger: g.trigger, done: g.done})
		}
	}
	m.mu.Unlock()
	if !found {
		return ErrGroupNotFound
	}
	for _, r := range runners {
		ack := make(chan struct{})
		select {
		case r.trigger <- ack:
		case <-r.done:
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-ack:
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.waitApplied(ctx)
}

func (m *Manager) waitApplied(ctx context.Context) error {
	m.mu.Lock()
	target := m.changeSeq
	m.mu.Unlock()
	for {
		m.mu.Lock()
		if m.appliedSeq >= target || m.stopped || !m.started {
			m.mu.Unlock()
			return nil
		}
		wait := m.appliedCh
		m.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Statuses returns the state of every alias entry in config order.
func (m *Manager) Statuses() []Status {
	if m == nil {
		return []Status{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0)
	for _, key := range m.order {
		g := m.groups[key]
		if g == nil {
			continue
		}
		polls := g.spec.polls()
		for _, entry := range g.spec.entries {
			status := Status{
				Group:          g.spec.name,
				Alias:          entry.alias,
				BaseURL:        g.spec.target.BaseURL,
				Mode:           ModePinned,
				ResolvedModel:  entry.pinned,
				Match:          entry.match,
				ReportedModels: append([]string{}, g.models...),
			}
			if entry.following() {
				status.Mode = ModeFollow
				status.ResolvedModel = g.resolved[normalizeAlias(entry.alias)]
			}
			if polls {
				status.PollInterval = g.spec.interval.String()
				status.LastError = g.lastError
				if !g.lastCheck.IsZero() {
					checked := g.lastCheck
					status.LastCheckAt = &checked
				}
				if !g.lastSuccess.IsZero() {
					success := g.lastSuccess
					status.LastSuccessAt = &success
				}
			}
			out = append(out, status)
		}
	}
	return out
}

// Running reports how many group pollers are active. Intended for tests and diagnostics.
func (m *Manager) Running() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, g := range m.groups {
		if g.cancel != nil {
			count++
		}
	}
	return count
}

// Effective returns cfg with follow entries resolved for runtime use. Follow
// entries always force-map responses to their alias. An unresolved entry keeps
// its alias as a placeholder name and is marked FollowPending so executors can
// fail fast. The input is never modified; without follow entries it is returned as is.
func (m *Manager) Effective(cfg *config.Config) *config.Config {
	if cfg == nil || !cfg.HasOpenAICompatibilityFollow() {
		return cfg
	}
	out := cfg.CloneForRuntime()
	var resolved map[string]map[string]string
	if m != nil {
		m.mu.Lock()
		resolved = make(map[string]map[string]string, len(m.groups))
		for key, g := range m.groups {
			byAlias := make(map[string]string, len(g.resolved))
			for alias, id := range g.resolved {
				byAlias[alias] = id
			}
			resolved[key] = byAlias
		}
		m.mu.Unlock()
	}
	for i := range out.OpenAICompatibility {
		compat := &out.OpenAICompatibility[i]
		key := groupKey(compat)
		for j := range compat.Models {
			model := &compat.Models[j]
			if !model.Follow {
				continue
			}
			model.ForceMapping = true
			if !model.IsFollowing() {
				continue
			}
			if id := resolved[key][normalizeAlias(model.Alias)]; id != "" {
				model.Name = id
				continue
			}
			model.Name = strings.TrimSpace(model.Alias)
			model.FollowPending = true
		}
	}
	return out
}
