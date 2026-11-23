package management

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// ConfigV8ContextKey marks operations registered by the v8 API that save configuration.
const ConfigV8ContextKey = "management.config-v8"

// ConfigV8 serves the persisted configuration using v8 names. GET is a view;
// only a successful configuration mutation migrates the stored document.
//
// Reads return an ETag holding the SHA-256 of the on-disk config bytes.
// Writes honour an optional If-Match against that token, so a client can
// detect that another writer saved after its read (412) instead of silently
// overwriting the change. Without If-Match, writes stay unconditional.
func (h *Handler) ConfigV8(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	raw, err := os.ReadFile(h.configFilePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read_failed"})
		return
	}
	diskHash := config.ComputeSourceHash(raw)
	if c.Request.Method != http.MethodGet && !ifMatchSatisfied(c.Request.Header.Values("If-Match"), diskHash) {
		c.Header("ETag", configV8ETag(diskHash))
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "precondition_failed"})
		return
	}
	data, _, commentedSections, err := config.NormalizeConfigLayoutSilent(raw, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(data, &doc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid_config"})
		return
	}
	root := doc.Content[0]
	path := strings.Trim(c.Param("path"), "/")
	parts := []string{}
	if path != "" {
		parts = strings.Split(path, "/")
	}
	yamlRequest := strings.HasSuffix(c.FullPath(), "/config.yaml")
	if c.Request.Method == http.MethodGet {
		config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
		if !yamlRequest {
			if servers := configV8Node(root, v8ICEServersPath); servers != nil && servers.Kind == yaml.SequenceNode {
				for _, server := range servers.Content {
					deleteConfigV8Path(server, []string{"username"})
					deleteConfigV8Path(server, []string{"credential"})
				}
			}
			deleteConfigV8Path(root, v8TailscaleAuthKeyPath)
			h.injectV8APIKeyAuthIndexesLocked(root, data)
		}
		value := configV8Node(root, parts)
		if value == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("ETag", configV8ETag(diskHash))
		if yamlRequest {
			c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
			return
		}
		var result any
		if err = value.Decode(&result); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode_failed"})
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	before := cloneConfigV8Node(root)
	config.ProjectV8ConfigAliases(root, strings.Join(parts, "."))
	commentSource := cloneConfigV8Node(root)
	if c.Request.Method == http.MethodDelete {
		if len(parts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_delete_config"})
			return
		}
		if !deleteConfigV8Path(root, parts) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
	} else {
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "invalid_body"})
			return
		}
		if !yamlRequest && !json.Valid(body) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
		var update yaml.Node
		if err = yaml.Unmarshal(body, &update); err != nil || len(update.Content) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		stripAPIKeysAuthIndexesFromUpdate(parts, &update)
		if len(parts) == 0 && update.Content[0].Kind != yaml.MappingNode {
			c.JSON(http.StatusBadRequest, gin.H{"error": "config_must_be_object"})
			return
		}
		if len(parts) == 0 {
			if err = config.NormalizeV8ConfigAliases(update.Content[0]); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
				return
			}
		}
		// Paths identify YAML keys, never array indexes. Lists are replaced whole.
		dst := root
		for _, part := range parts {
			if part == "" || dst.Kind != yaml.MappingNode {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path"})
				return
			}
			next := configV8Node(dst, []string{part})
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				dst.Content = append(dst.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
			}
			dst = next
		}
		if c.Request.Method == http.MethodPatch {
			mergeConfigV8Patch(dst, update.Content[0])
		} else {
			if len(parts) == 0 {
				doc.HeadComment = update.HeadComment
				doc.FootComment = update.FootComment
			}
			if dst.Kind == yaml.ScalarNode && update.Content[0].Kind == yaml.ScalarNode {
				update.Content[0].HeadComment = dst.HeadComment
				update.Content[0].LineComment = dst.LineComment
				update.Content[0].FootComment = dst.FootComment
			}
			*dst = *update.Content[0]
		}
	}
	if !yamlRequest && c.Request.Method != http.MethodDelete {
		// JSON carries no comments: keep those of entries that survive a
		// replacement. YAML writes state their comments explicitly.
		carryConfigV8Comments(root, commentSource)
	}
	if err = config.NormalizeV8ConfigAliases(root); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if !yamlRequest && c.Request.Method != http.MethodDelete {
		preserveV8TURNSecrets(root, before)
		preserveV8TailscaleAuthKey(root, before)
	}
	stripAPIKeysAuthIndexesFromRoot(root)
	config.NormalizeCollectionNodeStyles(root)
	data, err = yaml.Marshal(&doc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	next, err := config.ParseConfigBytes(data)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	if err = config.ValidateV8Config(data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	var finalCommented []config.ArchivedV8Section
	data, _, finalCommented, err = config.NormalizeConfigLayoutSilent(data, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_config", "message": err.Error()})
		return
	}
	// Save the validated canonical tree directly: projecting runtime defaults
	// back onto it loses explicit nulls and empty maps.
	// WriteConfig retains the inode of a mounted configuration file.
	if err = WriteConfig(h.configFilePath, data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write_failed", "message": err.Error()})
		return
	}
	// Emit migration warnings only after the migrated document is persisted
	// and only for sections whose archived comment block is in the written document.
	config.WarnUnrecognizedV8Sections(config.FilterArchivedV8Sections(data, commentedSections))
	config.WarnUnrecognizedV8Sections(finalCommented)
	// WriteConfig may normalize the bytes, so report the version actually on
	// disk. If it cannot be read back, omit the ETag instead of guessing one.
	// SourceHash keeps its runtime owner and is published by the reload, so it
	// is not overwritten on next here.
	if saved, errSaved := os.ReadFile(h.configFilePath); errSaved == nil {
		c.Header("ETag", configV8ETag(config.ComputeSourceHash(saved)))
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "config-version": 8})
}

