// Package thinking provides unified thinking configuration processing.
package thinking

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var providerAppliersMu sync.RWMutex

// nativeProviderAppliers maps built-in provider names to their implementations.
var nativeProviderAppliers = map[string]ProviderApplier{
	"claude": nil,
	"openai": nil,
	"codex":  nil,
	"xai":    nil,
}

// GetProviderApplier returns the ProviderApplier for the given provider name.
// Returns nil if the provider is not registered.
func GetProviderApplier(provider string) ProviderApplier {
	provider = normalizedProviderName(provider)
	if provider == "" {
		return nil
	}
	providerAppliersMu.RLock()
	defer providerAppliersMu.RUnlock()
	return nativeProviderAppliers[provider]
}

// RegisterProvider registers a provider applier by name.
func RegisterProvider(name string, applier ProviderApplier) {
	name = normalizedProviderName(name)
	if name == "" {
		return
	}
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	nativeProviderAppliers[name] = applier
}

func normalizedProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// IsUserDefinedModel reports whether the model is a user-defined model that should
// have thinking configuration passed through without validation.
//
// User-defined models are configured via config file's models[] array
// (e.g., openai-compatibility.*.models[], *-api-key.models[]). These models
// are marked with UserDefined=true at registration time.
//
// User-defined models should have their thinking configuration applied directly,
// letting the upstream service validate the configuration.
func IsUserDefinedModel(modelInfo *registry.ModelInfo) bool {
	if modelInfo == nil {
		return true
	}
	return modelInfo.UserDefined
}

