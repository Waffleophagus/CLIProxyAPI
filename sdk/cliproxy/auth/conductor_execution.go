package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redact"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

const (
	// ExcludedAuthIDsMetadataKey stores credential IDs already attempted in the
	// current request retry round.
	ExcludedAuthIDsMetadataKey = "excluded_auth_ids"
	// CloseAllExecutionSessionsID asks an executor to release all active execution sessions.
	// Executors that do not support this marker may ignore it.
	CloseAllExecutionSessionsID = "__all_execution_sessions__"
)

func newUpstreamAttemptContext(ctx context.Context) context.Context {
	ctx = logging.WithFreshResponseHeadersHolder(ctx)
	return cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
}

func claudeOAuthRequestCancellation(ctx context.Context, auth *Auth, err error) error {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") || !strings.EqualFold(strings.TrimSpace(auth.Attributes["auth_kind"]), "oauth") {
		return nil
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// upstreamAttemptSeq allocates a process-unique identifier for every
// upstream execution attempt marker so error wrappers rebuilt during
// annotation can still be traced back to the same physical attempt.
var upstreamAttemptSeq atomic.Uint64

type upstreamExecutionAttemptError struct {
	cause error
	// attemptID identifies the physical upstream attempt this error was
	// raised for; it is preserved when bookkeeping wrappers are rebuilt.
	attemptID uint64
}

func (e *upstreamExecutionAttemptError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *upstreamExecutionAttemptError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func markUpstreamExecutionAttempt(err error) error {
	if err == nil {
		return nil
	}
	if hasUpstreamExecutionAttempt(err) {
		return err
	}
	return markUpstreamExecutionAttemptWithID(err, upstreamAttemptSeq.Add(1))
}

// markUpstreamExecutionAttemptWithID tags err with the caller's attempt
// identity, reusing the existing marker when the error already carries one.
func markUpstreamExecutionAttemptWithID(err error, id uint64) error {
	if err == nil {
		return nil
	}
	if hasUpstreamExecutionAttempt(err) {
		return err
	}
	return &upstreamExecutionAttemptError{cause: err, attemptID: id}
}

func markUpstreamExecutionAttemptFromContext(ctx context.Context, err error) error {
	if err == nil || !cliproxyexecutor.UpstreamAttempted(ctx) {
		return err
	}
	return markUpstreamExecutionAttempt(err)
}

func hasUpstreamExecutionAttempt(err error) bool {
	var marked *upstreamExecutionAttemptError
	return errors.As(err, &marked) && marked != nil
}

func unwrapUpstreamExecutionAttempt(err error) error {
	marked, ok := err.(*upstreamExecutionAttemptError)
	if !ok || marked == nil || marked.cause == nil {
		return err
	}
	return marked.cause
}

func unwrapExecutionBoundaryError(err error) error {
	unwrapped, _ := unwrapExecutionBoundaryErrorChanged(err)
	return unwrapped
}

// unwrapExecutionBoundaryErrorChanged strips internal request-stop and upstream
// attempt markers, including those nested inside header-carrying stream
// wrappers, so callers keep the upstream response headers.
func unwrapExecutionBoundaryErrorChanged(err error) (error, bool) {
	changed := false
	for err != nil {
		switch wrapped := err.(type) {
		case requestStopError:
			err = wrapped.error
			changed = true
		case *upstreamExecutionAttemptError:
			if wrapped == nil || wrapped.cause == nil {
				return err, changed
			}
			err = wrapped.cause
			changed = true
		case *streamHeaderError:
			if wrapped == nil {
				return nil, changed
			}
			inner, innerChanged := unwrapExecutionBoundaryErrorChanged(wrapped.cause)
			if !innerChanged {
				return err, changed
			}
			return newStreamHeaderError(inner, wrapped.headers), true
		case *streamBootstrapError:
			if wrapped == nil {
				return nil, changed
			}
			inner, innerChanged := unwrapExecutionBoundaryErrorChanged(wrapped.cause)
			if !innerChanged {
				return err, changed
			}
			copied := *wrapped
			copied.cause = inner
			return &copied, true
		default:
			return err, changed
		}
	}
	return nil, changed
}

func unwrapStoppedStreamError(err error) error {
	unwrapped := unwrapExecutionBoundaryError(err)
	if bootstrap, ok := unwrapped.(*streamBootstrapError); ok && bootstrap != nil {
		return newStreamHeaderError(bootstrap.cause, bootstrap.headers)
	}
	return unwrapped
}

func preferredExecutionAttemptError(fallback, upstream error) error {
	if errors.Is(fallback, context.Canceled) || errors.Is(fallback, context.DeadlineExceeded) {
		return fallback
	}
	if upstream == nil {
		return fallback
	}
	return markUpstreamExecutionAttempt(upstream)
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, opts.ProxyURL)
	req, opts = cliproxysession.Enrich(req, opts)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	var selectionCooldownDeadline time.Time
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	for attempt := 0; ; attempt++ {
		roundAttempted := make(map[string]struct{})
		roundOpts := withAttemptedAuthTracker(opts, roundAttempted)
		resp, errExec := m.executeMixedOnce(ctx, normalized, req, roundOpts, maxRetryCredentials, attempt, defaultRequestRetry)
		if errExec == nil {
			return resp, nil
		}
		if isTerminalRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
		}
		observeSelectionCooldownDeadline(&selectionCooldownDeadline, errExec, time.Now())
		if hasUpstreamExecutionAttempt(errExec) {
			preferredUpstreamErr = errExec
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterErrorWithAttempted(ctx, opts, errExec, attempt, normalized, retryModel, maxWait, defaultRequestRetry, roundAttempted)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
		}
		lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		lastErr = unwrapExecutionBoundaryError(lastErr)
		lastErr = m.annotatePoolCooldownTerminalError(ctx, normalized, retryModel, opts, lastErr, selectionCooldownDeadline)
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, opts.ProxyURL)
	req, opts = cliproxysession.Enrich(req, opts)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	var selectionCooldownDeadline time.Time
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	for attempt := 0; ; attempt++ {
		roundAttempted := make(map[string]struct{})
		roundOpts := withAttemptedAuthTracker(opts, roundAttempted)
		resp, errExec := m.executeCountMixedOnce(ctx, normalized, req, roundOpts, maxRetryCredentials, attempt, defaultRequestRetry)
		if errExec == nil {
			return resp, nil
		}
		if isTerminalRequestStopError(errExec) {
			return cliproxyexecutor.Response{}, unwrapExecutionBoundaryError(errExec)
		}
		observeSelectionCooldownDeadline(&selectionCooldownDeadline, errExec, time.Now())
		if hasUpstreamExecutionAttempt(errExec) {
			preferredUpstreamErr = errExec
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterErrorWithAttempted(ctx, opts, errExec, attempt, normalized, retryModel, maxWait, defaultRequestRetry, roundAttempted)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
		}
		lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		lastErr = unwrapExecutionBoundaryError(lastErr)
		lastErr = m.annotatePoolCooldownTerminalError(ctx, normalized, retryModel, opts, lastErr, selectionCooldownDeadline)
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx = cliproxyexecutor.WithRequestProxyURL(ctx, opts.ProxyURL)
	req, opts = cliproxysession.Enrich(req, opts)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	defaultRequestRetry, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var preferredUpstreamErr error
	var selectionCooldownDeadline time.Time
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	attempt := 0
	for {
		roundAttempted := make(map[string]struct{})
		roundOpts := withAttemptedAuthTracker(opts, roundAttempted)
		result, errStream := m.executeStreamMixedOnce(ctx, normalized, req, roundOpts, maxRetryCredentials, attempt, defaultRequestRetry)
		if errStream == nil {
			return result, nil
		}
		if isTerminalRequestStopError(errStream) {
			// Keep stopped/terminated bootstrap failures as direct errors carrying
			// upstream headers via streamHeaderError rather than streamErrorResult so
			// outer bootstrap retries cannot replay an explicitly stopped request.
			return nil, unwrapStoppedStreamError(errStream)
		}
		observeSelectionCooldownDeadline(&selectionCooldownDeadline, errStream, time.Now())
		if hasUpstreamExecutionAttempt(errStream) {
			if isDirectStreamTerminalError(errStream) {
				preferredUpstreamErr = errStream
			} else {
				syncPreferredUpstreamError(&preferredUpstreamErr, errStream)
			}
		}
		lastErr = errStream
		wait, shouldRetry := m.shouldRetryAfterErrorWithAttempted(ctx, opts, errStream, attempt, normalized, retryModel, maxWait, defaultRequestRetry, roundAttempted)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
			return nil, errWait
		}
		attempt++
	}
	if lastErr != nil {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
		}
		if preferredUpstreamErr != nil && !isDirectStreamTerminalError(lastErr) {
			lastErr = preferredExecutionAttemptError(lastErr, preferredUpstreamErr)
		}
		lastErr = unwrapExecutionBoundaryError(lastErr)
		lastErr = m.annotatePoolCooldownTerminalError(ctx, normalized, retryModel, opts, lastErr, selectionCooldownDeadline)
		var bootstrapErr *streamBootstrapError
		if errors.As(lastErr, &bootstrapErr) && bootstrapErr != nil {
			return streamErrorResult(bootstrapErr.Headers(), lastErr), nil
		}
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func isRequestTerminatedError(err error) bool {
	var terminated *cliproxyexecutor.RequestTerminatedError
	return (errors.As(err, &terminated) && terminated != nil) || cliproxyexecutor.IsUpstreamWebsocketTurnError(err)
}

func executorForAuth(executor ProviderExecutor, auth *Auth) ProviderExecutor {
	if auth.AuthKind() == AuthKindAPIKey {
		if scoped, ok := executor.(APIKeyConfigExecutor); ok {
			return scoped.ForAPIKey()
		}
	}
	return executor
}

// syncPreferredUpstreamError refreshes the error that terminal selection
// will prefer over the last local failure when the attempt error gained a
// trusted retry annotation after its result was recorded.
func syncPreferredUpstreamError(upstreamErr *error, errExec error) {
	if upstreamErr == nil || errExec == nil || !hasUpstreamExecutionAttempt(errExec) {
		return
	}
	// A stream bootstrap wrapper built from an earlier attempt already
	// carries the response headers needed to hand that failure back as an
	// error stream; keep that earlier failure preferred. Only a hint
	// raised for the very same upstream attempt may annotate that wrapper
	// in place; a hint from a different credential attempt must not mutate
	// an earlier bootstrap, even when both attempts surfaced an equal
	// error value.
	var bootstrap *streamBootstrapError
	if errors.As(*upstreamErr, &bootstrap) && bootstrap != nil {
		if upstreamErrorMatchesBootstrap(errExec, bootstrap) {
			if hint := trustedRetryAfterHint(errExec); hint != nil && *hint > 0 {
				bootstrap.cause = withTrustedRetryAfter(bootstrap.cause, hint)
			}
			return
		}
		// A synthetic empty_stream wrapper must not mask a later real upstream
		// failure, and a headerless bootstrap wrapper can advance to a later
		// real bootstrap failure while still staying preferred over headerless
		// stream-start errors or empty_stream.
		if isEmptyStreamError(bootstrap.cause) && !isEmptyStreamError(errExec) {
			*upstreamErr = errExec
			return
		}
		var execBootstrap *streamBootstrapError
		if len(bootstrap.headers) == 0 && errors.As(errExec, &execBootstrap) && execBootstrap != nil && !isEmptyStreamError(execBootstrap.cause) {
			*upstreamErr = errExec
		}
		return
	}
	*upstreamErr = errExec
}

func isEmptyStreamError(err error) bool {
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == "empty_stream"
}

func isDirectStreamTerminalError(err error) bool {
	if err == nil {
		return false
	}
	var bootstrap *streamBootstrapError
	if errors.As(err, &bootstrap) && bootstrap != nil {
		return false
	}
	var headerErr *streamHeaderError
	if errors.As(err, &headerErr) && headerErr != nil {
		return true
	}
	return isTerminalRequestStopError(err) || isRequestInvalidError(err)
}

// upstreamErrorMatchesBootstrap reports whether err belongs to the same
// upstream attempt that produced bootstrap. Attempt markers carry a
// process-unique id that survives annotation rebuilds, so a hint refreshed
// for the same attempt matches while a different credential attempt is
// rejected even when both surfaced the very same error value. The id is
// read from the innermost source the attempt owns: a nested bootstrap
// wrapper's recorded id first, then the outer attempt marker. Only when
// neither side carries an attempt id (for example tests that bypass the
// marking helpers) does the check fall back to cause identity.
func upstreamErrorMatchesBootstrap(err error, bootstrap *streamBootstrapError) bool {
	if err == nil || bootstrap == nil {
		return false
	}
	execAttemptID := uint64(0)
	var execBootstrap *streamBootstrapError
	if errors.As(err, &execBootstrap) && execBootstrap != nil {
		execAttemptID = execBootstrap.attemptID
	}
	if execAttemptID == 0 {
		var marked *upstreamExecutionAttemptError
		if errors.As(err, &marked) && marked != nil {
			execAttemptID = marked.attemptID
		}
	}
	if execAttemptID != 0 && bootstrap.attemptID != 0 {
		return execAttemptID == bootstrap.attemptID
	}
	return sameInterfaceHeader(unwrapBootstrapSyncError(err), bootstrap.cause)
}

// sameInterfaceHeader reports whether two interface values share the same
// (type, data) header, i.e. are the very same value. It never compares the
// dynamic values themselves, which may be uncomparable.
func sameInterfaceHeader(a, b any) bool {
	ha := *(*[2]uintptr)(unsafe.Pointer(&a))
	hb := *(*[2]uintptr)(unsafe.Pointer(&b))
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	return ha == hb
}

// unwrapBootstrapSyncError peels the wrappers syncPreferredUpstreamError
// deals with so the underlying failure can be compared by identity.
func unwrapBootstrapSyncError(err error) error {
	for err != nil {
		switch wrapped := err.(type) {
		case *upstreamExecutionAttemptError:
			err = wrapped.cause
		case *trustedRetryAfterError:
			err = wrapped.cause
		case *streamBootstrapError:
			err = wrapped.cause
		default:
			return err
		}
	}
	return nil
}

func (m *Manager) executeMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, retryRound int, defaultRequestRetry int) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	// Each attempt starts from this immutable snapshot so selector and
	// executor metadata mutations never leak into later attempts.
	baseMetadata := opts.Metadata
	tried := make(map[string]struct{})
	for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
		tried[authID] = struct{}{}
	}
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	for {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		opts.Metadata = cloneRequestMetadata(baseMetadata)
		pickOpts := opts
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = newUpstreamAttemptContext(execCtx)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		preparedAuth, errPrepare := m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			errPrepare = markUpstreamExecutionAttemptFromContext(execCtx, errPrepare)
			if errCtx := execCtx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts, AccessToken: authAccessToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
			if ra := retryAfterFromError(errPrepare); ra != nil {
				result.RetryAfter = ra
			}
			if isCredentialScopedError(errPrepare) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(auth, errPrepare, m.runtimeConfigSnapshot())
			applyRequestScopedActionToResult(action, okAction, &result)
			m.MarkResult(execCtx, result)
			errPrepare = annotateTrustedRetryAfterByHint(errPrepare)
			syncPreferredUpstreamError(&upstreamErr, errPrepare)
			if isRequestScopedStop(action, okAction) {
				return cliproxyexecutor.Response{}, wrapRequestStopError(errPrepare)
			}
			if !okAction && isRequestInvalidError(errPrepare) {
				return cliproxyexecutor.Response{}, errPrepare
			}
			lastErr = errPrepare
			continue
		}
		if preparedAuth != nil {
			auth = preparedAuth
		}
		executor = executorForAuth(executor, auth)
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			execCtx = newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			if pickOpts.Metadata != nil {
				if canonicalID, ok := pickOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
					meta := make(map[string]any, len(execOpts.Metadata)+2)
					for k, v := range execOpts.Metadata {
						meta[k] = v
					}
					meta[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
					if parentID, okParent := pickOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; okParent && parentID != canonicalID {
						meta[cliproxyexecutor.ParentSessionIDMetadataKey] = parentID
					} else {
						delete(meta, cliproxyexecutor.ParentSessionIDMetadataKey)
					}
					execOpts.Metadata = meta
				}
			}
			payload := execOpts.OriginalRequest
			if len(payload) == 0 {
				payload = execReq.Payload
			}
			execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
			execReq = attachResolvedExecutionModelInfo(routing, execReq, auth, routeModel, upstreamModel, restoreExecutionModel)
			execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
			executionSeq := m.nextExecutionSeq()
			startExec := time.Now()
			var preRefreshExecErr error
			resp, errExec := executor.Execute(execCtx, auth, execReq, execOpts)
			errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
			durationExec := time.Since(startExec)
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, executionSeq)
					return cliproxyexecutor.Response{}, errCtx
				}
				refreshCtx := newUpstreamAttemptContext(execCtx)
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(refreshCtx, auth, errExec, didRefreshOnUnauthorized); okRefresh {
					preRefreshExecErr = errExec
					auth = refreshed
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					execCtx = newUpstreamAttemptContext(execCtx)
					execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
					executionSeq = m.nextExecutionSeq()
					startRetry := time.Now()
					resp, errExec = executor.Execute(execCtx, auth, execReq, execOpts)
					errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
					durationRetry := time.Since(startRetry)
					if errExec != nil {
						warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationRetry, errExec)
						if errCtx := execCtx.Err(); errCtx != nil {
							m.recordCanceledUpstreamCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, executionSeq)
							return cliproxyexecutor.Response{}, errCtx
						}
					}
				} else {
					warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationExec, errExec)
				}
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: errExec == nil, Options: execOpts, AccessToken: authAccessToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				if isCredentialScopedError(errExec) {
					result.CredentialScope = true
				}
				action, okAction := matchRequestScopedErrorAction(auth, errExec, m.runtimeConfigSnapshot())
				applyRequestScopedActionToResult(action, okAction, &result)
				if isResponsesCompactAvailabilityNeutralError(execOpts, errExec, result.Error) {
					m.recordAvailabilityNeutralResult(execCtx, result)
				} else {
					m.MarkResult(execCtx, result)
					errExec = annotateTrustedRetryAfterByHint(errExec)
				}
				if hasUpstreamExecutionAttempt(errExec) {
					syncPreferredUpstreamError(&upstreamErr, errExec)
				} else if preRefreshExecErr != nil {
					syncPreferredUpstreamError(&upstreamErr, preRefreshExecErr)
				}
				if isRequestStopError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				if okAction {
					if isRequestScopedStop(action, okAction) {
						return cliproxyexecutor.Response{}, wrapRequestStopError(errExec)
					}
					authErr = errExec
					if result.CredentialScope {
						break
					}
					continue
				}
				if isResponsesCompactRequestFaultError(execOpts, errExec) || isRequestInvalidError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				authErr = errExec
				if result.CredentialScope {
					break
				}
				continue
			}
			m.MarkResult(execCtx, result)
			attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, upstreamModel, aliasResult)
			rewriteForceMappedResponse(&resp, attemptAliasResult)
			return resp, nil
		}
		if authErr != nil {
			action, okAction := matchRequestScopedErrorAction(auth, authErr, m.runtimeConfigSnapshot())
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return cliproxyexecutor.Response{}, wrapRequestStopError(authErr)
				}
				lastErr = authErr
				continue
			}
			if isResponsesCompactRequestFaultError(opts, authErr) || isRequestInvalidError(authErr) {
				return cliproxyexecutor.Response{}, authErr
			}
			lastErr = authErr
			continue
		}
	}
}

