package cliproxy

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

func (s *Service) currentConfig() *config.Config {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return cfg
}

// currentSourceConfig returns the configuration as loaded, without resolved slugs.
func (s *Service) currentSourceConfig() *config.Config {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	cfg := s.sourceCfg
	if cfg == nil {
		cfg = s.cfg
	}
	s.cfgMu.RUnlock()
	return cfg
}

func (s *Service) currentWatcher() *WatcherWrapper {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	w := s.watcher
	s.cfgMu.RUnlock()
	return w
}

func (s *Service) setWatcher(w *WatcherWrapper) {
	if s == nil {
		return
	}
	s.cfgMu.Lock()
	s.watcher = w
	s.cfgMu.Unlock()
}

func (s *Service) applyWatcherConfigUpdate(newCfg *config.Config) {
	s.applyConfigUpdateWithAuthSynthesis(context.Background(), newCfg, false)
}

type configCommit struct {
	// cfg is the effective runtime config; source is the config as loaded.
	cfg      *config.Config
	source   *config.Config
	sequence uint64
	// sourceGeneration identifies source. A slug resolution change re-commits
	// the current source under the same generation, so server and pprof updates
	// are skipped only once that generation has been fully applied.
	sourceGeneration uint64
}

type routingRuntimeState struct {
	strategy                 string
	sessionAffinity          bool
	sessionAffinityTTL       time.Duration
	sessionAffinitySubagents bool
	sessionAffinityRebindOn  string
}

func normalizedRoutingRuntimeState(cfg *config.Config) routingRuntimeState {
	state := routingRuntimeState{
		strategy:                 "round-robin",
		sessionAffinityTTL:       time.Hour,
		sessionAffinitySubagents: true,
		sessionAffinityRebindOn:  coreauth.SessionAffinityRebindOnAny,
	}
	if cfg == nil {
		return state
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		state.strategy = "weighted-round-robin"
	case "fill-first", "fillfirst", "ff":
		state.strategy = "fill-first"
	}
	state.sessionAffinity = cfg.Routing.SessionAffinity
	if ttl := strings.TrimSpace(cfg.Routing.SessionAffinityTTL); ttl != "" {
		if parsed, errParse := time.ParseDuration(ttl); errParse == nil && parsed > 0 {
			if parsed < time.Second {
				parsed = time.Second
			}
			state.sessionAffinityTTL = parsed
		}
	}
	if state.sessionAffinity && cfg.Routing.SessionAffinitySubagents != nil {
		state.sessionAffinitySubagents = *cfg.Routing.SessionAffinitySubagents
	}
	if state.sessionAffinity && strings.EqualFold(strings.TrimSpace(cfg.Routing.SessionAffinityRebindOn), coreauth.SessionAffinityRebindOnQuota) {
		state.sessionAffinityRebindOn = coreauth.SessionAffinityRebindOnQuota
	}
	return state
}

func newRoutingSelector(state routingRuntimeState) coreauth.Selector {
	var selector coreauth.Selector
	switch state.strategy {
	case "weighted-round-robin":
		selector = &coreauth.WeightedRoundRobinSelector{}
	case "fill-first":
		selector = &coreauth.FillFirstSelector{}
	default:
		selector = &coreauth.RoundRobinSelector{}
	}
	if state.sessionAffinity {
		subagents := state.sessionAffinitySubagents
		selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
			Fallback:         selector,
			TTL:              state.sessionAffinityTTL,
			SubagentAffinity: &subagents,
			RebindOn:         state.sessionAffinityRebindOn,
		})
	}
	return selector
}

func (s *Service) applyConfigUpdateWithAuthSynthesis(ctx context.Context, newCfg *config.Config, synthesizeConfigAuths bool) bool {
	commit := s.commitConfigUpdate(newCfg)
	if commit.cfg == nil {
		return false
	}
	return s.applyConfigRuntime(ctx, commit, synthesizeConfigAuths)
}