// ApplyThinking applies thinking configuration to a request body.
//
// This is the unified entry point for all providers. It follows the processing
// order defined in FR25: route check → model capability query → config extraction
// → validation → application.
//
// Suffix Priority: When the model name includes a thinking suffix (e.g., "claude-sonnet-4-5(8192)"),
// the suffix configuration takes priority over any thinking parameters in the request body.
// This enables users to override thinking settings via the model name without modifying their
// request payload.
//
// Parameters:
//   - body: Original request body JSON
//   - model: Model name, optionally with thinking suffix (e.g., "claude-sonnet-4-5(16384)")
//   - fromFormat: Source request format (e.g., openai, codex, claude)
//   - toFormat: Target provider format for the request body (claude, openai, codex, xai)
//   - providerKey: Provider identifier used for registry model lookups (may differ from toFormat, e.g., openrouter -> openai)
//
// Returns:
//   - Modified request body JSON with thinking configuration applied
//   - Error if validation fails (ThinkingError). On error, a non-nil target
//     body is returned (with unsupported updates already removed).
//
// Passthrough behavior (returns original body without error):
//   - Unknown provider (not in providerAppliers map)
//   - modelInfo.Thinking is nil (model doesn't support thinking)
//
// Note: Unknown models (modelInfo is nil) are treated as user-defined models: we skip
// validation and still apply the thinking config so the upstream can validate it.
//
// Example:
//
//	// With suffix - suffix config takes priority
//	result, err := thinking.ApplyThinking(body, "claude-sonnet-4-5(8192)", "claude", "claude", "claude")
//
//	// Without suffix - uses body config
//	result, err := thinking.ApplyThinking(body, "claude-sonnet-4-5", "claude", "claude", "claude")
func ApplyThinking(body []byte, model string, fromFormat string, toFormat string, providerKey string) ([]byte, error) {
	summaryConfig := ExtractSummaryConfig(body, toFormat)
	return applyThinking(body, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
}

// ApplyThinkingWithSummary applies canonical thinking effort while preserving
// summary visibility extracted from the original source request. Callers that
// translate before applying thinking must pass the source config explicitly:
// a target Claude body can temporarily lack display while disabled thinking is
// being rewritten by a model suffix.
func ApplyThinkingWithSummary(body []byte, model string, fromFormat string, toFormat string, providerKey string, summaryConfig SummaryConfig) ([]byte, error) {
	return applyThinking(body, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
}

// ApplyThinkingWithSourceAndSummary applies thinking using the original source
// request when translation has already changed the target protocol. Without a
// bound model definition, an unknown model has no configuration update support.
func ApplyThinkingWithSourceAndSummary(body, sourceBody []byte, model, fromFormat, toFormat, providerKey string, summaryConfig SummaryConfig) ([]byte, error) {
	return applyThinking(body, sourceBody, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
}

// ApplyThinkingWithModelInfo applies thinking with the exact configured model
// definition selected for an API-key execution attempt while preserving summary
// visibility from the original source body.
func ApplyThinkingWithModelInfo(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo) ([]byte, error) {
	summaryConfig := ExtractSummaryConfig(sourceBody, fromFormat)
	if len(sourceBody) == 0 {
		summaryConfig = ExtractSummaryConfig(body, toFormat)
	}
	return ApplyThinkingWithModelInfoAndSummary(body, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, summaryConfig)
}

// ApplyThinkingWithModelInfoAndSummary applies the exact configured model
// definition with a summary intent already resolved across source translation.
func ApplyThinkingWithModelInfoAndSummary(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo, summaryConfig SummaryConfig) ([]byte, error) {
	return applyThinking(body, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, true, summaryConfig)
}

func applyThinking(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, resolvedModelInfo *registry.ModelInfo, modelInfoResolved bool, summaryConfig SummaryConfig) ([]byte, error) {
	providerFormat := strings.ToLower(strings.TrimSpace(toFormat))
	if providerFormat == "openai-response" {
		providerFormat = "codex"
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		providerKey = providerFormat
	}
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	if fromFormat == "" {
		fromFormat = providerFormat
	}
	// Summary visibility is orthogonal to thinking effort. Keep the original
	// source intent before a suffix-specific applier rewrites provider fields,
	// then restore it after the canonical effort has been applied.
	// 1. Parse suffix and get modelInfo
	suffixResult := ParseSuffix(model)
	baseModel := suffixResult.ModelName
	// Use provider-specific lookup to handle capability differences across providers.
	modelInfo := resolvedModelInfo
	if !modelInfoResolved {
		modelInfo = registry.LookupModelInfo(baseModel, providerKey)
	}

	// Resolve source intent before stripping unsupported target input items.
	var sourceConfig ThinkingConfig
	if isResponsesFormat(fromFormat) {
		sourceRequest := body
		if len(sourceBody) > 0 {
			sourceRequest = sourceBody
		}
		sourceConfig = extractCodexUsageConfig(sourceRequest)
	}
	responseTarget := providerFormat == "codex" || providerFormat == "xai"
	supportsUpdates := modelInfo != nil && modelInfo.SupportConfigurationUpdate
	if responseTarget && !supportsUpdates {
		body = stripConfigurationUpdates(body)
	}
	nativeResponses := responseTarget && isResponsesFormat(fromFormat) && supportsUpdates

	// 2. Route check: Get provider applier after target cleanup.
	applier := GetProviderApplier(providerFormat)
	if applier == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
		}).Debug("thinking: unknown provider, passthrough |")
		return body, nil
	}

	// 3. Model capability check
	if (isOpenAIFamily(providerFormat) || providerFormat == "xai") && detectModelCapability(modelInfo) == CapabilityHybrid {
		// OpenAI Chat/Responses and xAI appliers accept effort levels only.
		// Keep the original budget range in the model catalog.
		levelModel, levelSupport := *modelInfo, *modelInfo.Thinking
		levelSupport.Min, levelSupport.Max = 0, 0
		levelModel.Thinking = &levelSupport
		modelInfo = &levelModel
	}
	if nativeResponses {
		var errNormalize error
		body, errNormalize = normalizeNativeResponsesEffort(body, modelInfo, applier, fromFormat, providerFormat, HasThinkingSuffix(model))
		if errNormalize != nil {
			return body, errNormalize
		}
	}
	// Unknown models are treated as user-defined so thinking config can still be applied.
	// The upstream service is responsible for validating the configuration.
	if !suffixResult.HasSuffix && len(sourceBody) > 0 && isResponsesFormat(fromFormat) && !gjson.ValidBytes(body) && hasThinkingConfig(extractConfigurationUpdateConfig(sourceBody)) {
		// Do not rebuild a malformed target from a separate source update.
		return body, nil
	}
	if nativeResponses && !suffixResult.HasSuffix {
		// Native Responses preserves the baseline and update timeline after validation.
		if log.IsLevelEnabled(log.DebugLevel) && (modelInfo.Thinking != nil || modelInfo.UserDefined) {
			if config := extractCodexUsageConfig(body); hasThinkingConfig(config) {
				fields := log.Fields{
					"provider": providerFormat,
					"model":    modelInfo.ID,
					"mode":     config.Mode,
					"budget":   config.Budget,
					"level":    config.Level,
				}
				if baseline := extractCodexConfig(body); baseline.Mode == ModeLevel {
					fields["baseline_level"] = baseline.Level
				}
				entry := log.WithFields(fields)
				entry.Debug("thinking: original config from request |")
				entry.Debug("thinking: processed config to apply |")
			}
		}
		return body, nil
	}
	if fromFormat == "claude" && providerFormat == "claude" && !suffixResult.HasSuffix && !gjson.GetBytes(body, "thinking").Exists() {
		// Native Claude effort is independent of thinking activation. Preserve the
		// model's thinking default unless a suffix or explicit summary changes it.
		return applySummaryConfigForProvider(body, providerFormat, baseModel, providerKey, modelInfo, summaryConfig), nil
	}
	if IsUserDefinedModel(modelInfo) {
		return applyUserDefinedModel(body, modelInfo, fromFormat, providerFormat, providerKey, suffixResult, sourceConfig, nativeResponses, summaryConfig)
	}
	if modelInfo.Thinking == nil {
		config := extractThinkingConfig(body, providerFormat)
		if hasThinkingConfig(config) || summaryConfig.Mode != SummaryUnspecified {
			log.WithFields(log.Fields{
				"model":    baseModel,
				"provider": providerFormat,
			}).Debug("thinking: model does not support thinking, stripping config |")
			if responseTarget {
				return stripResponsesEffort(body), nil
			}
			return StripThinkingConfig(body, providerFormat), nil
		}
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    baseModel,
		}).Debug("thinking: model does not support thinking, passthrough |")
		return body, nil
	}

	// 4. Get config: suffix priority over body
	var config ThinkingConfig
	if suffixResult.HasSuffix {
		config = parseSuffixToConfig(suffixResult.RawSuffix, providerFormat, model)
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
			"mode":     config.Mode,
			"budget":   config.Budget,
			"level":    config.Level,
		}).Debug("thinking: config from model suffix |")
	} else {
		config = sourceConfig
		if !hasThinkingConfig(config) && modelInfoResolved && len(sourceBody) > 0 {
			config = extractSourceThinkingConfig(sourceBody, fromFormat)
		}
		if !hasThinkingConfig(config) {
			config = extractThinkingConfig(body, providerFormat)
		}
		if hasThinkingConfig(config) {
			log.WithFields(log.Fields{
				"provider": providerFormat,
				"model":    modelInfo.ID,
				"mode":     config.Mode,
				"budget":   config.Budget,
				"level":    config.Level,
			}).Debug("thinking: original config from request |")
		}
	}

	if !hasThinkingConfig(config) {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
		}).Debug("thinking: no config found, passthrough |")
		if nativeResponses {
			return body, nil
		}
		if modelInfoResolved && providerFormat == "claude" && fromFormat != providerFormat && ExtractSummaryConfig(sourceBody, fromFormat).Mode == SummaryEnabled {
			// Registry translation can only see aggregate model capabilities. For a
			// cross-protocol summary-only request it may have activated adaptive
			// thinking solely to make display valid. The selected API-key model is
			// authoritative at execution time, so discard that inferred activation
			// when the exact model supports only manual extended thinking. Use the
			// source intent here even if a target normalizer removed display; in that
			// case the inferred amount must disappear with it. Explicit native Claude
			// thinking never reaches this cross-protocol branch.
			body = stripInferredClaudeSummaryActivation(body, modelInfo)
		}
		return applySummaryConfigForProvider(body, providerFormat, baseModel, providerKey, modelInfo, summaryConfig), nil
	}
	if modelInfoResolved && config.Mode == ModeLevel && modelInfo != nil && modelInfo.Thinking != nil && shouldMapConfiguredHighIntent(fromFormat, providerFormat, modelInfo) {
		config.Level = mapConfiguredHighIntent(config.Level, modelInfo)
	}

	// 5. Validate and normalize configuration
	validated, err := ValidateConfig(config, modelInfo, fromFormat, providerFormat, suffixResult.HasSuffix)
	if err != nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
			"error":    err.Error(),
		}).Warn("thinking: validation failed |")
		// Return the target body on validation failure (defensive programming).
		// Unsupported update items were already removed before validation.
		return body, err
	}

	// Defensive check: ValidateConfig should never return (nil, nil)
	if validated == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
		}).Warn("thinking: ValidateConfig returned nil config without error, passthrough |")
		return body, nil
	}

	log.WithFields(log.Fields{
		"provider": providerFormat,
		"model":    modelInfo.ID,
		"mode":     validated.Mode,
		"budget":   validated.Budget,
		"level":    validated.Level,
	}).Debug("thinking: processed config to apply |")

	// 6. Apply configuration using provider-specific applier, then restore the
	// target summary intent that was explicit before suffix processing.
	applied, err := applier.Apply(body, *validated, modelInfo)
	if err != nil {
		return applied, err
	}
	// A fully disabled amount takes precedence over visibility. Re-applying a
	// summary-only field can recreate an otherwise removed provider config and
	// make a default-on model think again.
	if thinkingIsFullyDisabled(*validated) || nativeResponses {
		return applied, nil
	}
	if providerFormat == "claude" && thinkingRequestedOff(config) {
		// The model cannot turn thinking off, so ValidateConfig fell back to its
		// lowest level. Drop any stale block_binding from the off request and keep
		// the caller's intent of seeing no thinking: without an explicit omitted
		// display, cloaking would add progress updates.
		applied, _ = sjson.DeleteBytes(applied, "thinking.block_binding")
		summaryConfig = SummaryConfig{Mode: SummaryDisabled}
	}
	return applySummaryConfigForProvider(applied, providerFormat, baseModel, providerKey, modelInfo, summaryConfig), nil
}

