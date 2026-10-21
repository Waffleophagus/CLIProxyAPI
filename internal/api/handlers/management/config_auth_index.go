package management

import (
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func liveAuthIndexFromManager(manager *coreauth.Manager) map[string]string {
	out := map[string]string{}
	if manager == nil {
		return out
	}
	// authManager.List() returns clones, so EnsureIndex only affects these copies.
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := strings.TrimSpace(auth.ID)
		if id == "" {
			continue
		}
		idx := strings.TrimSpace(auth.Index)
		if idx == "" {
			idx = auth.EnsureIndex()
		}
		if idx == "" {
			continue
		}
		out[id] = idx
	}
	return out
}

func yamlMapScalar(node *yaml.Node, key string) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return strings.TrimSpace(node.Content[i+1].Value)
		}
	}
	return ""
}

func setMapScalar(mapping *yaml.Node, key, value string) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Value = value
			mapping.Content[i+1].Tag = "!!str"
			mapping.Content[i+1].Kind = yaml.ScalarNode
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func deleteMapKey(mapping *yaml.Node, key string) bool {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return true
		}
	}
	return false
}

func stripAPIKeysAuthIndexesFromGroup(group *yaml.Node) {
	if group == nil || group.Kind != yaml.MappingNode {
		return
	}
	deleteMapKey(group, "auth_index")
	deleteMapKey(group, "auth-index")
	keysNode := configV8Node(group, []string{"keys"})
	if keysNode != nil && keysNode.Kind == yaml.SequenceNode {
		for _, keyNode := range keysNode.Content {
			if keyNode != nil && keyNode.Kind == yaml.MappingNode {
				deleteMapKey(keyNode, "auth_index")
				deleteMapKey(keyNode, "auth-index")
			}
		}
	}
}

func stripAPIKeysAuthIndexesFromGroups(groupsNode *yaml.Node) {
	if groupsNode == nil {
		return
	}
	if groupsNode.Kind == yaml.SequenceNode {
		for _, group := range groupsNode.Content {
			stripAPIKeysAuthIndexesFromGroup(group)
		}
	} else if groupsNode.Kind == yaml.MappingNode {
		stripAPIKeysAuthIndexesFromGroup(groupsNode)
	}
}

func stripAPIKeysAuthIndexesFromProvidersMap(apiKeysNode *yaml.Node) {
	if apiKeysNode == nil || apiKeysNode.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(apiKeysNode.Content); i += 2 {
		stripAPIKeysAuthIndexesFromGroups(apiKeysNode.Content[i+1])
	}
}

func stripAPIKeysAuthIndexesFromRoot(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	apiKeysNode := configV8Node(root, []string{"api-keys"})
	if apiKeysNode != nil {
		stripAPIKeysAuthIndexesFromProvidersMap(apiKeysNode)
	}
}

func stripAPIKeysAuthIndexesFromUpdate(parts []string, update *yaml.Node) {
	if update == nil || len(update.Content) == 0 {
		return
	}
	node := update.Content[0]
	if len(parts) == 0 {
		stripAPIKeysAuthIndexesFromRoot(node)
		return
	}
	if parts[0] != "api-keys" {
		return
	}
	if len(parts) == 1 {
		stripAPIKeysAuthIndexesFromProvidersMap(node)
		return
	}
	stripAPIKeysAuthIndexesFromGroups(node)
}

