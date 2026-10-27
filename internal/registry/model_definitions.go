// Package registry provides model definitions and lookup helpers for various AI providers.
// Static model metadata is loaded from the embedded models.json file and can be refreshed from network.
package registry

import (
	"strings"
)

const (
	codexBuiltinImage15ModelID         = "gpt-image-1.5"
	codexBuiltinImageModelID           = "gpt-image-2"
	codexBuiltinImage25FlareModelID    = "gpt-image-2.5-flare"
	codexBuiltinImage25SunburstModelID = "gpt-image-2.5-sunburst"
	codexBuiltinImage25ModelID         = "gpt-image-2.5"
	xaiBuiltinImageModelID             = "grok-imagine-image"
	xaiBuiltinImageQualityModelID      = "grok-imagine-image-quality"
	xaiBuiltinImage20ModelID           = "grok-imagine-image-2.0"
	xaiBuiltinVideoModelID             = "grok-imagine-video"
	xaiBuiltinVideo15ModelID           = "grok-imagine-video-1.5"
	xaiBuiltinVideo15PreviewID         = "grok-imagine-video-1.5-preview"
	xaiBuiltinSpeechModelID            = "grok-tts"
	xaiBuiltinSpeechVoiceModelID       = "grok-voice-tts-1.0"
)

// staticModelsJSON mirrors the top-level structure of models.json.
type staticModelsJSON struct {
	Claude    []*ModelInfo `json:"claude"`
	CodexFree []*ModelInfo `json:"codex-free"`
	CodexTeam []*ModelInfo `json:"codex-team"`
	CodexPlus []*ModelInfo `json:"codex-plus"`
	CodexPro  []*ModelInfo `json:"codex-pro"`
	XAI       []*ModelInfo `json:"xai"`
	Meta      []*ModelInfo `json:"meta"`
}

// GetClaudeModels returns the standard Claude model definitions.
func GetClaudeModels() []*ModelInfo {
	return cloneModelInfos(getModels().Claude)
}

// GetCodexFreeModels returns model definitions for the Codex free plan tier.
func GetCodexFreeModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexFree))
}

// GetCodexTeamModels returns model definitions for the Codex team plan tier.
func GetCodexTeamModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexTeam))
}

// GetCodexPlusModels returns model definitions for the Codex plus plan tier.
func GetCodexPlusModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPlus))
}

// GetCodexProModels returns model definitions for the Codex pro plan tier.
func GetCodexProModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPro))
}

// GetXAIModels returns the standard xAI Grok model definitions.
func GetXAIModels() []*ModelInfo {
	return WithXAIBuiltins(cloneModelInfos(getModels().XAI))
}

// WithCodexBuiltins injects hard-coded Codex-only model definitions that should
// not depend on remote models.json updates. Built-ins replace any matching IDs
// already present in the provided slice.
func WithCodexBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models,
		codexBuiltinImage15ModelInfo(),
		codexBuiltinImageModelInfo(),
		codexBuiltinImage25FlareModelInfo(),
		codexBuiltinImage25SunburstModelInfo(),
		codexBuiltinImage25ModelInfo(),
	)
}

// WithXAIBuiltins injects hard-coded xAI image, video, and speech model definitions
// that should not depend on remote models.json updates.
func WithXAIBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, xaiBuiltinImageModelInfo(), xaiBuiltinImageQualityModelInfo(), xaiBuiltinImage20ModelInfo(), xaiBuiltinVideoModelInfo(), xaiBuiltinVideo15ModelInfo(), xaiBuiltinVideo15PreviewModelInfo(), xaiBuiltinSpeechModelInfo(), xaiBuiltinSpeechVoiceModelInfo())
}

func codexBuiltinImage15ModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImage15ModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 1.5",
		Version:     codexBuiltinImage15ModelID,
	}
}

func codexBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImageModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2",
		Version:     codexBuiltinImageModelID,
	}
}

func codexBuiltinImage25FlareModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImage25FlareModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2.5 Flare",
		Version:     codexBuiltinImage25FlareModelID,
	}
}

func codexBuiltinImage25SunburstModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImage25SunburstModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2.5 Sunburst",
		Version:     codexBuiltinImage25SunburstModelID,
	}
}

func codexBuiltinImage25ModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImage25ModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2.5",
		Version:     codexBuiltinImage25ModelID,
	}
}

func xaiBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image",
		Name:        xaiBuiltinImageModelID,
		Description: "xAI Grok image generation model.",
	}
}

func xaiBuiltinImageQualityModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageQualityModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image Quality",
		Name:        xaiBuiltinImageQualityModelID,
		Description: "xAI Grok higher-fidelity image generation model.",
	}
}

func xaiBuiltinImage20ModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImage20ModelID,
		Object:      "model",
		Created:     1786060800, // 2026-08-07
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image 2.0",
		Name:        xaiBuiltinImage20ModelID,
		Description: "xAI Grok image generation model.",
	}
}

func xaiBuiltinVideoModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinVideoModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Video",
		Name:        xaiBuiltinVideoModelID,
		Description: "xAI Grok video generation model.",
	}
}