func thinkingIsFullyDisabled(config ThinkingConfig) bool {
	return config.Mode == ModeNone && config.Budget == 0 && config.Level == ""
}

func thinkingRequestedOff(config ThinkingConfig) bool {
	return config.Mode == ModeNone || (config.Mode == ModeBudget && config.Budget == 0)
}

func shouldMapConfiguredHighIntent(fromFormat, toFormat string, modelInfo *registry.ModelInfo) bool {
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	toFormat = strings.ToLower(strings.TrimSpace(toFormat))
	if fromFormat != toFormat {
		return true
	}
	if modelInfo == nil {
		return false
	}
	modelType := strings.ToLower(strings.TrimSpace(modelInfo.Type))
	return modelType != "" && !isSameProviderFamily(toFormat, modelType)
}

func mapConfiguredHighIntent(level ThinkingLevel, modelInfo *registry.ModelInfo) ThinkingLevel {
	if modelInfo == nil || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) == 0 {
		return level
	}
	level = ThinkingLevel(strings.ToLower(strings.TrimSpace(string(level))))
	var candidates []ThinkingLevel
	switch level {
	case LevelXHigh:
		candidates = []ThinkingLevel{LevelXHigh, LevelMax, LevelHigh}
	case LevelMax:
		candidates = []ThinkingLevel{LevelMax, LevelXHigh, LevelHigh}
	default:
		return level
	}
	for _, candidate := range candidates {
		if isLevelSupported(string(candidate), modelInfo.Thinking.Levels) {
			return candidate
		}
	}
	return level
}