func (m *Manager) executeCountMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, retryRound int, defaultRequestRetry int) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	// Each attempt starts from this immutable snapshot so selector and
	// executor metadata mutations never leak into later attempts.
	baseMetadata := opts.Metadata
	tried := make(map[string]struct{})
	for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
		tried[authID] = struct{}{}
	}
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	for {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		opts.Metadata = cloneRequestMetadata(baseMetadata)
		pickOpts := opts
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return cliproxyexecutor.Response{}, errPick
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = newUpstreamAttemptContext(execCtx)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		preparedAuth, errPrepare := m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			errPrepare = markUpstreamExecutionAttemptFromContext(execCtx, errPrepare)
			if errCtx := execCtx.Err(); errCtx != nil {
				return cliproxyexecutor.Response{}, errCtx
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts, SkipQuotaObservation: true, AccessToken: authAccessToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
			if ra := retryAfterFromError(errPrepare); ra != nil {
				result.RetryAfter = ra
			}
			if isCredentialScopedError(errPrepare) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(auth, errPrepare, m.runtimeConfigSnapshot())
			applyRequestScopedActionToResult(action, okAction, &result)
			m.MarkResult(execCtx, result)
			errPrepare = annotateTrustedRetryAfterByHint(errPrepare)
			syncPreferredUpstreamError(&upstreamErr, errPrepare)
			if isRequestScopedStop(action, okAction) {
				return cliproxyexecutor.Response{}, wrapRequestStopError(errPrepare)
			}
			if !okAction && isRequestInvalidError(errPrepare) {
				return cliproxyexecutor.Response{}, errPrepare
			}
			lastErr = errPrepare
			continue
		}
		if preparedAuth != nil {
			auth = preparedAuth
		}
		executor = executorForAuth(executor, auth)
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			execCtx = newUpstreamAttemptContext(execCtx)
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			if pickOpts.Metadata != nil {
				if canonicalID, ok := pickOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
					meta := make(map[string]any, len(execOpts.Metadata)+2)
					for k, v := range execOpts.Metadata {
						meta[k] = v
					}
					meta[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
					if parentID, okParent := pickOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; okParent && parentID != canonicalID {
						meta[cliproxyexecutor.ParentSessionIDMetadataKey] = parentID
					} else {
						delete(meta, cliproxyexecutor.ParentSessionIDMetadataKey)
					}
					execOpts.Metadata = meta
				}
			}
			payload := execOpts.OriginalRequest
			if len(payload) == 0 {
				payload = execReq.Payload
			}
			execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
			execReq = attachResolvedExecutionModelInfo(routing, execReq, auth, routeModel, upstreamModel, restoreExecutionModel)
			execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
			executionSeq := m.nextExecutionSeq()
			startExec := time.Now()
			var preRefreshExecErr error
			resp, errExec := executor.CountTokens(execCtx, auth, execReq, execOpts)
			errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
			durationExec := time.Since(startExec)
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					m.recordCanceledCountTokensCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, executionSeq)
					return cliproxyexecutor.Response{}, errCtx
				}
				refreshCtx := newUpstreamAttemptContext(execCtx)
				if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(refreshCtx, auth, errExec, didRefreshOnUnauthorized); okRefresh {
					preRefreshExecErr = errExec
					auth = refreshed
					publishSelectedAuthMetadata(execOpts.Metadata, auth)
					didRefreshOnUnauthorized = true
					execCtx = newUpstreamAttemptContext(execCtx)
					execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
					executionSeq = m.nextExecutionSeq()
					startRetry := time.Now()
					resp, errExec = executor.CountTokens(execCtx, auth, execReq, execOpts)
					errExec = markUpstreamExecutionAttemptFromContext(execCtx, errExec)
					durationRetry := time.Since(startRetry)
					if errExec != nil {
						warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationRetry, errExec)
						if errCtx := execCtx.Err(); errCtx != nil {
							m.recordCanceledCountTokensCooldown(execCtx, auth, provider, resultModel, routeModel, execOpts, errExec, executionSeq)
							return cliproxyexecutor.Response{}, errCtx
						}
					}
				} else {
					warnLogUpstreamFailure(execCtx, entry, provider, upstreamModel, auth, durationExec, errExec)
				}
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, RouteModel: routeModel, Success: errExec == nil, Options: execOpts, SkipQuotaObservation: true, AccessToken: authAccessToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				action, okAction := matchRequestScopedErrorAction(auth, errExec, m.runtimeConfigSnapshot())
				applyRequestScopedActionToResult(action, okAction, &result)
				// Some Anthropic-compatible upstreams and plugin executors do not
				// implement count_tokens and return a generic endpoint 404 or an
				// explicit 501 / unsupported-capability error. Record the failure
				// for hooks and metrics without suspending a model that remains
				// usable through the messages endpoint.
				if isCountTokensEndpointNotFoundError(errExec, execReq.Model) && (result.Error == nil || result.Error.Code != ErrorCodeForceCooldown) {
					m.recordAvailabilityNeutralResult(execCtx, result)
				} else {
					if isCredentialScopedError(errExec) {
						result.CredentialScope = true
					}
					m.MarkResult(execCtx, result)
					errExec = annotateTrustedRetryAfterByHint(errExec)
				}
				if hasUpstreamExecutionAttempt(errExec) {
					syncPreferredUpstreamError(&upstreamErr, errExec)
				} else if preRefreshExecErr != nil {
					syncPreferredUpstreamError(&upstreamErr, preRefreshExecErr)
				}
				if isRequestStopError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				if okAction {
					if isRequestScopedStop(action, okAction) {
						return cliproxyexecutor.Response{}, wrapRequestStopError(errExec)
					}
					authErr = errExec
					if result.CredentialScope {
						break
					}
					continue
				}
				if isRequestInvalidError(errExec) {
					return cliproxyexecutor.Response{}, errExec
				}
				authErr = errExec
				if result.CredentialScope {
					break
				}
				continue
			}
			m.MarkResult(execCtx, result)
			attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, upstreamModel, aliasResult)
			rewriteForceMappedResponse(&resp, attemptAliasResult)
			return resp, nil
		}
		if authErr != nil {
			action, okAction := matchRequestScopedErrorAction(auth, authErr, m.runtimeConfigSnapshot())
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return cliproxyexecutor.Response{}, wrapRequestStopError(authErr)
				}
				lastErr = authErr
				continue
			}
			if isRequestInvalidError(authErr) {
				return cliproxyexecutor.Response{}, authErr
			}
			lastErr = authErr
			continue
		}
	}
}