// commitConfigUpdate applies only in-memory configuration state. Runtime work that
// may block on storage or networking is deliberately deferred. Catalog
// source generations change here so an older commit cannot restart stale readers.
func (s *Service) commitConfigUpdate(newCfg *config.Config) configCommit {
	if s == nil {
		return configCommit{}
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	// A nil config re-commits the current source, e.g. after a slug resolution.
	newSource := newCfg != nil
	if !newSource {
		newCfg = s.currentSourceConfig()
	}
	if newCfg == nil {
		return configCommit{}
	}
	if errValidate := newCfg.ValidateCredentialWeights(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected config update with invalid credential weights")
		return configCommit{}
	}

	if errValidate := newCfg.Models.Validate(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected invalid model catalog sources")
		return configCommit{}
	}
	if errValidate := newCfg.ValidateOpenAICompatibilityFollow(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected config update with invalid follow-mode models")
		return configCommit{}
	}
	if errValidate := newCfg.ValidateTailscale(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected config update with invalid tailscale settings")
		return configCommit{}
	}
	// Pollers never take service locks, so reconfiguring them here cannot deadlock.
	s.slugs.Reconfigure(newCfg)
	effective := s.slugs.Effective(newCfg)
	s.cfgMu.Lock()
	s.cfg = effective
	s.sourceCfg = newCfg
	s.cfgMu.Unlock()
	s.configSequence++
	if newSource {
		s.sourceGeneration++
	}
	registry.UpdateModelCatalogSources(newCfg.Models)
	return configCommit{cfg: effective, source: newCfg, sequence: s.configSequence, sourceGeneration: s.sourceGeneration}
}

// applySlugResolution re-runs the config runtime path after a follow-mode slug
// resolved to a new upstream model, so routing, model registration and alias
// tables pick it up without a config edit or restart.
func (s *Service) applySlugResolution(ctx context.Context) {
	if s == nil {
		return
	}
	commit := s.commitConfigUpdate(nil)
	if commit.cfg == nil {
		return
	}
	s.applyConfigRuntime(ctx, commit, true)
}

func (s *Service) configCommitCurrent(commit configCommit) bool {
	if s == nil || commit.sequence == 0 {
		return false
	}
	s.configUpdateMu.Lock()
	current := s.configSequence == commit.sequence
	s.configUpdateMu.Unlock()
	return current
}

func (s *Service) applyConfigRuntime(ctx context.Context, commit configCommit, synthesizeConfigAuths bool) bool {
	cfg := commit.cfg
	if s == nil || cfg == nil {
		return false
	}
	s.configRuntimeMu.Lock()
	defer s.configRuntimeMu.Unlock()
	if !s.configCommitCurrent(commit) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}

	if !s.applyManagerConfig(ctx, commit) {
		return false
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	// A current commit always carries the latest source generation. Push it to
	// the server unless it already got there: a slug commit can supersede a
	// reload commit before that reload's own apply runs, and must not drop it.
	if commit.sourceGeneration != s.appliedSourceGeneration {
		source := commit.source
		if source == nil {
			source = cfg
		}
		if !s.applyPprofConfigContext(ctx, source) {
			return false
		}
		s.applyUsageConfig(source)
		s.noteTailscaleConfig(source)
		if errContext := ctx.Err(); errContext != nil {
			return false
		}
		if !s.updateServerClientsContext(ctx, source) {
			return false
		}
		if errContext := ctx.Err(); errContext != nil {
			return false
		}
		s.appliedSourceGeneration = commit.sourceGeneration
	}

	registrationCtx := coreauth.WithSkipPersist(ctx)
	var auths []*coreauth.Auth
	if s.coreManager != nil {
		auths = s.coreManager.List()
	}
	s.registerAvailableExecutors(registrationCtx, executorRegistrationOptions{
		forceReplaceAuths: true,
		auths:             auths,
	})
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if synthesizeConfigAuths {
		s.registerConfigAPIKeyAuths(registrationCtx, cfg)
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if s.coreManager != nil && cfg.SaveCooldownStatus {
		if errRestoreCooldown := s.coreManager.RestoreCooldownStates(registrationCtx); errRestoreCooldown != nil && ctx.Err() == nil {
			log.Warnf("failed to restore cooldown state after config update: %v", errRestoreCooldown)
		}
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	s.syncModelRuntime(registrationCtx)
	return ctx.Err() == nil
}

func (s *Service) applyManagerConfig(ctx context.Context, commit configCommit) bool {
	if s == nil || s.coreManager == nil || commit.cfg == nil {
		return s != nil && commit.cfg != nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	routingState := normalizedRoutingRuntimeState(commit.cfg)
	if s.appliedRoutingState == nil || *s.appliedRoutingState != routingState {
		s.coreManager.SetSelector(newRoutingSelector(routingState))
		s.appliedRoutingState = &routingState
	}
	s.applyRetryConfig(commit.cfg)
	store := s.resolveCooldownStateStore(commit.cfg)
	if !s.coreManager.ApplyConfigWithCooldownStateStore(ctx, commit.cfg, store) {
		return false
	}
	s.coreManager.SetOAuthModelAlias(commit.cfg.OAuthModelAlias)
	return true
}

func (s *Service) updateServerClientsContext(ctx context.Context, cfg *config.Config) bool {
	if s == nil || cfg == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	if s.updateServerClientsContextFn != nil {
		return s.updateServerClientsContextFn(ctx, cfg)
	}
	if s.server == nil {
		return true
	}
	return s.server.UpdateClientsContext(ctx, cfg)
}

func (s *Service) reloadConfigFromWatcher() bool {
	w := s.currentWatcher()
	if w == nil {
		return false
	}
	return w.ReloadConfigIfChanged()
}

func (s *Service) registerConfigAPIKeyAuths(ctx context.Context, cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	configSynth := synthesizer.NewConfigSynthesizer()
	auths, errSynthesize := configSynth.Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		log.Warnf("failed to synthesize config API key auths: %v", errSynthesize)
		return
	}

	registrationCtx := coreauth.WithDeferredAPIKeyModelAliasRebuild(ctx)
	tasks := make([]modelRegistrationTask, 0, len(auths))
	needsAliasRebuild := false
	for _, auth := range auths {
		if !coreauth.IsConfigAPIKeyAuth(auth) {
			continue
		}
		prepared := s.prepareCoreAuthForModelRegistration(registrationCtx, auth)
		if prepared == nil {
			continue
		}
		needsAliasRebuild = true
		authForRegistration := prepared
		tasks = append(tasks, modelRegistrationTask{
			authID:   authForRegistration.ID,
			phase:    modelRegistrationPhaseConfigAPIKey,
			category: modelRegistrationCategory(authForRegistration),
			run: func(compatCache *openAICompatibilityRegistrationCache) {
				s.completeModelRegistrationForAuthWithCache(registrationCtx, authForRegistration, compatCache)
			},
		})
	}
	if needsAliasRebuild {
		s.coreManager.RefreshAPIKeyModelAlias()
	}
	s.runModelRegistrationTasks(registrationCtx, tasks)
}
