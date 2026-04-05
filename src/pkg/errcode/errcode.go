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
	ErrBodyTooLarge     Code = "ERR_BODY_TOO_LARGE"
	ErrJourneyNotFound  Code = "ERR_JOURNEY_NOT_FOUND"
	ErrTestExecution    Code = "ERR_TEST_EXECUTION"
	ErrBusClosed          Code = "ERR_BUS_CLOSED"
	ErrCellMissingOutbox  Code = "ERR_CELL_MISSING_OUTBOX"
	ErrAdapterNoTx        Code = "ERR_ADAPTER_NO_TX"
	ErrAuthKeyInvalid     Code = "ERR_AUTH_KEY_INVALID"
	ErrAuthTokenInvalid   Code = "ERR_AUTH_TOKEN_INVALID"
	ErrAuthTokenExpired   Code = "ERR_AUTH_TOKEN_EXPIRED"
)

// Error is a structured error that carries a machine-readable Code, a
// human-readable Message, optional Details, and an optional wrapped Cause.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
	Cause   error
}

// Error returns a formatted string representation.
// Format: "[CODE] message" or "[CODE] message: cause" when a Cause is present.
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %s", e.Code, e.Message, e.Cause.Error())
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
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
func WithDetails(err *Error, details map[string]any) *Error {
	merged := make(map[string]any, len(err.Details)+len(details))
	for k, v := range err.Details {
		merged[k] = v
	}
	for k, v := range details {
		merged[k] = v
	}
	return &Error{
		Code:    err.Code,
		Message: err.Message,
		Details: merged,
		Cause:   err.Cause,
	}
}