func (m *Manager) executeStreamMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, retryRound int, defaultRequestRetry int) (*cliproxyexecutor.StreamResult, error) {
	if len(providers) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	// Each attempt starts from this immutable snapshot so selector and
	// executor metadata mutations never leak into later attempts.
	baseMetadata := opts.Metadata
	tried := make(map[string]struct{})
	for authID := range m.requestRetryRoundExclusions(retryRound, defaultRequestRetry) {
		tried[authID] = struct{}{}
	}
	attempted := make(map[string]struct{})
	var lastErr error
	var upstreamErr error
	for {
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return nil, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		opts.Metadata = cloneRequestMetadata(baseMetadata)
		pickOpts := opts
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			if lastErr != nil {
				return nil, preferredExecutionAttemptError(lastErr, upstreamErr)
			}
			return nil, errPick
		}
		if auth == nil || executor == nil {
			return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		// Enrich before auth preparation so prepare-stage usage records observe the client request.
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = newUpstreamAttemptContext(execCtx)
		models, pooled, aliasResult, routing := m.executionModelCandidatesWithAlias(auth, routeModel)
		models = m.filterExecutionModels(auth, routeModel, models, pooled)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		publishSelectedAuthMetadata(opts.Metadata, auth)
		preparedAuth, errPrepare := m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			errPrepare = markUpstreamExecutionAttemptFromContext(execCtx, errPrepare)
			if errCtx := execCtx.Err(); errCtx != nil {
				return nil, errCtx
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return nil, errCancel
			}
			stateModel := m.selectionModelKeyForAuth(auth, routeModel)
			if stateModel == "" {
				stateModel = canonicalModelKey(routeModel)
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: false, Error: resultErrorFromError(errPrepare), Options: pickOpts, AccessToken: authAccessToken(auth), CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
			if ra := retryAfterFromError(errPrepare); ra != nil {
				result.RetryAfter = ra
			}
			if isCredentialScopedError(errPrepare) {
				result.CredentialScope = true
			}
			action, okAction := matchRequestScopedErrorAction(auth, errPrepare, m.runtimeConfigSnapshot())
			applyRequestScopedActionToResult(action, okAction, &result)
			m.MarkResult(execCtx, result)
			errPrepare = annotateTrustedRetryAfterByHint(errPrepare)
			syncPreferredUpstreamError(&upstreamErr, errPrepare)
			if isRequestStopError(errPrepare) {
				return nil, errPrepare
			}
			if isRequestScopedStop(action, okAction) {
				return nil, wrapRequestStopError(errPrepare)
			}
			if !okAction && (isTerminalRequestStopError(errPrepare) || isRequestInvalidError(errPrepare)) {
				return nil, errPrepare
			}
			lastErr = errPrepare
			continue
		}
		if preparedAuth != nil {
			auth = preparedAuth
		}
		execReq := sanitizeDownstreamWebsocketFallbackRequest(execCtx, auth, req)
		streamExecutionModel := ""
		if restoreExecutionModel {
			streamExecutionModel = executionModel
		}
		execOpts := opts
		if pickOpts.Metadata != nil {
			if canonicalID, ok := pickOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; ok {
				meta := make(map[string]any, len(execOpts.Metadata)+2)
				for k, v := range execOpts.Metadata {
					meta[k] = v
				}
				meta[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
				if parentID, okParent := pickOpts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey]; okParent && parentID != canonicalID {
					meta[cliproxyexecutor.ParentSessionIDMetadataKey] = parentID
				} else {
					delete(meta, cliproxyexecutor.ParentSessionIDMetadataKey)
				}
				execOpts.Metadata = meta
			}
		}
		payload := execOpts.OriginalRequest
		if len(payload) == 0 {
			payload = execReq.Payload
		}
		execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
		execCtx = syncMetadataSessionToContext(execCtx, execOpts.Metadata)
		streamResult, errStream := m.executeStreamWithModelPool(execCtx, executor, auth, provider, execReq, execOpts, routeModel, streamExecutionModel, models, pooled, aliasResult, routing)
		if errStream != nil {
			syncPreferredUpstreamError(&upstreamErr, errStream)
			if errCtx := execCtx.Err(); errCtx != nil && ctx != nil && ctx.Err() != nil {
				return nil, errCtx
			}
			if isRequestStopError(errStream) {
				return nil, errStream
			}
			action, okAction := matchRequestScopedErrorAction(auth, errStream, m.runtimeConfigSnapshot())
			if okAction {
				if isRequestScopedStop(action, okAction) {
					return nil, wrapRequestStopError(errStream)
				}
				lastErr = errStream
				continue
			}
			if isTerminalRequestStopError(errStream) || isRequestInvalidError(errStream) || cliproxyexecutor.IsUpstreamWebsocketTurnError(errStream) {
				return nil, errStream
			}
			lastErr = errStream
			continue
		}
		return streamResult, nil
	}
}