func extractSourceThinkingConfig(body []byte, provider string) ThinkingConfig {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "openai-response" {
		return extractCodexConfig(body)
	}
	return extractThinkingConfig(body, provider)
}

// parseSuffixToConfig converts a raw suffix string to ThinkingConfig.
//
// Parsing priority:
//  1. Special values: "none" → ModeNone, "auto"/"-1" → ModeAuto
//  2. Level names: "minimal", "low", "medium", "high", "xhigh" → ModeLevel
//  3. Numeric values: positive integers → ModeBudget, 0 → ModeNone
//
// If none of the above match, returns empty ThinkingConfig (treated as no config).
func parseSuffixToConfig(rawSuffix, provider, model string) ThinkingConfig {
	// 1. Try special values first (none, auto, -1)
	if mode, ok := ParseSpecialSuffix(rawSuffix); ok {
		switch mode {
		case ModeNone:
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case ModeAuto:
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		}
	}

	// 2. Try level parsing (minimal, low, medium, high, xhigh, max)
	if level, ok := ParseLevelSuffix(rawSuffix); ok {
		return ThinkingConfig{Mode: ModeLevel, Level: level}
	}

	// 3. Try numeric parsing
	if budget, ok := ParseNumericSuffix(rawSuffix); ok {
		if budget == 0 {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeBudget, Budget: budget}
	}

	// Unknown suffix format - return empty config
	log.WithFields(log.Fields{
		"provider":   provider,
		"model":      model,
		"raw_suffix": rawSuffix,
	}).Debug("thinking: unknown suffix format, treating as no config |")
	return ThinkingConfig{}
}

