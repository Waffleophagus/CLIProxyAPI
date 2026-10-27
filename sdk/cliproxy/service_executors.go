package cliproxy

import (
	"context"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

type openAICompatibilityRegistrationCache struct {
	cfg     *config.Config
	byName  map[string]*openAICompatibilityRegistrationEntry
	byIndex map[int]*openAICompatibilityRegistrationEntry
}

type openAICompatibilityRegistrationEntry struct {
	providerKey string
	models      []*ModelInfo
}

func (s *Service) newOpenAICompatibilityRegistrationCache() *openAICompatibilityRegistrationCache {
	if s == nil {
		return nil
	}
	return newOpenAICompatibilityRegistrationCacheForConfig(s.currentConfig())
}

func newOpenAICompatibilityRegistrationCacheForConfig(cfg *config.Config) *openAICompatibilityRegistrationCache {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return nil
	}

	cache := &openAICompatibilityRegistrationCache{
		cfg:     cfg,
		byName:  make(map[string]*openAICompatibilityRegistrationEntry, len(cfg.OpenAICompatibility)),
		byIndex: make(map[int]*openAICompatibilityRegistrationEntry, len(cfg.OpenAICompatibility)),
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		compatName := strings.TrimSpace(compat.Name)
		key := strings.ToLower(compatName)
		providerName := strings.ToLower(compatName)
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		entry := &openAICompatibilityRegistrationEntry{
			providerKey: util.OpenAICompatibleProviderKey(providerName),
			models:      buildOpenAICompatibilityConfigModels(compat),
		}
		cache.byIndex[i] = entry
		if _, exists := cache.byName[key]; !exists {
			cache.byName[key] = entry
		}
	}
	if len(cache.byName) == 0 {
		return nil
	}
	return cache
}

func (c *openAICompatibilityRegistrationCache) lookup(auth *coreauth.Auth, compatName string) (*openAICompatibilityRegistrationEntry, bool) {
	if c == nil {
		return nil, false
	}
	if auth != nil && auth.AuthSourceKind() == coreauth.AuthSourceConfig && auth.Attributes != nil {
		if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[coreauth.AttributeConfigIndex])); errIndex == nil {
			entry, ok := c.byIndex[index]
			return entry, ok
		}
	}
	entry, ok := c.byName[strings.ToLower(strings.TrimSpace(compatName))]
	return entry, ok
}

func (s *Service) ensureExecutorsForAuth(a *coreauth.Auth) {
	s.ensureExecutorsForAuthWithContext(context.Background(), a, false)
}

func (s *Service) ensureExecutorsForAuthWithMode(a *coreauth.Auth, forceReplace bool) {
	s.ensureExecutorsForAuthWithContext(context.Background(), a, forceReplace)
}

func (s *Service) ensureExecutorsForAuthWithContext(ctx context.Context, a *coreauth.Auth, forceReplace bool) {
	if a == nil || (ctx != nil && ctx.Err() != nil) {
		return
	}
	s.registerAvailableExecutors(ctx, executorRegistrationOptions{
		auths:             []*coreauth.Auth{a},
		forceReplaceAuths: forceReplace,
	})
}

func (s *Service) registerAvailableExecutors(ctx context.Context, opts executorRegistrationOptions) {
	if s == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.executorRegistrationMu.Lock()
	defer s.executorRegistrationMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	// Keep all Service-owned executor registration paths here so native and
	// auth-derived executors stay in the same binding order.
	if opts.includeBaseline {
		s.registerExecutorsForAuths(baselineExecutorAuths(), opts.forceReplaceAuths)
	}
	if len(opts.auths) > 0 {
		s.registerExecutorsForAuths(opts.auths, opts.forceReplaceAuths)
	}
}

func baselineExecutorAuths() []*coreauth.Auth {
	providers := []string{
		"codex",
		"claude",
		"xai",
		"meta",
		"openai-compatibility",
	}
	auths := make([]*coreauth.Auth, 0, len(providers))
	for _, provider := range providers {
		auth := &coreauth.Auth{
			ID:       provider,
			Provider: provider,
		}
		if provider == "openai-compatibility" {
			auth.Attributes = map[string]string{"compat_name": "openai-compatibility"}
		}
		auths = append(auths, auth)
	}
	return auths
}

func (s *Service) registerExecutorsForAuths(auths []*coreauth.Auth, forceReplace bool) {
	reboundCodex := false
	reboundXAI := false
	for _, auth := range auths {
		if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			if reboundCodex && forceReplace {
				continue
			}
			reboundCodex = true
		}
		if auth != nil && !auth.Disabled && strings.EqualFold(strings.TrimSpace(auth.Provider), "xai") {
			if reboundXAI && forceReplace {
				continue
			}
			reboundXAI = true
		}
		s.registerExecutorForAuth(auth, forceReplace)
	}
}