func withAttemptedAuthTracker(opts cliproxyexecutor.Options, attempted map[string]struct{}) cliproxyexecutor.Options {
	if attempted == nil {
		return opts
	}
	meta := cloneRequestMetadata(opts.Metadata)
	prevCallback, _ := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		if strings.TrimSpace(authID) != "" {
			attempted[authID] = struct{}{}
		}
		if prevCallback != nil {
			prevCallback(authID)
		}
	}
	opts.Metadata = meta
	return opts
}

type metadataCloneVisitKey struct {
	ptr      uintptr
	capacity int
	isSlice  bool
	typ      reflect.Type
}

var metadataMapAnyType = reflect.TypeFor[map[string]any]()

func cloneRequestMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return make(map[string]any, 4)
	}
	dst := make(map[string]any, len(src)+4)
	var visited map[metadataCloneVisitKey]any
	rootKey := metadataCloneVisitKey{ptr: reflect.ValueOf(src).Pointer()}
	for k, v := range src {
		dst[k] = cloneRequestMetadataValue(v, rootKey, dst, &visited)
	}
	return dst
}

func cloneRequestMetadataValue(v any, rootKey metadataCloneVisitKey, rootDst map[string]any, visited *map[metadataCloneVisitKey]any) any {
	switch v.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr, float32, float64, func(string):
		return v
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		key := metadataCloneVisitKey{ptr: rv.Pointer()}
		if key == rootKey {
			if rv.Type() == metadataMapAnyType {
				return rootDst
			}
			return reflect.ValueOf(rootDst).Convert(rv.Type()).Interface()
		}
		if *visited == nil {
			*visited = make(map[metadataCloneVisitKey]any)
		} else if existing, ok := (*visited)[key]; ok {
			ev := reflect.ValueOf(existing)
			if ev.Type() == rv.Type() {
				return existing
			}
			return ev.Convert(rv.Type()).Interface()
		}
		cloned := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		(*visited)[key] = cloned.Interface()
		elemType := rv.Type().Elem()
		iter := rv.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneReflectMetadataElement(iter.Value(), elemType, rootKey, rootDst, visited))
		}
		return cloned.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		if rv.Cap() == 0 {
			return reflect.MakeSlice(rv.Type(), 0, 0).Interface()
		}
		elemType := rv.Type().Elem()
		if elemType.Size() == 0 {
			return reflect.MakeSlice(rv.Type(), rv.Len(), rv.Cap()).Interface()
		}
		key := metadataCloneVisitKey{ptr: rv.Pointer(), capacity: rv.Cap(), isSlice: true, typ: elemType}
		if *visited == nil {
			*visited = make(map[metadataCloneVisitKey]any)
		} else if existing, ok := (*visited)[key]; ok {
			ev := reflect.ValueOf(existing)
			if ev.Type() != rv.Type() {
				ev = ev.Convert(rv.Type())
			}
			return ev.Slice(0, rv.Len()).Interface()
		}
		clonedFull := reflect.MakeSlice(rv.Type(), rv.Cap(), rv.Cap())
		(*visited)[key] = clonedFull.Interface()
		fullSrc := rv.Slice(0, rv.Cap())
		switch elemType.Kind() {
		case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct, reflect.Interface:
			for i := 0; i < rv.Cap(); i++ {
				clonedFull.Index(i).Set(cloneReflectMetadataElement(fullSrc.Index(i), elemType, rootKey, rootDst, visited))
			}
		default:
			reflect.Copy(clonedFull, fullSrc)
		}
		return clonedFull.Slice(0, rv.Len()).Interface()
	case reflect.Array:
		cloned := reflect.New(rv.Type()).Elem()
		elemType := rv.Type().Elem()
		for i := 0; i < rv.Len(); i++ {
			cloned.Index(i).Set(cloneReflectMetadataElement(rv.Index(i), elemType, rootKey, rootDst, visited))
		}
		return cloned.Interface()
	case reflect.Struct:
		rt := rv.Type()
		var cloned reflect.Value
		for i := 0; i < rt.NumField(); i++ {
			sf := rt.Field(i)
			if !sf.IsExported() {
				continue
			}
			fv := rv.Field(i)
			switch fv.Kind() {
			case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct, reflect.Interface:
				if !cloned.IsValid() {
					cloned = reflect.New(rt).Elem()
					cloned.Set(rv)
				}
				cloned.Field(i).Set(cloneReflectMetadataElement(fv, sf.Type, rootKey, rootDst, visited))
			}
		}
		if !cloned.IsValid() {
			return v
		}
		return cloned.Interface()
	default:
		return v
	}
}

