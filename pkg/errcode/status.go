package errcode

import (
	"log/slog"
	"net/http"
)

// StatusClientClosedRequest is nginx's non-standard 499 status code returned
// when the client closes the connection before the server finishes responding.
// Declared here alongside the errcode→status mapping so kernel/ governance
// rules can import only pkg/errcode without pulling in net/http concerns from
// pkg/httputil.
//
// ref: nginx ngx_http_request.h — NGX_HTTP_CLIENT_CLOSED_REQUEST 499
const StatusClientClosedRequest = 499

// codeToStatus maps known error codes to HTTP status codes.
// All codes use errcode constants for compile-time checking.
var codeToStatus = map[Code]int{
	// --- 404 Not Found ---
	ErrMetadataNotFound:   http.StatusNotFound,
	ErrCellNotFound:       http.StatusNotFound,
	ErrSliceNotFound:      http.StatusNotFound,
	ErrContractNotFound:   http.StatusNotFound,
	ErrAssemblyNotFound:   http.StatusNotFound,
	ErrJourneyNotFound:    http.StatusNotFound,
	ErrSessionNotFound:    http.StatusNotFound,
	ErrSessionConflict:    http.StatusConflict,
	ErrOrderNotFound:      http.StatusNotFound,
	ErrDeviceNotFound:     http.StatusNotFound,
	ErrCommandNotFound:    http.StatusNotFound,
	ErrAuthUserNotFound:   http.StatusNotFound,
	ErrAuthRoleNotFound:   http.StatusNotFound,
	ErrConfigNotFound:     http.StatusNotFound,
	ErrConfigRepoNotFound: http.StatusNotFound,
	ErrFlagNotFound:       http.StatusNotFound,
	ErrWSConnNotFound:     http.StatusNotFound,
	ErrAuditRepoNotFound:  http.StatusNotFound,
	ErrZeroTestMatch:      http.StatusNotFound,

	// --- 400 Bad Request ---
	ErrEnvelopeSchema:            http.StatusBadRequest,
	ErrCursorInvalid:             http.StatusBadRequest,
	ErrPageSizeExceeded:          http.StatusBadRequest,
	ErrValidationFailed:          http.StatusBadRequest,
	ErrValidationInvalidUUID:     http.StatusBadRequest,
	ErrMetadataInvalid:           http.StatusBadRequest,
	ErrLifecycleInvalid:          http.StatusBadRequest,
	ErrReferenceBroken:           http.StatusBadRequest,
	ErrCheckRefInvalid:           http.StatusBadRequest,
	ErrAuthInvalidInput:          http.StatusBadRequest,
	ErrAuthIdentityInvalidInput:  http.StatusBadRequest,
	ErrAuthLoginInvalidInput:     http.StatusBadRequest,
	ErrAuthRefreshInvalidInput:   http.StatusBadRequest,
	ErrAuthSessionInvalidInput:   http.StatusBadRequest,
	ErrAuthLogoutInvalidInput:    http.StatusBadRequest,
	ErrAuthRBACInvalidInput:      http.StatusBadRequest,
	ErrConfigInvalidInput:        http.StatusBadRequest,
	ErrConfigPublishInvalidInput: http.StatusBadRequest,
	ErrFlagInvalidInput:          http.StatusBadRequest,
	ErrInvalidTimeFormat:         http.StatusBadRequest,

	// --- 401 Unauthorized ---
	ErrAuthUnauthorized:       http.StatusUnauthorized,
	ErrAuthKeyInvalid:         http.StatusUnauthorized,
	ErrAuthVerifierConfig:     http.StatusInternalServerError,
	ErrAuthTokenInvalid:       http.StatusUnauthorized,
	ErrAuthTokenExpired:       http.StatusUnauthorized,
	ErrAuthLoginFailed:        http.StatusUnauthorized,
	ErrAuthRefreshFailed:      http.StatusUnauthorized,
	ErrAuthInvalidToken:       http.StatusUnauthorized,
	ErrAuthInvalidTokenIntent: http.StatusUnauthorized,
	ErrRefreshTokenRejected:   http.StatusUnauthorized,

	// --- 403 Forbidden ---
	ErrAuthForbidden:             http.StatusForbidden,
	ErrAuthUserLocked:            http.StatusForbidden,
	ErrCSRFOriginDenied:          http.StatusForbidden,
	ErrAuthPasswordResetRequired: http.StatusForbidden,

	// --- 409 Conflict ---
	ErrAuthUserDuplicate:   http.StatusConflict,
	ErrAuthSelfDelete:      http.StatusConflict,
	ErrAuthRoleDuplicate:   http.StatusConflict,
	ErrConfigDuplicate:     http.StatusConflict,
	ErrConfigRepoDuplicate: http.StatusConflict,
	ErrFlagDuplicate:       http.StatusConflict,
	// ErrDistlockTimeout: the requested lock key is currently held by another
	// holder. The caller should retry (409 rather than 503: the conflict is a
	// business-level contention, not an infra outage).
	ErrDistlockTimeout: http.StatusConflict,

	// --- 410 Gone ---
	// Setup is a one-shot lifecycle endpoint: once the first admin exists, the
	// endpoint is permanently retired for the lifetime of this deployment.
	// 410 signals "permanently unavailable" (vs. 409's "retry may succeed"),
	// shrinks the anonymous attack surface, and lets installer UIs distinguish
	// "not initialized yet" from "already past initialization window".
	ErrSetupAlreadyInitialized: http.StatusGone,

	// --- 429 Too Many Requests ---
	ErrRateLimited: http.StatusTooManyRequests,

	// --- 499 Client Closed Request ---
	// Nginx-style 499: the client disconnected before the server finished
	// responding (context.Canceled surfaced from a downstream IO operation).
	// Routed to log4xx → slog.Warn so the 5xx error rate stays clean of
	// client-direction noise.
	ErrClientCanceled: StatusClientClosedRequest,

	// --- 504 Gateway Timeout ---
	// Server-side or inherited request timeout — context.DeadlineExceeded
	// surfaced from a downstream IO operation.
	//
	// ref: RFC 9110 §15.6.5 — 504 Gateway Timeout
	// ref: kratos transport/http/status — DeadlineExceeded → 504
	ErrServerTimeout: http.StatusGatewayTimeout,

	// --- 413 Request Entity Too Large ---
	ErrBodyTooLarge: http.StatusRequestEntityTooLarge,

	// --- 503 Service Unavailable ---
	ErrServiceUnavailable:     http.StatusServiceUnavailable,
	ErrCircuitOpen:            http.StatusServiceUnavailable,
	ErrWSHubStopping:          http.StatusServiceUnavailable,
	ErrWSHubNotRunning:        http.StatusServiceUnavailable,
	ErrWSMaxConns:             http.StatusServiceUnavailable,
	ErrRelayBudgetExhausted:   http.StatusServiceUnavailable,
	ErrAuthRefreshUnavailable: http.StatusServiceUnavailable,
	// Vault / key-provider unavailability: infra down rather than internal bug.
	// 503 lets upstream load balancers and clients apply retry semantics
	// (Retry-After, circuit breakers) — matching ErrCircuitOpen's model.
	ErrVaultAuthFailed:      http.StatusServiceUnavailable,
	ErrKeyProviderTransient: http.StatusServiceUnavailable,

	// --- 500 Internal Server Error ---
	ErrInternal:                http.StatusInternalServerError,
	ErrDependencyCycle:         http.StatusInternalServerError,
	ErrBusClosed:               http.StatusInternalServerError,
	ErrAdapterPGNoTx:           http.StatusInternalServerError,
	ErrTestExecution:           http.StatusInternalServerError,
	ErrCellMissingOutbox:       http.StatusInternalServerError,
	ErrCellMissingCodec:        http.StatusInternalServerError,
	ErrCellMissingTokenIssuer:  http.StatusInternalServerError,
	ErrCellInvalidConfig:       http.StatusInternalServerError,
	ErrCellPlatformUnsupported: http.StatusInternalServerError,
	ErrArchiveUpload:           http.StatusInternalServerError,
	ErrArchiveMarshal:          http.StatusInternalServerError,
	ErrAuditRepoQuery:          http.StatusInternalServerError,
	ErrConfigRepoQuery:         http.StatusInternalServerError,
	ErrFlagRepoQuery:           http.StatusInternalServerError,
	ErrAuthKeyMissing:          http.StatusInternalServerError,
	// Role resolution failure at token-issuance time — infrastructure fault
	// (RoleRepository unavailable). Fail-closed: callers abort authn action
	// rather than issue a token with empty roles.
	ErrAuthRoleFetchFailed: http.StatusInternalServerError,
	ErrWSAlreadyStarted:    http.StatusInternalServerError,
	ErrWSAlreadyStopped:    http.StatusInternalServerError,
	// Observability init failures (missing Provider, missing CellID) —
	// these originate from composition-root misconfiguration and never
	// escape via HTTP in practice, but the exhaustive test requires every
	// errcode.Code to map. 500 is the conservative choice: if one ever
	// reaches the HTTP layer, it signals an internal setup bug.
	ErrObservabilityConfigInvalid: http.StatusInternalServerError,
	// Lifecycle operation called in wrong state (e.g. bootstrap phase violation).
	ErrBootstrapLifecycle: http.StatusInternalServerError,
	// KeyProvider / encryption failures — infrastructure-level, never leak
	// ciphertext or key IDs to the client; surface as 500 so the sanitised
	// "internal server error" body is returned.
	ErrKeyProviderKeyNotFound:   http.StatusInternalServerError,
	ErrKeyProviderAuthFailed:    http.StatusInternalServerError,
	ErrKeyProviderEncryptFailed: http.StatusInternalServerError,
	ErrKeyProviderDecryptFailed: http.StatusInternalServerError,
	ErrKeyProviderRotateFailed:  http.StatusInternalServerError,
	ErrConfigDecryptFailed:      http.StatusInternalServerError,
	ErrConfigEncryptFailed:      http.StatusInternalServerError,
	ErrConfigKeyMissing:         http.StatusInternalServerError,
	// Control-plane startup configuration errors — fail-fast at boot, never
	// reach HTTP in practice. 500 is the conservative choice if one ever
	// escapes: operator misconfiguration is a deployment bug, not a client bug.
	ErrControlplaneServiceSecretMissing:  http.StatusInternalServerError,
	ErrControlplaneNonceStoreMissing:     http.StatusInternalServerError,
	ErrControlplaneClaimerNotDistributed: http.StatusInternalServerError,
	ErrControlplaneVerboseTokenMissing:   http.StatusInternalServerError,
	ErrControlplaneVerboseTokenSample:    http.StatusInternalServerError,
	// ErrReadyzVerboseDenied is a 401 because the verbose endpoint enforces
	// an X-Readyz-Token bearer check (PR-A35); a mismatched or missing
	// header is treated exactly like any other bearer-token failure.
	ErrReadyzVerboseDenied: http.StatusUnauthorized,
	// ErrReadyzVerboseUnconfigured is a 401 — verbose requested without a
	// configured token AND without explicit Disabled is fail-closed (PR-MODE-1).
	ErrReadyzVerboseUnconfigured: http.StatusUnauthorized,
	// SEC-FAIL-CLOSED startup misconfiguration (PR-MODE-1): adapter endpoint
	// not TLS, listener authChain missing, websocket origins missing — all
	// fail-fast at boot, never reach HTTP in practice. 500 is the conservative
	// choice if one ever escapes (operator misconfiguration, not client bug).
	ErrAdapterEndpointNotTLS:    http.StatusInternalServerError,
	ErrListenerAuthChainMissing: http.StatusInternalServerError,
	ErrWebsocketOriginsMissing:  http.StatusInternalServerError,
	ErrWebsocketOriginsInvalid:  http.StatusInternalServerError,

	// --- Auth replay / nonce-store codes ---
	// ErrAuthReplayDetected is a security signal: the nonce has already been
	// consumed. Client must not retry with the same token (401).
	ErrAuthReplayDetected: http.StatusUnauthorized,
	// ErrNonceStoreFull is a transient capacity condition: the in-memory store
	// is at cap with no expired entries to reclaim. Client should back off (503).
	ErrNonceStoreFull: http.StatusServiceUnavailable,

	// --- 501 Not Implemented ---
	ErrNotImplemented: http.StatusNotImplemented,
}

