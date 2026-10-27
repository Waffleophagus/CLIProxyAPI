package handlers

import (
	"context"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// A chunk channel closed together with a canceled context must always report cancellation,
// never a clean EOF, even though select picks a ready case at random.
func TestNextStreamChunkClosedChannelWithCanceledContextReportsCancel(t *testing.T) {
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		chunks := make(chan coreexecutor.StreamChunk)
		close(chunks)
		closed := false
		if _, ok, canceled := nextStreamChunk(ctx, &closed, chunks); ok || !canceled {
			t.Fatalf("nextStreamChunk() ok=%v canceled=%v, want ok=false canceled=true", ok, canceled)
		}
	}
}

func TestNextStreamChunkClosedChannelWithLiveContextReportsEOF(t *testing.T) {
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	closed := false
	if _, ok, canceled := nextStreamChunk(context.Background(), &closed, chunks); ok || canceled {
		t.Fatalf("nextStreamChunk() ok=%v canceled=%v, want ok=false canceled=false", ok, canceled)
	}
	if !closed {
		t.Fatal("closed = false, want true after EOF")
	}
}