func cloneReflectMetadataElement(elem reflect.Value, elemType reflect.Type, rootKey metadataCloneVisitKey, rootDst map[string]any, visited *map[metadataCloneVisitKey]any) reflect.Value {
	switch elem.Kind() {
	case reflect.Interface:
		if elem.IsNil() {
			return reflect.Zero(elemType)
		}
		clonedVal := cloneRequestMetadataValue(elem.Interface(), rootKey, rootDst, visited)
		if clonedVal == nil {
			return reflect.Zero(elemType)
		}
		return reflect.ValueOf(clonedVal)
	case reflect.Map, reflect.Slice:
		if elem.IsNil() {
			return elem
		}
		return reflect.ValueOf(cloneRequestMetadataValue(elem.Interface(), rootKey, rootDst, visited))
	case reflect.Array, reflect.Struct:
		return reflect.ValueOf(cloneRequestMetadataValue(elem.Interface(), rootKey, rootDst, visited))
	default:
		return elem
	}
}

func ensureRequestedModelMetadata(opts cliproxyexecutor.Options, requestedModel string) cliproxyexecutor.Options {
	opts.Metadata = cloneRequestMetadata(opts.Metadata)
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return opts
	}
	if hasRequestedModelMetadata(opts.Metadata) {
		return opts
	}
	opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] = requestedModel
	return opts
}