func (s *Service) registerExecutorForAuth(a *coreauth.Auth, forceReplace bool) {
	if s == nil || s.coreManager == nil || a == nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if strings.EqualFold(strings.TrimSpace(a.Provider), "codex") {
		if !forceReplace {
			existingExecutor, hasExecutor := s.coreManager.Executor("codex")
			if hasExecutor {
				existingCodexAutoExecutor, isCodexAutoExecutor := existingExecutor.(*executor.CodexAutoExecutor)
				if isCodexAutoExecutor && existingCodexAutoExecutor.UsesConfig(cfg) {
					return
				}
			}
		}
		s.coreManager.RegisterExecutor(executor.NewCodexAutoExecutor(cfg))
		return
	}
	// Skip disabled auth entries when (re)binding executors.
	// Disabled auths can linger during config reloads (e.g., removed OpenAI-compat entries)
	// and must not override active provider executors.
	if a.Disabled {
		return
	}
	if compatProviderKey, _, isCompat := openAICompatInfoFromAuth(a); isCompat {
		if compatProviderKey == "" {
			compatProviderKey = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		if compatProviderKey == "" {
			compatProviderKey = "openai-compatibility"
		}
		s.registerOpenAICompatProviderExecutor(compatProviderKey, cfg, forceReplace, false)
		return
	}
	switch strings.ToLower(a.Provider) {
	case "claude":
		s.coreManager.RegisterExecutor(executor.NewClaudeExecutor(cfg))
	case "xai":
		if !forceReplace {
			existingExecutor, hasExecutor := s.coreManager.Executor("xai")
			if hasExecutor {
				existingXAIAutoExecutor, isXAIAutoExecutor := existingExecutor.(*executor.XAIAutoExecutor)
				if isXAIAutoExecutor && existingXAIAutoExecutor.UsesConfig(cfg) {
					return
				}
			}
		}
		s.coreManager.RegisterExecutor(executor.NewXAIAutoExecutor(cfg))
	case "meta":
		s.coreManager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	default:
		providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		s.registerOpenAICompatProviderExecutor(providerKey, cfg, forceReplace, true)
	}
}

// registerOpenAICompatProviderExecutor binds a native OpenAI-compat executor.
// When respectNonOwned is true, an existing non-native executor is preserved.
func (s *Service) registerOpenAICompatProviderExecutor(providerKey string, cfg *config.Config, forceReplace bool, respectNonOwned bool) {
	if s == nil || s.coreManager == nil {
		return
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		providerKey = "openai-compatibility"
	}
	if !forceReplace {
		if existingExecutor, hasExecutor := s.coreManager.Executor(providerKey); hasExecutor {
			if shouldKeepExistingOpenAICompatExecutor(existingExecutor, respectNonOwned) {
				return
			}
		}
	}
	s.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(providerKey, cfg))
}

func shouldKeepExistingOpenAICompatExecutor(existing coreauth.ProviderExecutor, respectNonOwned bool) bool {
	if existing == nil {
		return false
	}
	if _, existingBare := existing.(*executor.OpenAICompatExecutor); existingBare {
		return true
	}
	// Historical openai-compatibility path only short-circuits bare native executors.
	return respectNonOwned
}

// registerResolvedModelsForAuthFenced publishes models only if fence still
// matches inherited global exclusions and the current auth snapshot, and the
// registry epoch has not advanced. A superseded registration must not
// overwrite or remove a newer one. The returned value is false only when
// the fence rejected the publication; true also covers no-op inputs, so
// it never by itself proves models were published. Callers that drive a
// completion pass use false to reconcile the latest auth instead of
// losing the registration.
func (s *Service) registerResolvedModelsForAuthFenced(a *coreauth.Auth, fence modelRegistrationFence, providerKey string, models []*ModelInfo) bool {
	if a == nil || a.ID == "" {
		return true
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		return s.unregisterModelsForAuthFenced(a, fence)
	}
	normalizedModels := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		clone := *model
		clone.ID = modelID
		normalizedModels = append(normalizedModels, &clone)
	}
	if len(normalizedModels) == 0 {
		return s.unregisterModelsForAuthFenced(a, fence)
	}
	if !fence.publishModels(s, a.ID, providerKey, normalizedModels) {
		log.WithFields(log.Fields{"auth_id": a.ID, "provider": providerKey, "models": len(normalizedModels)}).Warnf("model registration for auth %s was superseded during computation; discarded the stale publication instead of overwriting the latest auth state", a.ID)
		return false
	}
	return true
}

// unregisterModelsForAuthFenced removes a client's models only if fence
// still matches inherited global exclusions and the current auth snapshot, and
// the registry epoch has not advanced since the registration began. The
// returned value is false only when the fence rejected the removal.
func (s *Service) unregisterModelsForAuthFenced(a *coreauth.Auth, fence modelRegistrationFence) bool {
	if a == nil || a.ID == "" {
		return true
	}
	if !fence.publishUnregister(s, a.ID) {
		log.WithFields(log.Fields{"auth_id": a.ID}).Warnf("model unregistration for auth %s was superseded during computation; discarded the stale removal instead of deleting the latest auth state", a.ID)
		return false
	}
	return true
}
