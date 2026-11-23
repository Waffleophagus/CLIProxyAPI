package auth

import (
	"net/http"
	"testing"
)

type credentialScopedCodedError struct {
	code string
}

func (e credentialScopedCodedError) Error() string            { return "credential scoped " + e.code }
func (e credentialScopedCodedError) ErrorCode() string        { return e.code }
func (e credentialScopedCodedError) StatusCode() int          { return http.StatusTooManyRequests }
func (e credentialScopedCodedError) IsCredentialScoped() bool { return true }

func TestResultErrorFromErrorKeepsCredentialScopeOverModelOnlyCodes(t *testing.T) {
	for _, code := range []string{
		transientRateLimitErrorCode,
		requestScopedErrorCode,
		streamTruncatedErrorCode,
		connectionLifecycleErrorCode,
		transientTransportErrorCode,
	} {
		err := credentialScopedCodedError{code: code}
		if isRequestScopedError(err) {
			t.Fatalf("code %q: credential-scoped error classified as request scoped", code)
		}
		if isStreamTruncatedError(err) {
			t.Fatalf("code %q: credential-scoped error classified as stream truncated", code)
		}
		got := resultErrorFromError(err)
		if got == nil || got.Code != credentialQuotaErrorCode {
			t.Fatalf("code %q: result code = %+v, want %q", code, got, credentialQuotaErrorCode)
		}
	}
}

func TestIsCredentialScopedErrorRecognizesCredentialQuotaCode(t *testing.T) {
	if !isCredentialScopedError(&Error{Code: ErrorCodeCredentialQuota, HTTPStatus: http.StatusTooManyRequests}) {
		t.Fatal("credential_quota code was not treated as credential scoped")
	}
}