func authSelectionModelFromOptions(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	case []byte:
		if strings.TrimSpace(string(value)) != "" {
			return strings.TrimSpace(string(value))
		}
	}
	return fallback
}

func executionModelForAuthSelection(opts cliproxyexecutor.Options, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	selectionModel := authSelectionModelFromOptions(opts, model)
	if selectionModel == model {
		return "", false
	}
	return model, true
}

func hasRequestedModelMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []byte:
		return strings.TrimSpace(string(v)) != ""
	default:
		return false
	}
}

func (m *Manager) prepareRequestAuth(ctx context.Context, executor ProviderExecutor, auth *Auth) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok {
		return auth, nil
	}

	return m.PrepareRequestAuth(ctx, preparer, auth)
}

// PrepareRequestAuth prepares a registered credential using the same serialization
// and lifecycle checks as normal request execution. Management tools use this path too.
func (m *Manager) PrepareRequestAuth(ctx context.Context, preparer RequestAuthPreparer, auth *Auth) (*Auth, error) {
	if m == nil || preparer == nil || auth == nil || !preparer.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}

	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	// Serialize preparation with credential refresh (including 401 recovery)
	// per credential so they never race the same credential state.
	releaseRefresh, errLock := m.lockRefreshContext(ctx, id)
	if errLock != nil {
		return nil, errLock
	}
	defer releaseRefresh()

	m.mu.RLock()
	current := m.auths[id]
	var target *Auth
	if current != nil {
		target = current.Clone()
	}
	m.mu.RUnlock()
	if target == nil {
		return nil, fmt.Errorf("prepare %s auth: credential no longer registered", prepareProviderLabel(auth.Provider))
	}

	if !preparer.ShouldPrepareRequestAuth(target) {
		return target, nil
	}

	base := target.Clone()
	updated, errPrepare := preparer.PrepareRequestAuth(ctx, base.Clone())
	if errPrepare != nil {
		return auth, errPrepare
	}
	if updated == nil {
		return target, nil
	}

	saved, errUpdate := m.UpdatePreparedAuth(ctx, base, updated)
	if errUpdate != nil {
		return nil, errUpdate
	}
	if saved != nil {
		return saved, nil
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "meta") {
		return nil, fmt.Errorf("prepare meta auth: credential removed during mint")
	}
	return nil, fmt.Errorf("prepare %s auth: credential removed during preparation", prepareProviderLabel(auth.Provider))
}

func prepareProviderLabel(provider string) string {
	label := strings.ToLower(strings.TrimSpace(provider))
	if label == "" {
		return "request"
	}
	return label
}

func contextWithRequestedModelAlias(ctx context.Context, opts cliproxyexecutor.Options, fallback string) context.Context {
	alias := requestedModelAliasFromOptions(opts, fallback)
	ctx = coreusage.WithRequestedModelAlias(ctx, alias)
	effort := reasoningEffortFromOptions(opts)
	if effort != "" {
		ctx = coreusage.WithReasoningEffort(ctx, effort)
	}
	serviceTier := serviceTierFromOptions(opts)
	if serviceTier != "" {
		ctx = coreusage.WithServiceTier(ctx, serviceTier)
	}
	if generate, ok := generateFromOptions(opts); ok {
		ctx = coreusage.WithGenerate(ctx, generate)
	}
	ctx = coreusage.WithStream(ctx, opts.Stream)
	return ctx
}

