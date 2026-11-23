package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7733StructMetadata struct {
	Tags   []string
	hidden string
}

type issue7733MutatingSelector struct {
	mu                sync.Mutex
	order             []string
	seenBefore        []map[string]any
	sameAttemptAlias  bool
	reslicedEmptyElem any
	pickCounter       int
}

func (s *issue7733MutatingSelector) Pick(_ context.Context, _ string, _ string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenBefore = append(s.seenBefore, cloneRequestMetadata(opts.Metadata))
	idx := s.pickCounter
	s.pickCounter++
	if idx == 0 && opts.Metadata != nil {
		opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = "attempt-1-canonical"
		opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = "attempt-1-parent"
		opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] = "attempt-1-lcp"
		opts.Metadata[cliproxyexecutor.IsForkMetadataKey] = true
		opts.Metadata[cliproxyexecutor.NodeKindMetadataKey] = "fork"
		if nested, ok := opts.Metadata["routing"].(map[string]any); ok {
			nested["node"] = "mutated-by-attempt-1"
		}
		if tags, ok := opts.Metadata["tags"].([]string); ok && len(tags) > 0 {
			tags[0] = "mutated-tag-1"
		}
		if excludedBool, ok := opts.Metadata["excluded_bool"].(map[string]bool); ok {
			excludedBool["mutated-bool"] = true
		}
		if excludedSet, ok := opts.Metadata["excluded_set"].(map[string]struct{}); ok {
			excludedSet["mutated-set"] = struct{}{}
		}
		if routingTags, ok := opts.Metadata["routing_tags"].(map[string][]string); ok {
			if list := routingTags["tags"]; len(list) > 0 {
				list[0] = "mutated-routing-tag"
			}
			routingTags["extra"] = []string{"leaked"}
		}
		if arrTags, ok := opts.Metadata["array_tags"].([1][]string); ok && len(arrTags[0]) > 0 {
			arrTags[0][0] = "mutated-array-tag"
		}
		if stTags, ok := opts.Metadata["struct_tags"].(issue7733StructMetadata); ok && len(stTags.Tags) > 0 {
			stTags.Tags[0] = "mutated-struct-tag"
		}
		if sharedA, ok := opts.Metadata["shared_bool_a"].(map[string]bool); ok {
			sharedA["within_attempt"] = true
			if sharedB, okB := opts.Metadata["shared_bool_b"].(map[string]bool); okB && sharedB["within_attempt"] {
				s.sameAttemptAlias = true
			}
		}
		if emptyBuf, ok := opts.Metadata["empty_cap_buf"].([]any); ok && len(emptyBuf) == 0 && cap(emptyBuf) == 1 {
			grown := emptyBuf[:1]
			s.reslicedEmptyElem = grown[0]
			grown[0] = "mutated-spare-slot"
		}
	}
	if idx < len(s.order) {
		targetID := s.order[idx]
		for _, candidate := range auths {
			if candidate != nil && candidate.ID == targetID {
				return candidate, nil
			}
		}
	}
	for _, candidate := range auths {
		if candidate != nil {
			return candidate, nil
		}
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

type issue7733ResultRecorderHook struct {
	NoopHook
	mu      sync.Mutex
	results []Result
}

func (h *issue7733ResultRecorderHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = append(h.results, result)
}

func (h *issue7733ResultRecorderHook) snapshot() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Result(nil), h.results...)
}

