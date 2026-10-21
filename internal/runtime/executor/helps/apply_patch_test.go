package helps

import (
	"bytes"
	"context"
	"errors"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestApplyPatchTranslationError(t *testing.T) {
	state := &common.ApplyPatchErrorState{}
	want := errors.New("invalid wrapper")
	state.SetToolInputError(want)
	if got := ApplyPatchTranslationError(state); got != want {
		t.Fatalf("translation error = %v, want %v", got, want)
	}
	if ApplyPatchTranslationError(struct{}{}) != nil || ApplyPatchTranslationError(nil) != nil {
		t.Fatal("unrelated translator state was rejected")
	}
}

func TestApplyPatchTokenUsageHelperRetainsFailureState(t *testing.T) {
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	var param any
	tokens := NewClaudeInputTokenState(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, original)
	chunks := TranslateStreamWithClaudeInputTokens(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, "model", original, original, []byte(`data: {"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"apply_patch","arguments":"{\"input\":7}"}}]}}]}`), &param, tokens)
	if ApplyPatchTranslationError(param) == nil {
		t.Fatal("token helper discarded the same retained error state")
	}
	failed := 0
	for _, chunk := range chunks {
		if bytes.Contains(chunk, []byte(`"type":"response.failed"`)) {
			failed++
			if bytes.Contains(chunk, []byte(`"usage"`)) {
				t.Fatal("usage normalization altered a failure frame")
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failure frames=%d", failed)
	}
}

func TestApplyPatchInteractionsOnlyValidSourceDoneClosesTransport(t *testing.T) {
	var param any
	translate := func(payload string) [][]byte {
		return sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatInteractions, sdktranslator.FormatOpenAIResponse, "model", nil, nil, []byte(payload), &param)
	}
	_ = translate(`{"event_type":"interaction.completed","interaction":{"id":"r"}}`)
	if chunks := translate(`{"event_type":"done",`); len(chunks) != 0 {
		t.Fatalf("malformed post-terminal event was accepted as a sentinel: %s", chunks)
	}
	chunks := translate("[DONE]")
	if len(chunks) != 1 || !bytes.Equal(chunks[0], []byte("data: [DONE]")) {
		t.Fatalf("first real source sentinel was swallowed: %s", chunks)
	}
	if len(translate("[DONE]")) != 0 || len(translate(`{"event_type":"done"}`)) != 0 {
		t.Fatal("duplicate source sentinel was forwarded")
	}
}
