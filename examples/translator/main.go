package main

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

func main() {
	rawRequest := []byte(`{"messages":[{"content":[{"text":"Hello! Claude","type":"text"}],"role":"user"}],"model":"claude-sonnet-4-5","stream":false}`)
	fmt.Println("Has claude->openai response translator:", translator.HasResponseTransformerByFormatName(
		translator.FormatClaude,
		translator.FormatOpenAI,
	))

	translatedRequest := translator.TranslateRequestByFormatName(
		translator.FormatOpenAI,
		translator.FormatClaude,
		"claude-sonnet-4-5",
		rawRequest,
		false,
	)

	fmt.Printf("Translated request to Claude format:\n%s\n\n", translatedRequest)

	claudeResponse := []byte(`{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Hello! How can I help you today?"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":10}}`)

	convertedResponse := translator.TranslateNonStreamByFormatName(
		context.Background(),
		translator.FormatClaude,
		translator.FormatOpenAI,
		"claude-sonnet-4-5",
		rawRequest,
		translatedRequest,
		claudeResponse,
		nil,
	)

	fmt.Printf("Converted response for OpenAI clients:\n%s\n", convertedResponse)
}
