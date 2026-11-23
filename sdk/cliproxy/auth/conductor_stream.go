package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func discardStreamChunks(ch <-chan cliproxyexecutor.StreamChunk) {
	if ch == nil {
		return
	}
	go func() {
		for range ch {
		}
	}()
}

type streamBootstrapError struct {
	cause   error
	headers http.Header
	// attemptID identifies the upstream attempt marker this wrapper was
	// built from, so syncPreferredUpstreamError can tell a retry hint
	// refreshed for the same attempt apart from one raised by a different
	// credential attempt that surfaced an equal error value.
	attemptID uint64
}

func cloneHTTPHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}

func newStreamBootstrapError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	upstreamAttempt := hasUpstreamExecutionAttempt(err)
	var marked *upstreamExecutionAttemptError
	attemptID := uint64(0)
	if errors.As(err, &marked) && marked != nil {
		attemptID = marked.attemptID
	}
	err = unwrapUpstreamExecutionAttempt(err)
	if existing, ok := err.(*streamBootstrapError); ok && existing != nil {
		// Already wrapped: refresh the captured headers when the caller
		// supplies a newer set, but never nest another wrapper.
		if headers != nil {
			existing.headers = cloneHTTPHeader(headers)
		}
		if upstreamAttempt {
			return markUpstreamExecutionAttemptWithID(existing, attemptID)
		}
		return existing
	}
	bootstrapErr := &streamBootstrapError{
		cause:     err,
		headers:   cloneHTTPHeader(headers),
		attemptID: attemptID,
	}
	if upstreamAttempt {
		return markUpstreamExecutionAttemptWithID(bootstrapErr, attemptID)
	}
	return bootstrapErr
}

func (e *streamBootstrapError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamBootstrapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamBootstrapError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

// streamHeaderError attaches upstream response headers to an error that must be
// returned directly (for example a request-fault bootstrap failure) without
// becoming a streamBootstrapError. It exposes Headers() so the error path can
// forward upstream diagnostic headers, while Unwrap preserves the original
// error classification and status.
type streamHeaderError struct {
	cause   error
	headers http.Header
}

func (e *streamHeaderError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamHeaderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamHeaderError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

func newStreamHeaderError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	if headers == nil {
		return err
	}
	if existing, ok := err.(*streamHeaderError); ok && existing != nil {
		return &streamHeaderError{cause: existing.cause, headers: cloneHTTPHeader(headers)}
	}
	return &streamHeaderError{cause: err, headers: cloneHTTPHeader(headers)}
}

func streamErrorResult(headers http.Header, err error) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: err}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneHTTPHeader(headers),
		Chunks:  ch,
	}
}

func validateStreamResult(result *cliproxyexecutor.StreamResult, err error) (*cliproxyexecutor.StreamResult, error) {
	if err != nil {
		return result, err
	}
	if result == nil || result.Chunks == nil {
		return result, &Error{Code: "empty_stream", Message: "upstream stream has no source", Retryable: true}
	}
	return result, nil
}

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) ([]cliproxyexecutor.StreamChunk, bool, error) {
	if ch == nil {
		return nil, true, nil
	}
	for {
		var (
			chunk cliproxyexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			// Prefer a chunk that is already available so an upstream error delivered together
			// with a context cancellation is not discarded.
			select {
			case chunk, ok = <-ch:
			default:
				select {
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case chunk, ok = <-ch:
				}
			}
		} else {
			chunk, ok = <-ch
		}
		if !ok {
			return nil, true, nil
		}
		if chunk.Err != nil {
			return nil, false, chunk.Err
		}
		if len(chunk.Payload) == 0 {
			continue
		}
		return []cliproxyexecutor.StreamChunk{chunk}, false, nil
	}
}

// recordCanceledUpstreamCooldown keeps an upstream-confirmed cooldown (an error carrying
// RetryAfter) when the request context was canceled while the executor was returning, for
// example because the downstream connection was closed in response to the same upstream error.
// Errors without RetryAfter, such as plain client cancellations, are not recorded.
func (m *Manager) recordCanceledUpstreamCooldown(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, opts cliproxyexecutor.Options, err error, executionSeq uint64) {
	m.recordCanceledCooldown(ctx, auth, provider, resultModel, routeModel, opts, err, executionSeq, false)
}

// recordCanceledCountTokensCooldown is recordCanceledUpstreamCooldown for
// count_tokens: like every count result, it must not replace the quota
// snapshot taken from generation responses.
func (m *Manager) recordCanceledCountTokensCooldown(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, opts cliproxyexecutor.Options, err error, executionSeq uint64) {
	m.recordCanceledCooldown(ctx, auth, provider, resultModel, routeModel, opts, err, executionSeq, true)
}

