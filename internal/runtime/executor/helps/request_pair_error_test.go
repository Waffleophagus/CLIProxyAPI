package helps

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func requireUnsupportedPart(t *testing.T, label string, err error, wantType string) {
	t.Helper()
	var part *translatorcommon.UnsupportedPartError
	if !errors.As(err, &part) || part.Type != wantType || clienterror.HTTPStatusFromError(err) != http.StatusBadRequest {
		t.Fatalf("%s: err = %v, want a 400 unsupported content part: %s", label, err, wantType)
	}
	if got, want := err.Error(), "unsupported content part: "+wantType; got != want {
		t.Fatalf("%s: error text = %q, want %q", label, got, want)
	}
}

// A Responses request whose only user content is an input_file file id must be
// refused with a named 400 on the default envelope path and on the compat path.
func TestResponsesFileIDOnlyReturns400OnEveryPath(t *testing.T) {
	payload := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-1"}]}]}`)
	for _, compat := range []bool{false, true} {
		_, single := TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, false, compat)
		requireUnsupportedPart(t, "single", single, "input_file")
		_, _, pair := TranslateRequestPairReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, payload, false, compat)
		requireUnsupportedPart(t, "pair", pair, "input_file")
		_, _, intent := TranslateRequestPairWithAPIKeyModelCompatibilityChecked(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", payload, payload, false, compat)
		requireUnsupportedPart(t, "pair with update intent", intent, "input_file")
	}
}

func TestResponsesAudioAndTextBesideFileIDOnCompatPath(t *testing.T) {
	audio := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_audio","input_audio":{"data":"UklGRg==","format":"wav"}}]}]}`)
	_, err := TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", audio, false, true)
	requireUnsupportedPart(t, "audio", err, "input_audio")

	mixed := []byte(`{"model":"claude-sonnet-4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"keep me"},{"type":"input_file","file_id":"file-1"}]}]}`)
	body, err := TranslateRequestReturningError(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4", mixed, false, true)
	if err != nil {
		t.Fatalf("text beside a file id was refused: %v", err)
	}
	if got := gjson.GetBytes(body, "messages.0.content").String(); got != "keep me" {
		t.Fatalf("text was lost: %s", body)
	}
}