// applyUserDefinedModel applies thinking configuration for user-defined models
// without ThinkingSupport validation.
func applyUserDefinedModel(body []byte, modelInfo *registry.ModelInfo, fromFormat, toFormat, providerKey string, suffixResult SuffixResult, sourceConfig ThinkingConfig, nativeResponses bool, summaryConfig SummaryConfig) ([]byte, error) {
	// Get model ID for logging
	modelID := ""
	if modelInfo != nil {
		modelID = modelInfo.ID
	} else {
		modelID = suffixResult.ModelName
	}

	// Get config: suffix priority over body
	var config ThinkingConfig
	if suffixResult.HasSuffix {
		config = parseSuffixToConfig(suffixResult.RawSuffix, toFormat, modelID)
		log.WithFields(log.Fields{
			"provider": toFormat,
			"model":    modelID,
			"mode":     config.Mode,
			"budget":   config.Budget,
			"level":    config.Level,
		}).Debug("thinking: config from model suffix |")
	} else {
		config = sourceConfig
		if !hasThinkingConfig(config) {
			config = extractThinkingConfig(body, fromFormat)
		}
		if !hasThinkingConfig(config) && fromFormat != toFormat {
			config = extractThinkingConfig(body, toFormat)
		}
		if hasThinkingConfig(config) {
			log.WithFields(log.Fields{
				"provider": toFormat,
				"model":    modelID,
				"mode":     config.Mode,
				"budget":   config.Budget,
				"level":    config.Level,
			}).Debug("thinking: original config from request |")
		}
	}

	if !hasThinkingConfig(config) {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (no config) |")
		return applySummaryConfigForProvider(body, toFormat, modelID, providerKey, modelInfo, summaryConfig), nil
	}

	applier := GetProviderApplier(toFormat)
	if applier == nil {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (unknown provider) |")
		return body, nil
	}

	// Validate user-defined models that declare thinking support so translated
	// budgets are clamped to model limits; invalid configs pass through unchanged.
	if modelInfo != nil && modelInfo.Thinking != nil {
		if normalized, errValidate := ValidateConfig(config, modelInfo, fromFormat, toFormat, suffixResult.HasSuffix); errValidate == nil {
			config = *normalized
		}
	}
	log.WithFields(log.Fields{
		"provider": toFormat,
		"model":    modelID,
		"mode":     config.Mode,
		"budget":   config.Budget,
		"level":    config.Level,
	}).Debug("thinking: processed config to apply |")
	applied, err := applier.Apply(body, config, modelInfo)
	if err != nil {
		return applied, err
	}
	if thinkingIsFullyDisabled(config) || nativeResponses {
		return applied, nil
	}
	return applySummaryConfigForProvider(applied, toFormat, modelID, providerKey, modelInfo, summaryConfig), nil
}