func xaiBuiltinVideo15ModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinVideo15ModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Video 1.5",
		Name:        xaiBuiltinVideo15ModelID,
		Description: "xAI Grok video generation model.",
	}
}

func xaiBuiltinVideo15PreviewModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinVideo15PreviewID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Video 1.5 Preview",
		Name:        xaiBuiltinVideo15PreviewID,
		Description: "Compatibility alias for the xAI Grok video generation model.",
	}
}

func xaiBuiltinSpeechModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinSpeechModelID,
		Object:      "model",
		Created:     1773619200, // 2026-03-16
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok TTS",
		Name:        xaiBuiltinSpeechModelID,
		Description: "xAI Grok unary text-to-speech model.",
	}
}

func xaiBuiltinSpeechVoiceModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinSpeechVoiceModelID,
		Object:      "model",
		Created:     1773619200, // 2026-03-16
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Voice TTS 1.0",
		Name:        xaiBuiltinSpeechVoiceModelID,
		Description: "xAI Grok unary text-to-speech model.",
	}
}

func upsertModelInfos(models []*ModelInfo, extras ...*ModelInfo) []*ModelInfo {
	if len(extras) == 0 {
		return models
	}

	extraIDs := make(map[string]struct{}, len(extras))
	extraList := make([]*ModelInfo, 0, len(extras))
	for _, extra := range extras {
		if extra == nil {
			continue
		}
		id := strings.TrimSpace(extra.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := extraIDs[key]; exists {
			continue
		}
		extraIDs[key] = struct{}{}
		extraList = append(extraList, cloneModelInfo(extra))
	}

	if len(extraList) == 0 {
		return models
	}

	filtered := make([]*ModelInfo, 0, len(models)+len(extraList))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := extraIDs[strings.ToLower(id)]; exists {
			continue
		}
		filtered = append(filtered, model)
	}

	filtered = append(filtered, extraList...)
	return filtered
}

// cloneModelInfos returns a shallow copy of the slice with each element deep-cloned.
func cloneModelInfos(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*ModelInfo, len(models))
	for i, m := range models {
		out[i] = cloneModelInfo(m)
	}
	return out
}

// GetStaticModelDefinitionsByChannel returns static model definitions for a given channel/provider.
// It returns nil when the channel is unknown.
//
// Supported channels:
//   - claude
//   - codex
//   - xai
//   - meta
func GetStaticModelDefinitionsByChannel(channel string) []*ModelInfo {
	key := strings.ToLower(strings.TrimSpace(channel))
	switch key {
	case "claude":
		return GetClaudeModels()
	case "codex":
		return GetCodexProModels()
	case "xai", "x-ai", "grok":
		return GetXAIModels()
	case "meta", "muse":
		return GetMetaModels()
	default:
		return nil
	}
}

// LookupStaticModelInfoByChannel searches one provider-specific static section.
// It does not fall back across providers, so callers can preserve provenance.
func LookupStaticModelInfoByChannel(modelID, channel string) *ModelInfo {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	models := GetStaticModelDefinitionsByChannel(channel)
	for _, model := range models {
		if model != nil && model.ID == modelID {
			return cloneModelInfo(model)
		}
	}
	if strings.EqualFold(strings.TrimSpace(channel), "claude") {
		for _, familyID := range claudeFamilyModelIDs(modelID) {
			for _, model := range models {
				if model != nil && model.ID == familyID {
					return cloneModelInfo(model)
				}
			}
		}
	}
	return nil
}

// GetMetaModels returns the standard Meta Muse model definitions.
func GetMetaModels() []*ModelInfo {
	return cloneModelInfos(getModels().Meta)
}

// LookupStaticModelInfo searches all static model definitions for a model by ID.
// Returns nil if no matching model is found.
func LookupStaticModelInfo(modelID string) *ModelInfo {
	if modelID == "" {
		return nil
	}

	data := getModels()
	allModels := [][]*ModelInfo{
		data.Claude,
		data.CodexPro,
		data.XAI,
		GetMetaModels(),
	}
	for _, models := range allModels {
		for _, m := range models {
			if m != nil && m.ID == modelID {
				return cloneModelInfo(m)
			}
		}
	}

	// Dated and bracketed Claude variants (claude-sonnet-5-5-20261001,
	// claude-opus-5-5[1m]) share the capabilities of their catalog entry.
	for _, familyID := range claudeFamilyModelIDs(modelID) {
		for _, m := range data.Claude {
			if m != nil && m.ID == familyID {
				return cloneModelInfo(m)
			}
		}
	}

	return nil
}

// claudeFamilyModelIDs returns the catalog IDs a Claude model variant may belong
// to: without a "[...]" context suffix, then also without a "-YYYYMMDD" date.
func claudeFamilyModelIDs(modelID string) []string {
	if !strings.HasPrefix(modelID, "claude-") {
		return nil
	}
	var ids []string
	base := modelID
	if i := strings.IndexByte(base, '['); i > 0 && strings.HasSuffix(base, "]") {
		base = base[:i]
		ids = append(ids, base)
	}
	if n := len(base); n > 9 && base[n-9] == '-' && isEightDigits(base[n-8:]) {
		ids = append(ids, base[:n-9])
	}
	return ids
}

func isEightDigits(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