// MapCodeToStatus maps an errcode.Code to the appropriate HTTP status code.
// Known codes use an explicit lookup table. Unknown codes default to 500
// and emit a warning log to prompt registration.
func MapCodeToStatus(code Code) int {
	if status, ok := codeToStatus[code]; ok {
		return status
	}
	slog.Warn("unmapped error code, defaulting to 500", slog.String("code", string(code)))
	return http.StatusInternalServerError
}

// IsClientError returns true if the given error code maps to a 4xx HTTP status
// (client error). Unknown codes return false.
func IsClientError(code Code) bool {
	status, ok := codeToStatus[code]
	return ok && status >= 400 && status < 500
}

// PublicCode returns the wire-safe error code for an internal errcode.Code.
// Client-error codes are already public and pass through unchanged. Server
// errors collapse to status-level public codes so callers can still distinguish
// retry-relevant transport semantics without learning internal subsystem names.
func PublicCode(code Code) Code {
	status := MapCodeToStatus(code)
	if status < http.StatusInternalServerError {
		return code
	}
	return PublicCodeForStatus(status)
}

// PublicCodeForStatus returns the wire-safe error code for an HTTP status.
// It is used by raw HTTP writers that know the status before they have a typed
// errcode.Code. Unknown or less-specific 5xx statuses intentionally collapse to
// ERR_INTERNAL.
func PublicCodeForStatus(status int) Code {
	switch status {
	case http.StatusServiceUnavailable:
		return ErrServiceUnavailable
	case http.StatusGatewayTimeout:
		return ErrServerTimeout
	default:
		return ErrInternal
	}
}
