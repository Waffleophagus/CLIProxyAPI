package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"golang.org/x/net/context"
)

// ExecuteWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, false)
}

// ExecuteImageWithAuthManager executes an OpenAI-compatible image endpoint request.
func (h *BaseAPIHandler) ExecuteImageWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, true)
}

func (h *BaseAPIHandler) executeWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManagerFormats(ctx, handlerType, handlerType, modelName, rawJSON, alt, allowImageModel, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeWithAuthManagerFormats(ctx context.Context, entryProtocol, exitProtocol, modelName string, rawJSON []byte, alt string, allowImageModel bool, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	originalRequestedModel := modelName
	// Speech-only models are reachable solely through the speech entry protocol.
	execOptions.AllowSpeechModel = isModelExecutionSpeechProtocol(entryProtocol)
	responseProtocol := modelExecutionResponseProtocol(entryProtocol, exitProtocol)
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, originalRequestedModel, allowImageModel, execOptions)
	if errMsg != nil {
		return nil, nil, errMsg
	}
	reqMeta := requestExecutionMetadata(ctx)
	if execOptions.Path != "" {
		reqMeta[coreexecutor.RequestPathMetadataKey] = execOptions.Path
	}
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	setReasoningEffortMetadata(reqMeta, entryProtocol, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	originalRequest := rawJSON
	if ctxOriginal := originalRequestFromContext(ctx); len(ctxOriginal) > 0 {
		originalRequest = ctxOriginal
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: originalRequest,
		SourceFormat:    sdktranslator.FromString(entryProtocol),
		ResponseFormat:  sdktranslator.FromString(responseProtocol),
		Headers:         modelExecutionHeaders(ctx, execOptions.Headers),
		Query:           modelExecutionQuery(ctx, execOptions.Query),
	}
	opts.Metadata = reqMeta
	ctx = enrichContextWithSessionHierarchy(ctx, opts.Headers, req.Payload, opts.Metadata)
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		return nil, nil, executionErrorMessage(err)
	}
	passthroughHeaders := PassthroughHeadersEnabled(h.CurrentConfig())
	responseHeaders := downstreamHeadersFromExecutor(cloneHeader(resp.Headers), passthroughHeaders)
	return resp.Payload, responseHeaders, nil
}

// ExecuteCountWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeCountWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	originalRequestedModel := modelName
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, originalRequestedModel, false, execOptions)
	if errMsg != nil {
		return nil, nil, errMsg
	}
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	setReasoningEffortMetadata(reqMeta, handlerType, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(handlerType),
		Headers:         modelExecutionHeaders(ctx, execOptions.Headers),
		Query:           modelExecutionQuery(ctx, execOptions.Query),
	}
	opts.Metadata = reqMeta
	ctx = enrichContextWithSessionHierarchy(ctx, opts.Headers, req.Payload, opts.Metadata)
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		return nil, nil, executionErrorMessage(err)
	}
	passthroughHeaders := PassthroughHeadersEnabled(h.CurrentConfig())
	responseHeaders := downstreamHeadersFromExecutor(cloneHeader(resp.Headers), passthroughHeaders)
	return resp.Payload, responseHeaders, nil
}

func ExecutionErrorMessage(err error) *interfaces.ErrorMessage {
	return executionErrorMessage(err)
}

func stripOverlappingHeaders(addon, primary http.Header) http.Header {
	if len(addon) == 0 {
		return nil
	}
	if len(primary) == 0 {
		return addon
	}
	for primaryKey := range primary {
		addon.Del(primaryKey)
		for addonKey := range addon {
			if strings.EqualFold(addonKey, primaryKey) {
				delete(addon, addonKey)
			}
		}
	}
	if len(addon) == 0 {
		return nil
	}
	return addon
}

func executionErrorMessage(err error) *interfaces.ErrorMessage {
	var addon http.Header
	var he interface{ Headers() http.Header }
	if errors.As(err, &he) && he != nil {
		if hdr := he.Headers(); hdr != nil {
			addon = hdr.Clone()
		}
	}
	var terminated *coreexecutor.RequestTerminatedError
	if errors.As(err, &terminated) && terminated != nil {
		termHeaders := terminated.ResponseHeaders()
		return &interfaces.ErrorMessage{
			StatusCode:     normalizedTerminationStatus(terminated.StatusCode()),
			Error:          err,
			DirectResponse: true,
			Body:           terminated.ResponseBody(),
			Headers:        termHeaders,
			Addon:          stripOverlappingHeaders(addon, termHeaders),
		}
	}
	status := http.StatusInternalServerError
	if code := clienterror.HTTPStatusFromError(err); code > 0 {
		status = code
	}
	type directResponseError interface {
		DirectResponse() bool
		ResponseBody() []byte
	}
	var direct directResponseError
	if errors.As(err, &direct) && direct != nil && direct.DirectResponse() {
		body := direct.ResponseBody()
		var headers http.Header
		if len(body) > 0 {
			contentType := http.DetectContentType(body)
			if json.Valid(body) {
				contentType = "application/json"
			}
			headers = http.Header{"Content-Type": []string{contentType}}
		}
		return &interfaces.ErrorMessage{
			StatusCode:     status,
			Error:          err,
			DirectResponse: true,
			Body:           body,
			Headers:        headers,
			Addon:          stripOverlappingHeaders(addon, headers),
		}
	}
	return &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
}