func requestedModelAliasFromOptions(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	case []byte:
		if len(value) == 0 {
			return fallback
		}
		return strings.TrimSpace(string(value))
	default:
		return fallback
	}
}

func reasoningEffortFromOptions(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func serviceTierFromOptions(opts cliproxyexecutor.Options) string {
	return stringMetadataValue(opts.Metadata, cliproxyexecutor.ServiceTierMetadataKey)
}

func generateFromOptions(opts cliproxyexecutor.Options) (bool, bool) {
	if len(opts.Metadata) == 0 {
		return false, false
	}
	raw, ok := opts.Metadata[cliproxyexecutor.GenerateMetadataKey]
	if !ok || raw == nil {
		return false, false
	}
	switch value := raw.(type) {
	case bool:
		return value, true
	default:
		return false, false
	}
}

func stringMetadataValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func pinnedAuthIDFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.PinnedAuthMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch val := raw.(type) {
	case string:
		return strings.TrimSpace(val)
	case []byte:
		return strings.TrimSpace(string(val))
	default:
		return ""
	}
}

func disallowFreeAuthFromMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.DisallowFreeAuthMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch val := raw.(type) {
	case bool:
		return val
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(val))
		return err == nil && parsed
	case []byte:
		parsed, err := strconv.ParseBool(strings.TrimSpace(string(val)))
		return err == nil && parsed
	default:
		return false
	}
}

func isFreeCodexAuth(auth *Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

func publishSelectedAuthMetadata(meta map[string]any, auth *Auth) {
	if len(meta) == 0 || auth == nil {
		return
	}
	if authID := strings.TrimSpace(auth.ID); authID != "" {
		meta[cliproxyexecutor.SelectedAuthMetadataKey] = authID
		if callback, ok := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string)); ok && callback != nil {
			callback(authID)
		}
	}
	if authIndex := strings.TrimSpace(auth.EnsureIndex()); authIndex != "" {
		meta[cliproxyexecutor.SelectedAuthIndexMetadataKey] = authIndex
		if callback, ok := meta[cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey].(func(string)); ok && callback != nil {
			callback(authIndex)
		}
	}
}

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	exec, _ := m.executorLocked(provider)
	return exec
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

// roundTripperFor retrieves an HTTP RoundTripper for the given auth if a provider is registered.
func (m *Manager) roundTripperFor(auth *Auth) http.RoundTripper {
	m.mu.RLock()
	p := m.rtProvider
	m.mu.RUnlock()
	if p == nil || auth == nil {
		return nil
	}
	return p.RoundTripperFor(auth)
}

// RoundTripperProvider defines a minimal provider of per-auth HTTP transports.
type RoundTripperProvider interface {
	RoundTripperFor(auth *Auth) http.RoundTripper
}

// RequestPreparer is an optional interface that provider executors can implement
// to mutate outbound HTTP requests with provider credentials.
type RequestPreparer interface {
	PrepareRequest(req *http.Request, auth *Auth) error
}

func executorKeyFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		providerKey := strings.TrimSpace(auth.Attributes["provider_key"])
		compatName := strings.TrimSpace(auth.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return util.OpenAICompatibleProviderKey(providerKey)
		}
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		providerKey := strings.TrimSpace(auth.Label)
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		return util.OpenAICompatibleProviderKey(providerKey)
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// logEntryWithRequestID returns a logrus entry with request_id field if available in context.
func logEntryWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

func debugLogAuthSelection(entry *log.Entry, auth *Auth, provider string, model string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	if entry == nil || auth == nil {
		return
	}
	accountType, accountInfo := auth.AccountInfo()
	proxyInfo := auth.ProxyInfo()
	suffix := ""
	if proxyInfo != "" {
		suffix = " " + proxyInfo
	}
	switch accountType {
	case "api_key":
		entry.Debugf("Use API key %s for model %s%s", util.HideAPIKey(accountInfo), model, suffix)
	case "oauth":
		ident := formatOauthIdentity(auth, provider, accountInfo)
		entry.Debugf("Use OAuth %s for model %s%s", ident, model, suffix)
	}
}

func formatOauthIdentity(auth *Auth, provider string, accountInfo string) string {
	if auth == nil {
		return ""
	}
	// Prefer the auth's provider when available.
	providerName := strings.TrimSpace(auth.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(provider)
	}
	// Only log the basename to avoid leaking host paths.
	// FileName may be unset for some auth backends; fall back to ID.
	authFile := strings.TrimSpace(auth.FileName)
	if authFile == "" {
		authFile = strings.TrimSpace(auth.ID)
	}
	if authFile != "" {
		authFile = filepath.Base(authFile)
	}
	parts := make([]string, 0, 3)
	if providerName != "" {
		parts = append(parts, "provider="+providerName)
	}
	if authFile != "" {
		parts = append(parts, "auth_file="+authFile)
	}
	if len(parts) == 0 {
		return accountInfo
	}
	return strings.Join(parts, " ")
}

func formatAuthIdentity(auth *Auth, provider string) string {
	if auth == nil {
		return "auth=nil"
	}
	accountType, accountInfo := auth.AccountInfo()
	switch accountType {
	case "api_key":
		return fmt.Sprintf("api_key=%s", util.HideAPIKey(accountInfo))
	case "oauth":
		return formatOauthIdentity(auth, provider, accountInfo)
	default:
		if auth.FileName != "" {
			return fmt.Sprintf("auth_file=%s", filepath.Base(auth.FileName))
		}
		if auth.ID != "" {
			return fmt.Sprintf("auth_id=%s", auth.ID)
		}
		if accountInfo != "" {
			return accountInfo
		}
		return "unknown"
	}
}

// safeErrorDiagnosticForLog returns a bounded, single-line error diagnostic with
// the given credential values and any credential-shaped tokens redacted.
func safeErrorDiagnosticForLog(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	diagnostic := err.Error()
	type logDiagnosticError interface {
		LogDiagnostic() string
	}
	var diagnosticErr logDiagnosticError
	if errors.As(err, &diagnosticErr) && diagnosticErr != nil {
		if markedDiagnostic := strings.TrimSpace(diagnosticErr.LogDiagnostic()); markedDiagnostic != "" {
			diagnostic = markedDiagnostic
		}
	}
	return logging.SafeDiagnosticForLog(redact.Secrets(diagnostic, secrets...))
}

