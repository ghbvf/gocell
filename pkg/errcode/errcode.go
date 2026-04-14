// Package errcode provides structured error codes for the GoCell framework.
// All errors exposed across package boundaries must use this package instead of
// bare errors.New.
package errcode

import "fmt"

// Code is a typed error code string.
type Code string

// Sentinel error codes used throughout the GoCell framework.
const (
	ErrMetadataInvalid  Code = "ERR_METADATA_INVALID"
	ErrMetadataNotFound Code = "ERR_METADATA_NOT_FOUND"
	ErrCellNotFound     Code = "ERR_CELL_NOT_FOUND"
	ErrSliceNotFound    Code = "ERR_SLICE_NOT_FOUND"
	ErrContractNotFound Code = "ERR_CONTRACT_NOT_FOUND"
	ErrAssemblyNotFound Code = "ERR_ASSEMBLY_NOT_FOUND"
	ErrLifecycleInvalid Code = "ERR_LIFECYCLE_INVALID"
	ErrDependencyCycle  Code = "ERR_DEPENDENCY_CYCLE"
	ErrValidationFailed Code = "ERR_VALIDATION_FAILED"
	ErrReferenceBroken  Code = "ERR_REFERENCE_BROKEN"
	ErrInternal         Code = "ERR_INTERNAL"
	ErrAuthUnauthorized Code = "ERR_AUTH_UNAUTHORIZED"
	ErrAuthForbidden    Code = "ERR_AUTH_FORBIDDEN"
	ErrRateLimited      Code = "ERR_RATE_LIMITED"
	ErrCSRFOriginDenied Code = "ERR_CSRF_ORIGIN_DENIED"
	ErrBodyTooLarge     Code = "ERR_BODY_TOO_LARGE"
	ErrJourneyNotFound  Code = "ERR_JOURNEY_NOT_FOUND"
	ErrTestExecution    Code = "ERR_TEST_EXECUTION"
	ErrCheckRefInvalid  Code = "ERR_CHECKREF_INVALID"
	ErrZeroTestMatch    Code = "ERR_ZERO_TEST_MATCH"
	ErrBusClosed          Code = "ERR_BUS_CLOSED"
	ErrCellMissingOutbox  Code = "ERR_CELL_MISSING_OUTBOX"
	ErrSessionNotFound    Code = "ERR_SESSION_NOT_FOUND"
	ErrSessionConflict    Code = "ERR_SESSION_CONFLICT"
	ErrOrderNotFound      Code = "ERR_ORDER_NOT_FOUND"
	ErrDeviceNotFound     Code = "ERR_DEVICE_NOT_FOUND"
	ErrCommandNotFound    Code = "ERR_COMMAND_NOT_FOUND"
	ErrAdapterPGNoTx      Code = "ERR_ADAPTER_PG_NO_TX"
	ErrAuthKeyInvalid     Code = "ERR_AUTH_KEY_INVALID"
	ErrAuthTokenInvalid   Code = "ERR_AUTH_TOKEN_INVALID"
	ErrAuthTokenExpired   Code = "ERR_AUTH_TOKEN_EXPIRED"

	// Access-core cell error codes.
	ErrAuthUserNotFound         Code = "ERR_AUTH_USER_NOT_FOUND"
	ErrAuthUserDuplicate        Code = "ERR_AUTH_USER_DUPLICATE"
	ErrAuthRoleNotFound         Code = "ERR_AUTH_ROLE_NOT_FOUND"
	ErrAuthInvalidInput         Code = "ERR_AUTH_INVALID_INPUT"
	ErrAuthUserLocked           Code = "ERR_AUTH_USER_LOCKED"
	ErrAuthSessionInvalidInput  Code = "ERR_AUTH_SESSION_INVALID_INPUT"
	ErrAuthIdentityInvalidInput Code = "ERR_AUTH_IDENTITY_INVALID_INPUT"
	ErrAuthLoginInvalidInput    Code = "ERR_AUTH_LOGIN_INVALID_INPUT"
	ErrAuthLoginFailed          Code = "ERR_AUTH_LOGIN_FAILED"
	ErrAuthLogoutInvalidInput   Code = "ERR_AUTH_LOGOUT_INVALID_INPUT"
	ErrAuthRefreshInvalidInput  Code = "ERR_AUTH_REFRESH_INVALID_INPUT"
	ErrAuthRefreshFailed        Code = "ERR_AUTH_REFRESH_FAILED"
	ErrAuthRefreshTokenReuse    Code = "ERR_AUTH_REFRESH_TOKEN_REUSE"
	ErrAuthInvalidToken         Code = "ERR_AUTH_INVALID_TOKEN"
	ErrAuthRBACInvalidInput     Code = "ERR_AUTH_RBAC_INVALID_INPUT"
	ErrAuthKeyMissing           Code = "ERR_AUTH_KEY_MISSING"

	// Config-core cell error codes.
	ErrConfigNotFound            Code = "ERR_CONFIG_NOT_FOUND"
	ErrConfigDuplicate           Code = "ERR_CONFIG_DUPLICATE"
	ErrConfigInvalidInput        Code = "ERR_CONFIG_INVALID_INPUT"
	ErrConfigPublishInvalidInput Code = "ERR_CONFIG_PUBLISH_INVALID_INPUT"
	ErrConfigRepoNotFound        Code = "ERR_CONFIG_REPO_NOT_FOUND"
	ErrConfigRepoDuplicate       Code = "ERR_CONFIG_REPO_DUPLICATE"
	ErrConfigRepoQuery           Code = "ERR_CONFIG_REPO_QUERY"
	ErrFlagNotFound              Code = "ERR_FLAG_NOT_FOUND"
	ErrFlagDuplicate             Code = "ERR_FLAG_DUPLICATE"
	ErrFlagInvalidInput          Code = "ERR_FLAG_INVALID_INPUT"

	// Audit-core cell error codes.
	ErrAuditRepoNotFound Code = "ERR_AUDIT_REPO_NOT_FOUND"
	ErrAuditRepoQuery    Code = "ERR_AUDIT_REPO_QUERY"
	ErrArchiveUpload     Code = "ERR_ARCHIVE_UPLOAD"
	ErrArchiveMarshal    Code = "ERR_ARCHIVE_MARSHAL"
	ErrNotImplemented    Code = "ERR_NOT_IMPLEMENTED"

	// Pagination / validation error codes.
	ErrCursorInvalid      Code = "ERR_CURSOR_INVALID"
	ErrPageSizeExceeded   Code = "ERR_PAGE_SIZE_EXCEEDED"
	ErrInvalidTimeFormat  Code = "ERR_INVALID_TIME_FORMAT"

	// Resilience middleware error codes.
	ErrCircuitOpen Code = "ERR_CIRCUIT_OPEN"

	// WebSocket runtime error codes.
	ErrWSConnNotFound   Code = "ERR_WS_CONN_NOT_FOUND"
	ErrWSAlreadyStarted Code = "ERR_WS_ALREADY_STARTED"
	ErrWSAlreadyStopped Code = "ERR_WS_ALREADY_STOPPED"
	ErrWSHubStopping    Code = "ERR_WS_HUB_STOPPING"
	ErrWSHubNotRunning  Code = "ERR_WS_HUB_NOT_RUNNING"
	ErrWSMaxConns       Code = "ERR_WS_MAX_CONNS"
)