type callbackSchedulerTestExecutor struct {
	schedulerTestExecutor
	execute       func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	executeStream func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	countTokens   func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

func (e *callbackSchedulerTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.execute != nil {
		return e.execute(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{}, nil
}

func (e *callbackSchedulerTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.executeStream != nil {
		return e.executeStream(ctx, auth, req, opts)
	}
	return nil, nil
}

func (e *callbackSchedulerTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.countTokens != nil {
		return e.countTokens(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{}, nil
}

func TestIssue7733MixedLoopsIsolatePerAttemptMetadataOnFailover(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			selector := &issue7733MutatingSelector{
				order: []string{"issue7733-skip-a", "issue7733-skip-b"},
			}
			hook := &issue7733ResultRecorderHook{}
			manager := NewManager(nil, selector, hook)
			retryableErr := &Error{
				Code:       "upstream_unavailable",
				Message:    "temporary failure",
				Retryable:  true,
				HTTPStatus: http.StatusServiceUnavailable,
			}
			executor := &callbackSchedulerTestExecutor{
				schedulerTestExecutor: schedulerTestExecutor{provider: "issue7733-skip"},
				execute: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "issue7733-skip-a" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				countTokens: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "issue7733-skip-a" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
				executeStream: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if auth.ID == "issue7733-skip-a" {
						return nil, retryableErr
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			manager.RegisterExecutor(executor)
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient("issue7733-skip-a", "issue7733-skip", []*registry.ModelInfo{{ID: "route-model"}})
			reg.RegisterClient("issue7733-skip-b", "issue7733-skip", []*registry.ModelInfo{{ID: "route-model"}})
			t.Cleanup(func() {
				reg.UnregisterClient("issue7733-skip-a")
				reg.UnregisterClient("issue7733-skip-b")
			})
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-skip-a",
				Provider: "issue7733-skip",
				Status:   StatusActive,
			}); errRegister != nil {
				t.Fatalf("register failing auth: %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-skip-b",
				Provider: "issue7733-skip",
				Status:   StatusActive,
			}); errRegister != nil {
				t.Fatalf("register healthy auth: %v", errRegister)
			}

			callerRouting := map[string]any{"node": "base"}
			callerTags := []string{"base-tag"}
			callerExcludedBool := map[string]bool{"base-bool": true}
			callerExcludedSet := map[string]struct{}{"base-set": {}}
			callerRoutingTags := map[string][]string{"tags": {"base"}}
			callerArrayTags := [1][]string{{"base-array"}}
			callerStructTags := issue7733StructMetadata{Tags: []string{"base-struct"}, hidden: "keep"}
			callerSharedBool := map[string]bool{"base": true}
			callerEmptyCapBacking := []any{"spare-base"}
			callerEmptyCapBuf := callerEmptyCapBacking[:0]
			opts := cliproxyexecutor.Options{
				Stream: mode == "stream",
				Metadata: map[string]any{
					"caller_marker": "base",
					"routing":       callerRouting,
					"tags":          callerTags,
					"excluded_bool": callerExcludedBool,
					"excluded_set":  callerExcludedSet,
					"routing_tags":  callerRoutingTags,
					"array_tags":    callerArrayTags,
					"struct_tags":   callerStructTags,
					"shared_bool_a": callerSharedBool,
					"shared_bool_b": callerSharedBool,
					"empty_cap_buf": callerEmptyCapBuf,
				},
			}
			req := cliproxyexecutor.Request{Model: "route-model"}
			var errRun error
			switch mode {
			case "execute":
				_, errRun = manager.Execute(context.Background(), []string{"issue7733-skip"}, req, opts)
			case "count":
				_, errRun = manager.ExecuteCount(context.Background(), []string{"issue7733-skip"}, req, opts)
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, errRun = manager.ExecuteStream(context.Background(), []string{"issue7733-skip"}, req, opts)
				if errRun == nil {
					for range stream.Chunks {
					}
				}
			}
			if errRun != nil {
				t.Fatalf("%s failed: %v", mode, errRun)
			}
			if len(selector.seenBefore) != 2 {
				t.Fatalf("selector pick count = %d, want 2", len(selector.seenBefore))
			}
			secondMeta := selector.seenBefore[1]
			if secondMeta["caller_marker"] != "base" {
				t.Fatalf("second attempt lost base metadata: %#v", secondMeta)
			}
			if routing, ok := secondMeta["routing"].(map[string]any); !ok || routing["node"] != "base" {
				t.Fatalf("second attempt observed mutated nested map: %#v", secondMeta["routing"])
			}
			if tags, ok := secondMeta["tags"].([]string); !ok || len(tags) != 1 || tags[0] != "base-tag" {
				t.Fatalf("second attempt observed mutated slice: %#v", secondMeta["tags"])
			}
			if callerRouting["node"] != "base" {
				t.Fatalf("caller nested map was mutated: %#v", callerRouting)
			}
			if callerTags[0] != "base-tag" {
				t.Fatalf("caller slice was mutated: %#v", callerTags)
			}
			if excludedBool, ok := secondMeta["excluded_bool"].(map[string]bool); !ok || len(excludedBool) != 1 || !excludedBool["base-bool"] {
				t.Fatalf("second attempt observed mutated map[string]bool: %#v", secondMeta["excluded_bool"])
			}
			if excludedSet, ok := secondMeta["excluded_set"].(map[string]struct{}); !ok || len(excludedSet) != 1 {
				t.Fatalf("second attempt observed mutated map[string]struct{}: %#v", secondMeta["excluded_set"])
			}
			if _, mutated := callerExcludedBool["mutated-bool"]; mutated {
				t.Fatalf("caller map[string]bool was mutated: %#v", callerExcludedBool)
			}
			if _, mutated := callerExcludedSet["mutated-set"]; mutated {
				t.Fatalf("caller map[string]struct{} was mutated: %#v", callerExcludedSet)
			}
			if routingTags, ok := secondMeta["routing_tags"].(map[string][]string); !ok || len(routingTags) != 1 || len(routingTags["tags"]) != 1 || routingTags["tags"][0] != "base" {
				t.Fatalf("second attempt observed mutated map[string][]string: %#v", secondMeta["routing_tags"])
			}
			if len(callerRoutingTags) != 1 || len(callerRoutingTags["tags"]) != 1 || callerRoutingTags["tags"][0] != "base" {
				t.Fatalf("caller map[string][]string was mutated: %#v", callerRoutingTags)
			}
			if arrTags, ok := secondMeta["array_tags"].([1][]string); !ok || len(arrTags[0]) != 1 || arrTags[0][0] != "base-array" {
				t.Fatalf("second attempt observed mutated [1][]string: %#v", secondMeta["array_tags"])
			}
			if len(callerArrayTags[0]) != 1 || callerArrayTags[0][0] != "base-array" {
				t.Fatalf("caller [1][]string was mutated: %#v", callerArrayTags)
			}
			if stTags, ok := secondMeta["struct_tags"].(issue7733StructMetadata); !ok || len(stTags.Tags) != 1 || stTags.Tags[0] != "base-struct" || stTags.hidden != "keep" {
				t.Fatalf("second attempt observed mutated struct: %#v", secondMeta["struct_tags"])
			}
			if len(callerStructTags.Tags) != 1 || callerStructTags.Tags[0] != "base-struct" {
				t.Fatalf("caller struct slice was mutated: %#v", callerStructTags)
			}
			if !selector.sameAttemptAlias {
				t.Fatalf("shared map[string]bool keys lost intra-attempt aliasing after clone")
			}
			if _, mutated := callerSharedBool["within_attempt"]; mutated {
				t.Fatalf("caller shared map[string]bool was mutated: %#v", callerSharedBool)
			}
			if secondShared, ok := secondMeta["shared_bool_b"].(map[string]bool); !ok || secondShared["within_attempt"] || !secondShared["base"] {
				t.Fatalf("second attempt observed mutated shared map[string]bool: %#v", secondMeta["shared_bool_b"])
			}
			if selector.reslicedEmptyElem != "spare-base" {
				t.Fatalf("first attempt empty-len non-zero-cap reslice = %#v, want spare-base", selector.reslicedEmptyElem)
			}
			if callerEmptyCapBacking[0] != "spare-base" {
				t.Fatalf("caller spare slice capacity element was mutated: %#v", callerEmptyCapBacking)
			}
			if secondBuf, ok := secondMeta["empty_cap_buf"].([]any); !ok || len(secondBuf) != 0 || cap(secondBuf) != 1 || secondBuf[:1][0] != "spare-base" {
				t.Fatalf("second attempt empty_cap_buf = %#v (len=%d cap=%d), want len=0 cap=1 spare-base", secondMeta["empty_cap_buf"], len(secondBuf), cap(secondBuf))
			}
			results := hook.snapshot()
			if len(results) != 2 {
				t.Fatalf("recorded results = %d, want 2", len(results))
			}
			if got := results[0].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "issue7733-skip-a" {
				t.Fatalf("first result selected auth metadata = %#v, want issue7733-skip-a", got)
			}
			if got := results[1].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "issue7733-skip-b" {
				t.Fatalf("second result selected auth metadata = %#v, want issue7733-skip-b", got)
			}
			for _, leakedKey := range []string{
				cliproxyexecutor.SelectedAuthMetadataKey,
				cliproxyexecutor.SelectedAuthIndexMetadataKey,
				cliproxyexecutor.CanonicalSessionIDMetadataKey,
				cliproxyexecutor.ParentSessionIDMetadataKey,
				cliproxyexecutor.LCPAffinitySessionIDMetadataKey,
				cliproxyexecutor.IsForkMetadataKey,
				cliproxyexecutor.NodeKindMetadataKey,
			} {
				if val, exists := secondMeta[leakedKey]; exists {
					t.Fatalf("second attempt inherited mutated metadata key %q = %#v from failed attempt", leakedKey, val)
				}
			}
		})
	}
}

func TestIssue7733CloneRequestMetadataHandlesCycles(t *testing.T) {
	root := map[string]any{"label": "root"}
	nestedMap := map[string]any{"name": "nested"}
	nestedMap["self"] = nestedMap
	nestedMap["parent"] = root
	cyclicSlice := make([]any, 2)
	cyclicSlice[0] = "item"
	cyclicSlice[1] = cyclicSlice
	root["self"] = root
	root["nested"] = nestedMap
	root["slice"] = cyclicSlice

	cloned := cloneRequestMetadata(root)
	if cloned["label"] != "root" {
		t.Fatalf("cloned label = %#v, want root", cloned["label"])
	}
	clonedSelf, ok := cloned["self"].(map[string]any)
	if !ok {
		t.Fatalf("cloned self type = %T, want map[string]any", cloned["self"])
	}
	clonedSelf["label"] = "mutated-clone"
	if root["label"] != "root" {
		t.Fatalf("mutating cyclic clone mutated original root map: %#v", root["label"])
	}
	if cloned["label"] != "mutated-clone" {
		t.Fatalf("cloned self reference did not point to cloned root")
	}

	clonedNested, ok := cloned["nested"].(map[string]any)
	if !ok {
		t.Fatalf("cloned nested type = %T, want map[string]any", cloned["nested"])
	}
	clonedNestedSelf, ok := clonedNested["self"].(map[string]any)
	if !ok {
		t.Fatalf("cloned nested self type = %T, want map[string]any", clonedNested["self"])
	}
	clonedNestedSelf["name"] = "mutated-nested"
	if nestedMap["name"] != "nested" {
		t.Fatalf("mutating cyclic nested clone mutated original nested map: %#v", nestedMap["name"])
	}
	if clonedNested["name"] != "mutated-nested" {
		t.Fatalf("cloned nested self reference did not point to cloned nested map")
	}

	clonedSlice, ok := cloned["slice"].([]any)
	if !ok || len(clonedSlice) != 2 {
		t.Fatalf("cloned slice = %#v, want len 2 []any", cloned["slice"])
	}
	clonedSliceSelf, ok := clonedSlice[1].([]any)
	if !ok || len(clonedSliceSelf) != 2 {
		t.Fatalf("cloned cyclic slice reference = %#v, want len 2 []any", clonedSlice[1])
	}
	clonedSliceSelf[0] = "mutated-item"
	if cyclicSlice[0] != "item" {
		t.Fatalf("mutating cyclic slice clone mutated original slice: %#v", cyclicSlice[0])
	}
	if clonedSlice[0] != "mutated-item" {
		t.Fatalf("cloned slice self reference did not point to cloned slice")
	}

	type cyclicTypedMap map[string]any
	typedCyclic := cyclicTypedMap{"k": "v"}
	typedCyclic["self"] = typedCyclic
	rootWithTyped := map[string]any{"typed": typedCyclic}
	clonedTypedRoot := cloneRequestMetadata(rootWithTyped)
	clonedTyped, ok := clonedTypedRoot["typed"].(cyclicTypedMap)
	if !ok {
		t.Fatalf("cloned typed cyclic map type = %T, want cyclicTypedMap", clonedTypedRoot["typed"])
	}
	clonedTyped["k"] = "changed"
	if typedCyclic["k"] != "v" {
		t.Fatalf("mutating cloned typed cyclic map mutated original: %#v", typedCyclic["k"])
	}
	if selfRef, okSelf := clonedTyped["self"].(cyclicTypedMap); !okSelf || selfRef["k"] != "changed" {
		t.Fatalf("cloned typed cyclic self reference broken: %#v", clonedTyped["self"])
	}

	rootTyped := cyclicTypedMap{"label": "root"}
	rootTyped["self"] = rootTyped
	clonedNamedRoot := cloneRequestMetadata(rootTyped)
	clonedNamedSelf, okNamed := clonedNamedRoot["self"].(cyclicTypedMap)
	if !okNamed {
		t.Fatalf("cloned named root self type = %T, want cyclicTypedMap", clonedNamedRoot["self"])
	}
	clonedNamedSelf["label"] = "mutated-named-root"
	if rootTyped["label"] != "root" {
		t.Fatalf("mutating cloned named root self mutated original: %#v", rootTyped["label"])
	}
	if clonedNamedRoot["label"] != "mutated-named-root" {
		t.Fatalf("cloned named root self did not alias cloned root: %#v", clonedNamedRoot["label"])
	}

	type cyclicTypedSlice []any
	namedSlice := make(cyclicTypedSlice, 2)
	namedSlice[0] = []any(namedSlice)
	namedSlice[1] = "orig"
	clonedNamedSliceRoot := cloneRequestMetadata(map[string]any{"s": namedSlice})
	clonedNamedSlice, okSlice := clonedNamedSliceRoot["s"].(cyclicTypedSlice)
	if !okSlice || len(clonedNamedSlice) != 2 {
		t.Fatalf("cloned named slice = %#v, want len 2 cyclicTypedSlice", clonedNamedSliceRoot["s"])
	}
	convertedView, okView := clonedNamedSlice[0].([]any)
	if !okView || len(convertedView) != 2 {
		t.Fatalf("cloned converted slice view = %#v, want len 2 []any", clonedNamedSlice[0])
	}
	convertedView[1] = "mutated-view"
	if namedSlice[1] != "orig" {
		t.Fatalf("mutating cloned converted slice view mutated original: %#v", namedSlice[1])
	}
	if clonedNamedSlice[1] != "mutated-view" {
		t.Fatalf("cloned converted slice view did not alias cloned named slice: %#v", clonedNamedSlice[1])
	}
}