// configV8ETag renders a config source hash as a strong ETag.
func configV8ETag(hash string) string {
	return `"` + hash + `"`
}

// ifMatchSatisfied reports whether the optional If-Match header lines pass
// the RFC 9110 strong comparison against currentHash. Repeated lines form
// one list. An absent header always passes; a present but blank header
// carries no tag and fails; the whole field "*" matches any current
// version; weak and malformed entity tags never match.
func ifMatchSatisfied(values []string, currentHash string) bool {
	if len(values) == 0 {
		return true
	}
	field := strings.TrimSpace(strings.Join(values, ","))
	if field == "*" {
		return true
	}
	for _, tag := range splitEntityTagList(field) {
		tag = strings.TrimSpace(tag)
		if len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"' && !strings.Contains(tag[1:len(tag)-1], `"`) && tag[1:len(tag)-1] == currentHash {
			return true
		}
	}
	return false
}

// splitEntityTagList splits an If-Match field value on list commas. A comma
// inside a quoted entity tag is tag content, not a separator.
func splitEntityTagList(field string) []string {
	parts := []string{}
	start := 0
	quoted := false
	for i := 0; i < len(field); i++ {
		switch field[i] {
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				parts = append(parts, field[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, field[start:])
}

var v8ICEServersPath = []string{"oauth", "providers", "codex", "live-media-relay", "ice-servers"}

// JSON reads redact TURN secrets. Preserve omitted credentials when a client
// writes that JSON back, matching by endpoint rather than array position so a
// changed URL cannot inherit credentials for a different server. Explicit empty
// strings/null clear a secret; YAML writes retain full replacement semantics.
func preserveV8TURNSecrets(root, before *yaml.Node) {
	next := configV8Node(root, v8ICEServersPath)
	previous := configV8Node(before, v8ICEServersPath)
	if next == nil || previous == nil || next.Kind != yaml.SequenceNode || previous.Kind != yaml.SequenceNode {
		return
	}
	matched := make([]bool, len(previous.Content))
	for _, server := range next.Content {
		urls := configV8Node(server, []string{"urls"})
		if urls == nil {
			continue
		}
		var newURLs []string
		if urls.Decode(&newURLs) != nil {
			continue
		}
		for i, old := range previous.Content {
			if matched[i] {
				continue
			}
			oldURLs := configV8Node(old, []string{"urls"})
			var previousURLs []string
			if oldURLs == nil || oldURLs.Decode(&previousURLs) != nil || !reflect.DeepEqual(newURLs, previousURLs) {
				continue
			}
			matched[i] = true
			for _, name := range []string{"username", "credential"} {
				if configV8Node(server, []string{name}) == nil {
					if secret := configV8Node(old, []string{name}); secret != nil {
						server.Content = append(server.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, cloneConfigV8Node(secret))
					}
				}
			}
			break
		}
	}
}

var v8TailscaleAuthKeyPath = []string{"tailscale", "auth-key"}

// JSON reads redact tailscale.auth-key. Keep the stored key when a JSON write
// omits it; an explicit empty string or null clears it.
func preserveV8TailscaleAuthKey(root, before *yaml.Node) {
	section := configV8Node(root, v8TailscaleAuthKeyPath[:1])
	if section == nil || section.Kind != yaml.MappingNode || configV8Node(section, v8TailscaleAuthKeyPath[1:]) != nil {
		return
	}
	if previous := configV8Node(before, v8TailscaleAuthKeyPath); previous != nil {
		section.Content = append(section.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v8TailscaleAuthKeyPath[1]}, cloneConfigV8Node(previous))
	}
}

func configV8Node(root *yaml.Node, parts []string) *yaml.Node {
	for _, part := range parts {
		if root == nil || root.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == part {
				next = root.Content[i+1]
				break
			}
		}
		root = next
	}
	return root
}

// deleteConfigV8Path prunes only empty ancestors of the removed field. Other
// explicit empty maps can carry inheritance semantics and stay intact.
func deleteConfigV8Path(root *yaml.Node, parts []string) bool {
	if root == nil || root.Kind != yaml.MappingNode || len(parts) == 0 {
		return false
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != parts[0] {
			continue
		}
		if len(parts) > 1 {
			child := root.Content[i+1]
			if !deleteConfigV8Path(child, parts[1:]) {
				return false
			}
			if len(child.Content) > 0 {
				return true
			}
		}
		root.Content = append(root.Content[:i], root.Content[i+2:]...)
		return true
	}
	return false
}

func cloneConfigV8Node(node *yaml.Node) *yaml.Node {
	copy := *node
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = cloneConfigV8Node(child)
	}
	return &copy
}

// Unlike JSON merge-patch, null is retained: optional key overrides use it to
// inherit the group value. DELETE is the explicit field-removal operation.
func mergeConfigV8Patch(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		if dst.Kind == yaml.ScalarNode && src.Kind == yaml.ScalarNode {
			copy := *src
			copy.HeadComment, copy.LineComment, copy.FootComment = dst.HeadComment, dst.LineComment, dst.FootComment
			*dst = copy
			return
		}
		*dst = *src
		return
	}
	for i := 0; i < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		if old := configV8Node(dst, []string{key.Value}); old != nil {
			mergeConfigV8Patch(old, value)
		} else {
			dst.Content = append(dst.Content, key, value)
		}
	}
}

// configV8IdentityKeys name the fields that identify a mapping inside a list,
// in priority order: openai-compatibility models by alias, groups by name,
// key entries by api-key.
var configV8IdentityKeys = []string{"alias", "name", "api-key", "id"}

// carryConfigV8Comments copies YAML comments from the previous document (src)
// onto nodes that a JSON write recreated without any. Mapping values match by
// key. List items match by identity (a scalar's value, or a mapping's first
// identity field) and otherwise by position when unchanged, so comments stay
// with entries that survive when a whole list is replaced. Nodes that already
// carry comments keep them.
func carryConfigV8Comments(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	if dst.HeadComment == "" && dst.LineComment == "" && dst.FootComment == "" {
		dst.HeadComment, dst.LineComment, dst.FootComment = src.HeadComment, src.LineComment, src.FootComment
	}
	if dst.Kind != src.Kind {
		return
	}
	switch dst.Kind {
	case yaml.DocumentNode:
		for i := 0; i < len(dst.Content) && i < len(src.Content); i++ {
			carryConfigV8Comments(dst.Content[i], src.Content[i])
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(dst.Content); i += 2 {
			for j := 0; j+1 < len(src.Content); j += 2 {
				if src.Content[j].Value == dst.Content[i].Value {
					carryConfigV8Comments(dst.Content[i], src.Content[j])
					carryConfigV8Comments(dst.Content[i+1], src.Content[j+1])
					break
				}
			}
		}
	case yaml.SequenceNode:
		used := make([]bool, len(src.Content))
		for i, item := range dst.Content {
			if j := matchConfigV8Item(item, i, src.Content, used); j >= 0 {
				used[j] = true
				carryConfigV8Comments(item, src.Content[j])
			}
		}
	}
}

func matchConfigV8Item(item *yaml.Node, index int, previous []*yaml.Node, used []bool) int {
	if id := configV8ItemIdentity(item); id != "" {
		for j, candidate := range previous {
			if !used[j] && configV8ItemIdentity(candidate) == id {
				return j
			}
		}
	}
	if index < len(previous) && !used[index] && configV8NodesEqual(item, previous[index]) {
		return index
	}
	return -1
}

func configV8ItemIdentity(node *yaml.Node) string {
	switch node.Kind {
	case yaml.ScalarNode:
		return "=" + node.Value
	case yaml.MappingNode:
		for _, key := range configV8IdentityKeys {
			if value := configV8Node(node, []string{key}); value != nil && value.Kind == yaml.ScalarNode && value.Value != "" {
				return key + "=" + value.Value
			}
		}
	}
	return ""
}

// configV8NodesEqual compares content, ignoring comments and quoting style.
func configV8NodesEqual(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.ScalarNode && a.ShortTag() != b.ShortTag() {
		return false
	}
	for i := range a.Content {
		if !configV8NodesEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}