// extractThinkingConfig extracts provider-specific thinking config from request body.
func extractThinkingConfig(body []byte, provider string) ThinkingConfig {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ThinkingConfig{}
	}

	switch provider {
	case "claude":
		return extractClaudeConfig(body)
	case "openai":
		return extractOpenAIConfig(body)
	case "codex", "xai":
		return extractCodexConfig(body)
	default:
		return ThinkingConfig{}
	}
}

func hasThinkingConfig(config ThinkingConfig) bool {
	return config.Mode != ModeBudget || config.Budget != 0 || config.Level != ""
}

// ExtractReasoningEffort returns the source request's thinking setting as a
// canonical reasoning_effort label for usage logging. Responses updates take
// precedence over a suffix because they describe the source turn's intent;
// otherwise a valid suffix overrides the top-level setting.
func ExtractReasoningEffort(body []byte, provider, model string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if isResponsesFormat(provider) {
		if effort := reasoningEffortFromConfig(extractConfigurationUpdateConfig(body)); effort != "" {
			return effort
		}
	}
	if effort := reasoningEffortFromSuffix(ParseSuffix(model)); effort != "" {
		return effort
	}

	config := extractThinkingConfigForUsage(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai-response":
			config = extractCodexUsageConfig(body)
		case "openai":
			config = extractCodexUsageConfig(body)
		}
	}
	return reasoningEffortFromConfig(config)
}

// ExtractTranslatedReasoningEffort returns the final provider payload's thinking
// setting as a canonical reasoning_effort label for usage logging.
func ExtractTranslatedReasoningEffort(body []byte, provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	config := extractThinkingConfigForUsage(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai", "openai-response":
			config = extractCodexUsageConfig(body)
			if !hasThinkingConfig(config) {
				config = extractOpenAIConfig(body)
			}
		}
	}
	return reasoningEffortFromConfig(config)
}

func extractThinkingConfigForUsage(body []byte, provider string) ThinkingConfig {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "xai", "openai-response":
		return extractCodexUsageConfig(body)
	case "claude":
		// between_tools is how claude-sonnet-5-5 turns thinking off.
		if gjson.GetBytes(body, "thinking.type").String() == "between_tools" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		config := extractThinkingConfig(body, provider)
		if config.Mode == ModeNone && hasThinkingConfig(config) {
			return config
		}
		// The last effort directive, not the cached top-level placeholder, is the turn's effort.
		if directive := extractClaudeEffortDirectiveConfig(body); hasThinkingConfig(directive) {
			return directive
		}
		return config
	default:
		return extractThinkingConfig(body, provider)
	}
}

func reasoningEffortFromSuffix(suffix SuffixResult) string {
	if !suffix.HasSuffix {
		return ""
	}
	return reasoningEffortFromConfig(parseSuffixToConfig(suffix.RawSuffix, "", suffix.ModelName))
}

