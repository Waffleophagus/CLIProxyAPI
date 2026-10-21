package cache

import (
	"bytes"
	"context"
	"testing"
)

func TestKimiThinkingReplayConditionalDeleteKeepsNewerContent(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "k3"
	const sessionKey = "execution:conditional-delete"
	oldContent := []byte(`[{"type":"thinking","signature":"old"}]`)
	newContent := []byte(`[{"type":"thinking","signature":"new"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, oldContent) {
		t.Fatal("failed to seed old content")
	}
	_, snapshot, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found {
		t.Fatalf("GetKimiThinkingReplayWithSnapshotRequired() = found %v, error %v", found, errGet)
	}
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, newContent) {
		t.Fatal("failed to write newer content")
	}
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, oldContent) {
		t.Fatal("failed to write latest content with repeated bytes")
	}

	deleted, errDelete := DeleteKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshot)
	if errDelete != nil {
		t.Fatalf("DeleteKimiThinkingReplayIfUnchanged() error = %v", errDelete)
	}
	if deleted {
		t.Fatal("stale snapshot deleted newer content")
	}
	got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || !bytes.Equal(got, oldContent) {
		t.Fatalf("cached content = %s, found %v, error %v; want latest repeated content", got, found, errGet)
	}
}

func TestKimiThinkingReplayConditionalReplaceKeepsConcurrentContent(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "k3"
	const sessionKey = "execution:conditional-replace"
	_, snapshot, found, errGet := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || found {
		t.Fatalf("initial cache read = found %v, error %v; want miss", found, errGet)
	}
	newContent := []byte(`[{"type":"thinking","signature":"new"}]`)
	staleContent := []byte(`[{"type":"thinking","signature":"stale"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), modelFamily, sessionKey, newContent) {
		t.Fatal("failed to write concurrent content")
	}

	replaced, errReplace := ReplaceKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snapshot, staleContent)
	if errReplace != nil {
		t.Fatalf("ReplaceKimiThinkingReplayIfUnchanged() error = %v", errReplace)
	}
	if replaced {
		t.Fatal("stale snapshot replaced concurrent content")
	}
	got, found, errGet := GetKimiThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if errGet != nil || !found || !bytes.Equal(got, newContent) {
		t.Fatalf("cached content = %s, found %v, error %v; want concurrent content", got, found, errGet)
	}
}

func TestKimiThinkingReplayTombstoneFencesConcurrentMiss(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	const modelFamily = "k3"
	const sessionKey = "execution:tombstone-fence"
	_, firstSnapshot, firstFound, errFirst := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	_, secondSnapshot, secondFound, errSecond := GetKimiThinkingReplayWithSnapshotRequired(context.Background(), modelFamily, sessionKey)
	if errFirst != nil || errSecond != nil || firstFound || secondFound {
		t.Fatalf("concurrent misses = %v/%v, errors %v/%v", firstFound, secondFound, errFirst, errSecond)
	}
	deleted, errDelete := DeleteKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, firstSnapshot)
	if errDelete != nil || !deleted {
		t.Fatalf("first miss delete = %v, error %v", deleted, errDelete)
	}
	staleContent := []byte(`[{"type":"thinking","signature":"stale"}]`)
	replaced, errReplace := ReplaceKimiThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, secondSnapshot, staleContent)
	if errReplace != nil {
		t.Fatalf("stale miss replace error = %v", errReplace)
	}
	if replaced {
		t.Fatal("stale miss snapshot crossed a newer tombstone")
	}
}

func TestKimiThinkingReplayTracksAggregateLocalBytes(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	first := []byte(`[{"type":"thinking","signature":"first"}]`)
	second := []byte(`[{"type":"thinking","signature":"second"}]`)
	if !CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:bytes-1", first) ||
		!CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:bytes-2", second) {
		t.Fatal("failed to seed aggregate byte accounting")
	}
	if got, want := kimiThinkingReplayTotalBytes, len(first)+len(second); got != want {
		t.Fatalf("aggregate bytes = %d, want %d", got, want)
	}
	if errDelete := DeleteKimiThinkingReplayRequired(context.Background(), "k3", "execution:bytes-1"); errDelete != nil {
		t.Fatalf("DeleteKimiThinkingReplayRequired() error = %v", errDelete)
	}
	if got, want := kimiThinkingReplayTotalBytes, len(second); got != want {
		t.Fatalf("aggregate bytes after delete = %d, want %d", got, want)
	}
	ClearKimiThinkingReplayCache()
	if kimiThinkingReplayTotalBytes != 0 {
		t.Fatalf("aggregate bytes after clear = %d, want 0", kimiThinkingReplayTotalBytes)
	}
}

func TestKimiThinkingReplayRejectsOversizedContent(t *testing.T) {
	ClearKimiThinkingReplayCache()
	t.Cleanup(ClearKimiThinkingReplayCache)

	content := make([]byte, KimiThinkingReplayCacheMaxBytesPerEntry+1)
	content[0] = '['
	for i := 1; i < len(content)-1; i++ {
		content[i] = ' '
	}
	content[len(content)-1] = ']'
	if CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:oversized", content) {
		t.Fatal("oversized content was cached")
	}
}