func (h *Handler) injectV8APIKeyAuthIndexesLocked(root *yaml.Node, data []byte) {
	if h == nil || root == nil || root.Kind != yaml.MappingNode {
		return
	}
	apiKeysNode := configV8Node(root, []string{"api-keys"})
	if apiKeysNode == nil || apiKeysNode.Kind != yaml.MappingNode {
		return
	}

	cfg, errParse := config.ParseConfigBytes(data)
	if errParse != nil {
		cfg = h.cfg
	}
	if cfg == nil {
		return
	}

	synth := synthesizer.NewConfigSynthesizer()
	synthCtx := &synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, errSynth := synth.Synthesize(synthCtx)
	if errSynth != nil {
		return
	}

	liveIndexByID := liveAuthIndexFromManager(h.authManager)
	resolveAuthIndex := func(a *coreauth.Auth) string {
		if a == nil {
			return ""
		}
		if idx := strings.TrimSpace(liveIndexByID[a.ID]); idx != "" {
			return idx
		}
		return a.EnsureIndex()
	}

	authsByProvider := make(map[string][]*coreauth.Auth)
	for _, a := range auths {
		if a != nil {
			authsByProvider[a.Provider] = append(authsByProvider[a.Provider], a)
		}
	}

	authByConfigIndex := make(map[string]map[string]*coreauth.Auth)
	for _, p := range []string{"codex", "claude", "xai", "meta"} {
		authByConfigIndex[p] = make(map[string]*coreauth.Auth)
		for _, a := range authsByProvider[p] {
			if a != nil && a.Attributes != nil {
				authByConfigIndex[p][a.Attributes["config_index"]] = a
			}
		}
	}

	for i := 0; i+1 < len(apiKeysNode.Content); i += 2 {
		providerName := strings.TrimSpace(apiKeysNode.Content[i].Value)
		groupsNode := apiKeysNode.Content[i+1]
		if groupsNode == nil || groupsNode.Kind != yaml.SequenceNode {
			continue
		}

		if providerName == "openai-compatibility" {
			validGroupIdx := 0
			for _, group := range groupsNode.Content {
				if group == nil || group.Kind != yaml.MappingNode {
					continue
				}
				groupBase := strings.TrimSpace(yamlMapScalar(group, "base-url"))
				if groupBase == "" {
					continue
				}
				targetGroupIdxStr := strconv.Itoa(validGroupIdx)
				validGroupIdx++

				isOpenAICompatAuth := func(a *coreauth.Auth) bool {
					if a == nil || a.Attributes == nil {
						return false
					}
					if a.Attributes["config_index"] != targetGroupIdxStr {
						return false
					}
					return a.Provider == "openai-compatibility" || strings.HasPrefix(a.Provider, "openai-compatible-")
				}

				keysNode := configV8Node(group, []string{"keys"})
				if keysNode == nil || keysNode.Kind != yaml.SequenceNode || len(keysNode.Content) == 0 {
					for _, a := range auths {
						if isOpenAICompatAuth(a) && a.Attributes["api_key"] == "" {
							if idx := resolveAuthIndex(a); idx != "" {
								setMapScalar(group, "auth_index", idx)
							}
							break
						}
					}
					continue
				}

				var groupAuths []*coreauth.Auth
				for _, a := range auths {
					if isOpenAICompatAuth(a) {
						groupAuths = append(groupAuths, a)
					}
				}

				for kIdx, keyNode := range keysNode.Content {
					if keyNode == nil || keyNode.Kind != yaml.MappingNode {
						continue
					}
					if kIdx < len(groupAuths) {
						if idx := resolveAuthIndex(groupAuths[kIdx]); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		if providerName == "claude" {
			rawIdx := 0
			for _, group := range groupsNode.Content {
				if group == nil || group.Kind != yaml.MappingNode {
					continue
				}
				groupBase := strings.TrimSpace(yamlMapScalar(group, "base-url"))
				keysNode := configV8Node(group, []string{"keys"})
				if keysNode == nil || keysNode.Kind != yaml.SequenceNode {
					continue
				}
				for _, keyNode := range keysNode.Content {
					if keyNode == nil || keyNode.Kind != yaml.MappingNode {
						continue
					}
					apiKey := strings.TrimSpace(yamlMapScalar(keyNode, "api-key"))
					targetIdx := strconv.Itoa(rawIdx)
					rawIdx++
					if apiKey == "" && groupBase == "" {
						continue
					}
					if a := authByConfigIndex["claude"][targetIdx]; a != nil {
						if idx := resolveAuthIndex(a); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		if providerName == "codex" || providerName == "xai" {
			validIdx := 0
			for _, group := range groupsNode.Content {
				if group == nil || group.Kind != yaml.MappingNode {
					continue
				}
				groupBase := strings.TrimSpace(yamlMapScalar(group, "base-url"))
				if groupBase == "" {
					continue
				}
				keysNode := configV8Node(group, []string{"keys"})
				if keysNode == nil || keysNode.Kind != yaml.SequenceNode {
					continue
				}
				for _, keyNode := range keysNode.Content {
					if keyNode == nil || keyNode.Kind != yaml.MappingNode {
						continue
					}
					targetIdx := strconv.Itoa(validIdx)
					validIdx++
					if a := authByConfigIndex[providerName][targetIdx]; a != nil {
						if idx := resolveAuthIndex(a); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}

		if providerName == "meta" {
			validIdx := 0
			for _, group := range groupsNode.Content {
				if group == nil || group.Kind != yaml.MappingNode {
					continue
				}
				keysNode := configV8Node(group, []string{"keys"})
				if keysNode == nil || keysNode.Kind != yaml.SequenceNode {
					continue
				}
				for _, keyNode := range keysNode.Content {
					if keyNode == nil || keyNode.Kind != yaml.MappingNode {
						continue
					}
					apiKey := strings.TrimSpace(yamlMapScalar(keyNode, "api-key"))
					if apiKey == "" || strings.HasPrefix(apiKey, "dca:") {
						continue
					}
					targetIdx := strconv.Itoa(validIdx)
					validIdx++
					if a := authByConfigIndex["meta"][targetIdx]; a != nil {
						if idx := resolveAuthIndex(a); idx != "" {
							setMapScalar(keyNode, "auth_index", idx)
						}
					}
				}
			}
			continue
		}
	}
}