func (m *Manager) recordCanceledCooldown(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, opts cliproxyexecutor.Options, err error, executionSeq uint64, skipQuotaObservation bool) {
	retryAfter := retryAfterFromError(err)
	if m == nil || auth == nil || retryAfter == nil {
		return
	}
	action, okAction := matchRequestScopedErrorAction(auth, err, m.runtimeConfigSnapshot())
	result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(err), Options: opts, RetryAfter: retryAfter, SkipQuotaObservation: skipQuotaObservation, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
	result.CredentialScope = isCredentialScopedError(err)
	applyRequestScopedActionToResult(action, okAction, &result)
	m.recordExecutionResult(context.WithoutCancel(ctx), result, auth)
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, opts cliproxyexecutor.Options, executionSeq uint64) *cliproxyexecutor.StreamResult {
	return m.wrapStreamResultWithCancel(ctx, auth, provider, resultModel, routeModel, headers, buffered, remaining, aliasResult, opts, executionSeq, nil)
}

// wrapStreamResultWithCancel forwards an attempt's stream and owns cancelAttempt:
// the attempt context is canceled once the stream finishes, fails, or the
// downstream goes away, so the upstream connection never outlives the wrapper.
func (m *Manager) wrapStreamResultWithCancel(ctx context.Context, auth *Auth, provider, resultModel, routeModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, aliasResult OAuthModelAliasResult, opts cliproxyexecutor.Options, executionSeq uint64, cancelAttempt context.CancelFunc) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk)
	streamStart := time.Now()
	go func() {
		defer close(out)
		if cancelAttempt != nil {
			defer cancelAttempt()
		}
		var failed bool
		forward := true
		var rewriter *StreamRewriter
		if aliasResult.ForceMapping && strings.TrimSpace(aliasResult.OriginalAlias) != "" {
			rewriter = NewStreamRewriter(StreamRewriteOptions{RewriteModel: aliasResult.OriginalAlias})
		}
		emit := func(chunk cliproxyexecutor.StreamChunk) bool {
			if chunk.Err != nil && !failed {
				if ctx != nil && ctx.Err() != nil {
					if retryAfterFromError(chunk.Err) != nil {
						m.recordCanceledUpstreamCooldown(ctx, auth, provider, resultModel, routeModel, opts, chunk.Err, executionSeq)
						failed = true
						return false
					}
					if !isRequestScopedError(chunk.Err) {
						failed = true
						return false
					}
				}
				failed = true
				entry := logEntryWithRequestID(ctx)
				warnLogUpstreamFailure(ctx, entry, provider, resultModel, auth, time.Since(streamStart), chunk.Err)
				rerr := resultErrorFromError(chunk.Err)
				action, okAction := matchRequestScopedErrorAction(auth, chunk.Err, m.runtimeConfigSnapshot())
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: opts, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(chunk.Err)
				result.CredentialScope = isCredentialScopedError(chunk.Err)
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(ctx, result, auth)
			}
			if !forward {
				return false
			}
			if chunk.Err != nil {
				if ctx == nil {
					out <- chunk
					if cancelAttempt != nil {
						cancelAttempt()
					}
					forward = false
					return true
				}
				select {
				case <-ctx.Done():
					forward = false
					return false
				case out <- chunk:
					if cancelAttempt != nil {
						cancelAttempt()
					}
					forward = false
					return true
				}
			}
			if len(chunk.Payload) == 0 {
				return true
			}
			payload := rewriteForceMappedStreamChunk(rewriter, chunk.Payload)
			if len(payload) == 0 {
				return true
			}
			chunk.Payload = payload
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				forward = false
				return false
			case out <- chunk:
				return true
			}
		}
		for _, chunk := range buffered {
			if !forward {
				break
			}
			if ok := emit(chunk); !ok {
				if cancelAttempt != nil {
					cancelAttempt()
				}
				discardStreamChunks(remaining)
				return
			}
		}
		for {
			var (
				chunk cliproxyexecutor.StreamChunk
				open  bool
			)
			if ctx != nil {
				// Prefer a chunk that is already available so an upstream error delivered
				// together with cancellation can still record its RetryAfter cooldown.
				select {
				case chunk, open = <-remaining:
				default:
					select {
					case <-ctx.Done():
						if cancelAttempt != nil {
							cancelAttempt()
						}
						discardStreamChunks(remaining)
						return
					case chunk, open = <-remaining:
					}
				}
			} else {
				chunk, open = <-remaining
			}
			if !open {
				break
			}
			if !forward {
				continue
			}
			if ok := emit(chunk); !ok {
				if cancelAttempt != nil {
					cancelAttempt()
				}
				discardStreamChunks(remaining)
				return
			}
		}
		if forward {
			tail := finishForceMappedStreamChunks(rewriter)
			rewriter = nil
			if len(tail) > 0 {
				tailChunk := cliproxyexecutor.StreamChunk{Payload: tail}
				if !emit(tailChunk) {
					return
				}
			}
		}
		if !failed && (ctx == nil || ctx.Err() == nil) {
			m.recordExecutionResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: true, Options: opts, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}, auth)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel, executionModel string, execModels []string, pooled bool, aliasResult OAuthModelAliasResult, routing *apiKeyModelRoutingSnapshot) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executor = executorForAuth(executor, auth)
	baseCtx := contextWithRequestedModelAlias(ctx, opts, routeModel)
	var lastErr error
	var upstreamErr error
	didRefreshOnUnauthorized := false
	for idx, execModel := range execModels {
		execCtx := newUpstreamAttemptContext(baseCtx)
		resultModel := m.stateModelForExecution(auth, routeModel, execModel, pooled)
		execReq := req
		execReq.Model = execModel
		if executionModel != "" {
			execReq.Model = executionModel
		}
		execOpts := opts
		execReq = attachResolvedExecutionModelInfo(routing, execReq, auth, routeModel, execModel, executionModel != "")
		if errCtx := execCtx.Err(); errCtx != nil {
			return nil, errCtx
		}
		entry := logEntryWithRequestID(execCtx)
		payload := execOpts.OriginalRequest
		if len(payload) == 0 {
			payload = execReq.Payload
		}
		execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
		execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
		executionSeq := m.nextExecutionSeq()
		// Capture the token this attempt uses: executors may mutate auth in
		// place, so failure results must not read it back later.
		accessToken := authAccessToken(auth)
		// Each upstream attempt gets its own cancellable context so abandoned
		// attempts (failover, refresh retry, empty stream) are canceled before
		// their chunks are drained.
		var cancelAttempt context.CancelFunc
		var attemptCleaned bool
		startAttempt := func(parent context.Context) context.Context {
			if cancelAttempt != nil {
				cancelAttempt()
			}
			var attemptCtx context.Context
			attemptCtx, cancelAttempt = context.WithCancel(parent)
			attemptCleaned = false
			return attemptCtx
		}
		cancelCurrentAttempt := func() {
			if cancelAttempt != nil {
				cancelAttempt()
				cancelAttempt = nil
			}
		}
		abandonAttempt := func(res *cliproxyexecutor.StreamResult) {
			cancelCurrentAttempt()
			if attemptCleaned {
				return
			}
			attemptCleaned = true
			if res != nil && res.Chunks != nil {
				discardStreamChunks(res.Chunks)
			}
		}

		var preRefreshStreamErr error
		attemptCtx := startAttempt(execCtx)
		startStream := time.Now()
		streamResult, errStream := executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
		errStream = markUpstreamExecutionAttemptFromContext(attemptCtx, errStream)
		durationStream := time.Since(startStream)
		if errStream != nil {
			abandonAttempt(streamResult)
			if errCtx := execCtx.Err(); errCtx != nil {
				m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errStream, executionSeq)
				return nil, errCtx
			}
			alreadyTried := didRefreshOnUnauthorized
			refreshed, okRefresh := m.tryRefreshAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, errStream, alreadyTried)
			if okRefresh {
				preRefreshStreamErr = errStream
				auth = refreshed
				publishSelectedAuthMetadata(execOpts.Metadata, auth)
				didRefreshOnUnauthorized = true
				execCtx = newUpstreamAttemptContext(execCtx)
				execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
				executionSeq = m.nextExecutionSeq()
				accessToken = authAccessToken(auth)
				attemptCtx = startAttempt(execCtx)
				startRetry := time.Now()
				streamResult, errStream = executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
				errStream = markUpstreamExecutionAttemptFromContext(attemptCtx, errStream)
				durationRetry := time.Since(startRetry)
				if errStream != nil {
					abandonAttempt(streamResult)
					warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, durationRetry, errStream)
					if errCtx := execCtx.Err(); errCtx != nil {
						m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errStream, executionSeq)
						return nil, errCtx
					}
				}
			} else {
				warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, durationStream, errStream)
			}
		}
		if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errStream); errCancel != nil {
			abandonAttempt(streamResult)
			return nil, errCancel
		}
		streamResult, errStream = validateStreamResult(streamResult, errStream)
		errStream = markUpstreamExecutionAttemptFromContext(attemptCtx, errStream)
		if errStream != nil {
			abandonAttempt(streamResult)
			rerr := resultErrorFromError(errStream)
			action, okAction := matchRequestScopedErrorAction(auth, errStream, m.runtimeConfigSnapshot())
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			result.RetryAfter = retryAfterFromError(errStream)
			if isCredentialScopedError(errStream) {
				result.CredentialScope = true
			}
			applyRequestScopedActionToResult(action, okAction, &result)
			m.recordExecutionResult(execCtx, result, auth)
			errStream = annotateTrustedRetryAfterByHint(errStream)
			if hasUpstreamExecutionAttempt(errStream) {
				syncPreferredUpstreamError(&upstreamErr, errStream)
			} else if preRefreshStreamErr != nil {
				syncPreferredUpstreamError(&upstreamErr, preRefreshStreamErr)
			}
			if isRequestStopError(errStream) {
				return nil, errStream
			}
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(errStream)
				}
				lastErr = errStream
				if result.CredentialScope {
					return nil, preferredExecutionAttemptError(errStream, upstreamErr)
				}
				continue
			}
			if isTerminalRequestStopError(errStream) || isRequestInvalidError(errStream) || cliproxyexecutor.IsUpstreamWebsocketTurnError(errStream) {
				return nil, errStream
			}
			lastErr = errStream
			if result.CredentialScope {
				return nil, preferredExecutionAttemptError(errStream, upstreamErr)
			}
			continue
		}

		var preRefreshBootstrapErr error
		var preRefreshBootstrapHeaders http.Header
		buffered, closed, bootstrapErr := readStreamBootstrap(attemptCtx, streamResult.Chunks)
		bootstrapErr = markUpstreamExecutionAttemptFromContext(attemptCtx, bootstrapErr)
		if bootstrapErr != nil {
			abandonAttempt(streamResult)
			if errCtx := execCtx.Err(); errCtx != nil {
				m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, bootstrapErr, executionSeq)
				return nil, errCtx
			}
			alreadyTried := didRefreshOnUnauthorized
			refreshed, okRefresh := m.tryRefreshAfterUnauthorized(newUpstreamAttemptContext(execCtx), auth, bootstrapErr, alreadyTried)
			if okRefresh {
				preRefreshBootstrapErr = bootstrapErr
				preRefreshBootstrapHeaders = streamResult.Headers
				auth = refreshed
				publishSelectedAuthMetadata(execOpts.Metadata, auth)
				didRefreshOnUnauthorized = true
				execCtx = newUpstreamAttemptContext(execCtx)
				execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
				executionSeq = m.nextExecutionSeq()
				accessToken = authAccessToken(auth)
				attemptCtx = startAttempt(execCtx)
				startRetry := time.Now()
				retryStream, retryErr := executor.ExecuteStream(attemptCtx, auth, execReq, execOpts)
				retryErr = markUpstreamExecutionAttemptFromContext(attemptCtx, retryErr)
				retryStream, retryErr = validateStreamResult(retryStream, retryErr)
				retryErr = markUpstreamExecutionAttemptFromContext(attemptCtx, retryErr)
				if retryErr != nil {
					abandonAttempt(retryStream)
					if errCtx := execCtx.Err(); errCtx != nil {
						m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, retryErr, executionSeq)
						return nil, errCtx
					}
					bootstrapErr = retryErr
					warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
					streamResult = &cliproxyexecutor.StreamResult{}
				} else {
					streamResult = retryStream
					buffered, closed, bootstrapErr = readStreamBootstrap(attemptCtx, streamResult.Chunks)
					bootstrapErr = markUpstreamExecutionAttemptFromContext(attemptCtx, bootstrapErr)
					if bootstrapErr != nil {
						abandonAttempt(streamResult)
					}
					if errCtx := execCtx.Err(); errCtx != nil {
						m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, bootstrapErr, executionSeq)
						abandonAttempt(streamResult)
						return nil, errCtx
					}
					if bootstrapErr != nil {
						warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startRetry), bootstrapErr)
					}
				}
			} else {
				warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startStream), bootstrapErr)
			}
		}
		if errCancel := claudeOAuthRequestCancellation(execCtx, auth, bootstrapErr); errCancel != nil {
			abandonAttempt(streamResult)
			return nil, errCancel
		}
		if bootstrapErr != nil {
			abandonAttempt(streamResult)
			if !hasUpstreamExecutionAttempt(bootstrapErr) {
				if preRefreshBootstrapErr != nil {
					syncPreferredUpstreamError(&upstreamErr, newStreamBootstrapError(preRefreshBootstrapErr, preRefreshBootstrapHeaders))
				} else if preRefreshStreamErr != nil {
					syncPreferredUpstreamError(&upstreamErr, preRefreshStreamErr)
				}
			}
			action, okAction := matchRequestScopedErrorAction(auth, bootstrapErr, m.runtimeConfigSnapshot())
			if okAction {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				applyRequestScopedActionToResult(action, okAction, &result)
				m.recordExecutionResult(execCtx, result, auth)
				bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
				if isRequestStopError(bootstrapErr) {
					headerErr := newStreamHeaderError(bootstrapErr, streamResult.Headers)
					syncPreferredUpstreamError(&upstreamErr, headerErr)
					return nil, headerErr
				}
				if isRequestScopedStop(action, okAction) {
					headerErr := newStreamHeaderError(bootstrapErr, streamResult.Headers)
					syncPreferredUpstreamError(&upstreamErr, headerErr)
					return nil, wrapRequestStopError(headerErr)
				}
				bootstrapErr = newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
				lastErr = bootstrapErr
				if result.CredentialScope {
					currentErr := bootstrapErr
					return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
				}
				continue
			}
			if isTerminalRequestStopError(bootstrapErr) || isRequestInvalidError(bootstrapErr) || cliproxyexecutor.IsUpstreamWebsocketTurnError(bootstrapErr) {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(execCtx, result, auth)
				bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
				syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
				return nil, newStreamHeaderError(bootstrapErr, streamResult.Headers)
			}
			if idx < len(execModels)-1 {
				rerr := resultErrorFromError(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				if isCredentialScopedError(bootstrapErr) {
					result.CredentialScope = true
				}
				m.recordExecutionResult(execCtx, result, auth)
				bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
				bootstrapErr = newStreamBootstrapError(bootstrapErr, streamResult.Headers)
				syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
				lastErr = bootstrapErr
				if result.CredentialScope {
					currentErr := bootstrapErr
					return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
				}
				continue
			}
			rerr := resultErrorFromError(bootstrapErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: rerr, Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			result.RetryAfter = retryAfterFromError(bootstrapErr)
			if isCredentialScopedError(bootstrapErr) {
				result.CredentialScope = true
			}
			m.recordExecutionResult(execCtx, result, auth)
			bootstrapErr = annotateTrustedRetryAfterByHint(bootstrapErr)
			bootstrapErr = newStreamBootstrapError(bootstrapErr, streamResult.Headers)
			syncPreferredUpstreamError(&upstreamErr, bootstrapErr)
			currentErr := bootstrapErr
			return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
		}

		if closed || len(buffered) == 0 {
			cancelCurrentAttempt()
			if !closed && !attemptCleaned {
				attemptCleaned = true
				discardStreamChunks(streamResult.Chunks)
			}
			emptyErr := markUpstreamExecutionAttemptFromContext(attemptCtx, &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true})
			currentErr := newStreamBootstrapError(emptyErr, streamResult.Headers)
			if hasUpstreamExecutionAttempt(emptyErr) {
				syncPreferredUpstreamError(&upstreamErr, currentErr)
			} else if preRefreshBootstrapErr != nil {
				syncPreferredUpstreamError(&upstreamErr, newStreamBootstrapError(preRefreshBootstrapErr, preRefreshBootstrapHeaders))
			} else if preRefreshStreamErr != nil {
				syncPreferredUpstreamError(&upstreamErr, preRefreshStreamErr)
			}
			warnLogUpstreamFailure(execCtx, entry, provider, execModel, auth, time.Since(startStream), emptyErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(emptyErr), Options: execOpts, AccessToken: accessToken, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			m.recordExecutionResult(execCtx, result, auth)
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, preferredExecutionAttemptError(currentErr, upstreamErr)
		}

		attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, execModel, aliasResult)
		handoffCancel := cancelAttempt
		cancelAttempt = nil
		return m.wrapStreamResultWithCancel(execCtx, auth.Clone(), provider, resultModel, routeModel, streamResult.Headers, buffered, streamResult.Chunks, attemptAliasResult, execOpts, executionSeq, handoffCancel), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, preferredExecutionAttemptError(lastErr, upstreamErr)
}