func warnLogUpstreamFailure(ctx context.Context, entry *log.Entry, provider, model string, auth *Auth, duration time.Duration, err error) {
	if err == nil {
		return
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if isRequestInvalidError(err) {
		return
	}
	if entry == nil {
		if ctx != nil {
			entry = logEntryWithRequestID(ctx)
		} else {
			entry = log.NewEntry(log.StandardLogger())
		}
	}
	authIdent := formatAuthIdentity(auth, provider)
	errSummary := safeErrorDiagnosticForLog(err, CredentialSecrets(auth)...)
	duration = duration.Round(time.Millisecond)
	if statusCode := statusCodeFromError(err); statusCode != 0 {
		entry.Warnf("%3d | %13v | upstream execution failed: provider=%s model=%s auth=%s err=%s", statusCode, duration, provider, model, authIdent, errSummary)
		return
	}
	entry.Warnf("upstream execution failed: provider=%s model=%s auth=%s duration=%s err=%s", provider, model, authIdent, duration, errSummary)
}

// InjectCredentials delegates per-provider HTTP request preparation when supported.
// If the registered executor for the auth provider implements RequestPreparer,
// it will be invoked to modify the request (e.g., add headers).
func (m *Manager) InjectCredentials(req *http.Request, authID string) error {
	if req == nil || authID == "" {
		return nil
	}
	m.mu.RLock()
	a := m.auths[authID]
	var exec ProviderExecutor
	if a != nil {
		exec, _ = m.executorLocked(executorKeyFromAuth(a))
	}
	m.mu.RUnlock()
	if a == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return p.PrepareRequest(req, a)
	}
	return nil
}

// PrepareHttpRequest injects provider credentials into the supplied HTTP request.
func (m *Manager) PrepareHttpRequest(ctx context.Context, auth *Auth, req *http.Request) error {
	if m == nil {
		return &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	if ctx != nil {
		*req = *req.WithContext(ctx)
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	preparer, ok := exec.(RequestPreparer)
	if !ok || preparer == nil {
		return &Error{Code: "not_supported", Message: "executor does not support http request preparation"}
	}
	return preparer.PrepareRequest(req, auth)
}

// NewHttpRequest constructs a new HTTP request and injects provider credentials into it.
func (m *Manager) NewHttpRequest(ctx context.Context, auth *Auth, method, targetURL string, body []byte, headers http.Header) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		httpReq.Header = headers.Clone()
	}
	if errPrepare := m.PrepareHttpRequest(ctx, auth, httpReq); errPrepare != nil {
		return nil, errPrepare
	}
	return httpReq, nil
}

// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
func (m *Manager) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if m == nil {
		return nil, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return nil, &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return nil, &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return nil, &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	return exec.HttpRequest(ctx, auth, req)
}

func ensureCanonicalSessionMetadata(metadata map[string]any, headers http.Header, payload []byte) map[string]any {
	if metadata != nil {
		if canonicalID, ok := metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); ok && strings.TrimSpace(canonicalID) != "" {
			return metadata
		}
	}
	canonicalID := CanonicalSessionID(headers, payload, metadata)
	if canonicalID == "" {
		return metadata
	}
	out := make(map[string]any, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	out[cliproxyexecutor.CanonicalSessionIDMetadataKey] = canonicalID
	return out
}

func syncMetadataSessionToContext(ctx context.Context, metadata map[string]any) context.Context {
	if ctx == nil {
		return nil
	}
	canonicalID := ""
	if len(metadata) > 0 {
		canonicalID, _ = metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
		if canonicalID == "" {
			canonicalID, _ = metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string)
		}
		if canonicalID == "" {
			if execID, _ := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); execID != "" {
				execID = strings.TrimSpace(execID)
				if !strings.HasPrefix(execID, "execution:") {
					canonicalID = "execution:" + execID
				} else {
					canonicalID = execID
				}
			}
		}
		if canonicalID == "" {
			if derivedID, _ := metadata[cliproxyexecutor.DerivedSessionIDMetadataKey].(string); derivedID != "" {
				derivedID = strings.TrimSpace(derivedID)
				if !strings.HasPrefix(derivedID, "derived:") {
					canonicalID = "derived:" + derivedID
				} else {
					canonicalID = derivedID
				}
			}
		}
	}
	canonicalID = strings.TrimSpace(canonicalID)
	if canonicalID == "" {
		clientMeta := logging.GetClientRequestMetadata(ctx)
		if clientMeta.SessionID != "" || clientMeta.ParentSessionID != "" || clientMeta.NodeKind != "" || clientMeta.IsFork || clientMeta.IsCompaction {
			clientMeta.SessionID = ""
			clientMeta.ParentSessionID = ""
			clientMeta.NodeKind = ""
			clientMeta.IsFork = false
			clientMeta.IsCompaction = false
			ctx = logging.WithClientRequestMetadata(ctx, clientMeta)
		}
		return util.WithSessionID(ctx, "")
	}
	clientMeta := logging.GetClientRequestMetadata(ctx)
	clientMeta.SessionID = cliproxysession.BoundSessionIdentity(canonicalID)
	if parentID, ok := metadata[cliproxyexecutor.ParentSessionIDMetadataKey].(string); ok && strings.TrimSpace(parentID) != "" {
		clientMeta.ParentSessionID = cliproxysession.BoundSessionIdentity(strings.TrimSpace(parentID))
	} else {
		clientMeta.ParentSessionID = ""
	}
	if clientMeta.SessionID == clientMeta.ParentSessionID {
		clientMeta.ParentSessionID = ""
	}
	if nodeKind, ok := metadata[cliproxyexecutor.NodeKindMetadataKey].(string); ok && strings.TrimSpace(nodeKind) != "" {
		clientMeta.NodeKind = strings.TrimSpace(nodeKind)
	} else {
		clientMeta.NodeKind = ""
	}
	if isFork, ok := metadata[cliproxyexecutor.IsForkMetadataKey].(bool); ok {
		clientMeta.IsFork = isFork
	} else {
		clientMeta.IsFork = false
	}
	if isCompaction, ok := metadata[cliproxyexecutor.IsCompactionMetadataKey].(bool); ok {
		clientMeta.IsCompaction = isCompaction
	} else {
		clientMeta.IsCompaction = false
	}
	ctx = logging.WithClientRequestMetadata(ctx, clientMeta)
	return util.WithSessionID(ctx, clientMeta.SessionID)
}

func sanitizeDownstreamWebsocketFallbackRequest(ctx context.Context, auth *Auth, req cliproxyexecutor.Request) cliproxyexecutor.Request {
	if !cliproxyexecutor.DownstreamWebsocket(ctx) || authWebsocketsEnabled(auth) || len(req.Payload) == 0 {
		return req
	}
	updated, errDelete := sjson.DeleteBytes(req.Payload, "generate")
	if errDelete != nil {
		return req
	}
	req.Payload = updated
	return req
}
