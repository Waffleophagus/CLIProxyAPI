package helps

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestClaudeAdvisorOwnerStore_FirstWriterWinsSlidingTTLAndBounds(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	store := NewClaudeAdvisorOwnerStore()
	store.nowFunc = func() time.Time { return now }

	// First writer wins while entry is still live.
	store.Remember("claude", "srvtoolu_1", "id:cred-a")
	store.Remember("claude", "srvtoolu_1", "id:cred-b")
	owner, ok := store.Lookup("claude", "srvtoolu_1")
	if !ok || owner != "id:cred-a" {
		t.Fatalf("Lookup() = (%q, %v), want (id:cred-a, true)", owner, ok)
	}

	// Scope isolation.
	if foreignOwner, found := store.Lookup("other-provider", "srvtoolu_1"); found {
		t.Fatalf("Lookup() crossed scopes: (%q, %v)", foreignOwner, found)
	}

	// Active session sliding TTL: hitting Lookup at +20h keeps the entry alive past
	// the original 24h mark (at +30h total).
	now = now.Add(20 * time.Hour)
	owner, ok = store.Lookup("claude", "srvtoolu_1")
	if !ok || owner != "id:cred-a" {
		t.Fatalf("active Lookup() at +20h = (%q, %v), want (id:cred-a, true)", owner, ok)
	}
	now = now.Add(10 * time.Hour) // 30h since initial registration, 10h since last hit
	owner, ok = store.Lookup("claude", "srvtoolu_1")
	if !ok || owner != "id:cred-a" {
		t.Fatalf("active Lookup() at +30h (10h idle) = (%q, %v), want (id:cred-a, true)", owner, ok)
	}

	// Idle entry expires once untouched for >24h.
	now = now.Add(ClaudeAdvisorOwnerTTL + time.Second)
	if expiredOwner, found := store.Lookup("claude", "srvtoolu_1"); found {
		t.Fatalf("expired entry remained live: (%q, %v)", expiredOwner, found)
	}

	// Re-inserting an expired key keeps order length accurate.
	store.Remember("claude", "srvtoolu_1", "id:cred-b")
	owner, ok = store.Lookup("claude", "srvtoolu_1")
	if !ok || owner != "id:cred-b" {
		t.Fatalf("refreshed Lookup() = (%q, %v), want (id:cred-b, true)", owner, ok)
	}
	if store.order.Len() != 1 {
		t.Fatalf("order len = %d after refresh, want 1", store.order.Len())
	}

	// Bounded LRU eviction: exceeding claudeAdvisorOwnerLimit evicts the oldest idle entry.
	for i := 0; i <= claudeAdvisorOwnerLimit; i++ {
		store.Remember("claude", "srvtoolu_bound_"+strconv.Itoa(i), "id:cred-a")
	}
	if _, found := store.Lookup("claude", "srvtoolu_1"); found {
		t.Fatal("oldest entry was not evicted when exceeding store limit")
	}
	if got, found := store.Lookup("claude", "srvtoolu_bound_"+strconv.Itoa(claudeAdvisorOwnerLimit)); !found || got != "id:cred-a" {
		t.Fatalf("newest entry lost: (%q, %v)", got, found)
	}
}

func TestStripForeignAdvisorBlocks_EdgeCases(t *testing.T) {
	store := NewClaudeAdvisorOwnerStore()
	store.Remember("claude", "toolu_ord", "id:cred-a")
	store.Remember("claude", "srvtoolu_obj", "id:cred-a")
	store.Remember("claude", "srvtoolu_mixed_srv", "id:cred-a")

	doc := []byte(`{
		"messages": [
			{"role": "user", "content": "hi"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_ord", "name": "normal_tool", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_ord",
						"content": [
							{"type": "advisor_redacted_result", "encrypted_content": "enc_ord"}
						]
					}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_mixed_srv", "name": "advisor", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "srvtoolu_mixed_srv",
						"content": [
							{"type": "text", "text": "wrapper text"},
							{"type": "advisor_redacted_result", "encrypted_content": "enc_mixed_srv"}
						]
					}
				]
			},
			{
				"role": "user",
				"content": {
					"type": "advisor_tool_result",
					"tool_use_id": "srvtoolu_obj",
					"content": {"type": "advisor_redacted_result", "encrypted_content": "enc_obj"}
				}
			},
			{"role": "user", "content": "final"}
		]
	}`)

	doc, _, stripped := store.StripForeignAdvisorBlocks(doc, "claude", "id:cred-b")
	if stripped != 4 {
		t.Fatalf("stripped = %d, want 4", stripped)
	}
	out := string(doc)
	if strings.Contains(out, "enc_ord") || strings.Contains(out, "enc_obj") || strings.Contains(out, "srvtoolu_obj") || strings.Contains(out, "srvtoolu_mixed_srv") || strings.Contains(out, "enc_mixed_srv") {
		t.Fatalf("foreign advisor content or dangling server_tool_use tool_result remained: %s", out)
	}
	// Ordinary tool_use must keep its paired tool_result with cleared content instead of orphaning it.
	if got := gjson.GetBytes(doc, "messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.2.content.0.type = %q, want tool_result: %s", got, out)
	}
	if got := gjson.GetBytes(doc, "messages.2.content.0.content").String(); got != "" {
		t.Fatalf("messages.2.content.0.content = %q, want empty string: %s", got, out)
	}
	// Both advisor-only turns (server_tool_use + mixed tool_result for dropped server_tool_use)
	// and the object-content advisor message must be dropped completely.
	if got := gjson.GetBytes(doc, "messages.#").Int(); got != 4 {
		t.Fatalf("messages count = %d, want 4: %s", got, out)
	}
}