func reasoningEffortFromConfig(config ThinkingConfig) string {
	if !hasThinkingConfig(config) {
		return ""
	}
	switch config.Mode {
	case ModeNone:
		return string(LevelNone)
	case ModeAuto:
		return string(LevelAuto)
	case ModeLevel:
		return strings.ToLower(strings.TrimSpace(string(config.Level)))
	case ModeBudget:
		level, ok := ConvertBudgetToLevel(config.Budget)
		if !ok {
			return ""
		}
		return level
	default:
		return ""
	}
}

// extractClaudeConfig extracts thinking configuration from Claude format request body.
//
// Claude API format:
//   - thinking.type: "enabled", "adaptive", "auto", or "disabled"
//   - thinking.budget_tokens: integer (-1=auto, 0=disabled, >0=budget)
//   - output_config.effort: string ("none", "auto", "low", "medium", "high", "max")
//
// Priority: thinking.type="disabled" takes precedence over budget_tokens.
// When type="enabled", budget_tokens takes precedence over output_config.effort.
// When type="enabled" without budget_tokens, output_config.effort is preserved if present;
// otherwise returns ModeAuto to indicate the user wants thinking enabled with default budget.
func extractClaudeConfig(body []byte) ThinkingConfig {
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType == "disabled" {
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	}
	if !gjson.GetBytes(body, "thinking").Exists() || thinkingType == "adaptive" || thinkingType == "auto" {
		// Claude output_config.effort also applies when thinking is omitted.
		// We only treat it as a thinking config when effort is explicitly present;
		// otherwise we passthrough and let upstream defaults apply.
		if effort := gjson.GetBytes(body, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value == "" {
				return ThinkingConfig{}
			}
			switch value {
			case "none":
				return ThinkingConfig{Mode: ModeNone, Budget: 0}
			case "auto":
				return ThinkingConfig{Mode: ModeAuto, Budget: -1}
			default:
				return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
			}
		}
		return ThinkingConfig{}
	}

	// Check budget_tokens
	if budget := gjson.GetBytes(body, "thinking.budget_tokens"); budget.Exists() {
		value := int(budget.Int())
		switch value {
		case 0:
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case -1:
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		default:
			return ThinkingConfig{Mode: ModeBudget, Budget: value}
		}
	}

	// If type="enabled", check output_config.effort before falling back to auto
	if thinkingType == "enabled" {
		if effort := gjson.GetBytes(body, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value != "" {
				switch value {
				case "none":
					return ThinkingConfig{Mode: ModeNone, Budget: 0}
				case "auto":
					return ThinkingConfig{Mode: ModeAuto, Budget: -1}
				default:
					return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
				}
			}
		}
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	}

	return ThinkingConfig{}
}

// extractOpenAIConfig extracts thinking configuration from OpenAI format request body.
//
// OpenAI API format:
//   - reasoning_effort: "none", "low", "medium", "high" (discrete levels)
//
// OpenAI uses level-based thinking configuration only, no numeric budget support.
// The "none" value is treated specially to return ModeNone.
func extractOpenAIConfig(body []byte) ThinkingConfig {
	// Check reasoning_effort (OpenAI Chat Completions format)
	if effort := gjson.GetBytes(body, "reasoning_effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}

	return ThinkingConfig{}
}

// extractCodexConfig extracts thinking configuration from Codex format request body.
//
// Codex API format (OpenAI Responses API):
//   - reasoning.effort: "none", "low", "medium", "high"
//
// This is similar to OpenAI but uses nested field "reasoning.effort" instead of "reasoning_effort".
func extractCodexConfig(body []byte) ThinkingConfig {
	// Check reasoning.effort (Codex / OpenAI Responses API format)
	if effort := gjson.GetBytes(body, "reasoning.effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}

	return ThinkingConfig{}
}

// extractCodexUsageConfig returns the last effective Responses update for usage
// reporting, falling back to the top-level effort when no update applies.
func extractCodexUsageConfig(body []byte) ThinkingConfig {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ThinkingConfig{}
	}
	if config := extractConfigurationUpdateConfig(body); hasThinkingConfig(config) {
		return config
	}
	return extractCodexConfig(body)
}