// Error is a structured error that carries a machine-readable Code, a
// human-readable Message, optional Details, and an optional wrapped Cause.
//
// InternalMessage holds diagnostic detail that must never be exposed to
// API consumers. When present, Error() uses it (for logs/traces); HTTP
// response writers use Message (safe for clients).
type Error struct {
	Code            Code
	Message         string
	InternalMessage string
	Details         map[string]any
	Cause           error
}

// Error returns a formatted string representation for logging/diagnostics.
// When InternalMessage is set it is preferred over Message, because Error()
// is consumed by logs and traces — not by API clients.
// Format: "[CODE] msg" or "[CODE] msg: cause" when a Cause is present.
func (e *Error) Error() string {
	msg := e.Message
	if e.InternalMessage != "" {
		msg = e.InternalMessage
	}
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %s", e.Code, msg, e.Cause.Error())
	}
	return fmt.Sprintf("[%s] %s", e.Code, msg)
}

// Unwrap returns the underlying Cause, enabling errors.Is / errors.As chains.
func (e *Error) Unwrap() error {
	return e.Cause
}

// New creates an *Error with the given code and message.
func New(code Code, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
	}
}

// Safe creates an *Error with separate public and internal messages.
// publicMsg is returned to API clients; internalMsg is used in logs/traces
// via Error() and must never be exposed over the wire.
func Safe(code Code, publicMsg, internalMsg string) *Error {
	return &Error{
		Code:            code,
		Message:         publicMsg,
		InternalMessage: internalMsg,
	}
}

// Wrap creates an *Error that wraps an existing error as its Cause.
func Wrap(code Code, message string, cause error) *Error {
	return &Error{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// WithDetails returns a shallow copy of err with the provided details merged in.
// If err.Details is nil a new map is allocated; existing keys are preserved
// unless overwritten by the supplied details.
// It panics if err is nil — callers must not pass a nil *Error.
func WithDetails(err *Error, details map[string]any) *Error {
	if err == nil {
		panic("errcode: WithDetails called with nil *Error")
	}
	merged := make(map[string]any, len(err.Details)+len(details))
	for k, v := range err.Details {
		merged[k] = v
	}
	for k, v := range details {
		merged[k] = v
	}
	return &Error{
		Code:            err.Code,
		Message:         err.Message,
		InternalMessage: err.InternalMessage,
		Details:         merged,
		Cause:           err.Cause,
	}
}
