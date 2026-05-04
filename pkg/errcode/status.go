package errcode

import "net/http"

// StatusClientClosedRequest is nginx's non-standard 499 status code returned
// when the client closes the connection before the server finishes responding.
//
// ref: nginx ngx_http_request.h — NGX_HTTP_CLIENT_CLOSED_REQUEST 499
const StatusClientClosedRequest = 499

// Kind is the transport-facing class of an Error.
//
// Unlike Code, Kind is intentionally small and framework-owned. Subsystems own
// their Code values locally; HTTP response status is derived only from Kind.
type Kind int

const (
	// KindInternal is the zero value so partially constructed errors fail closed
	// as 500 instead of accidentally becoming a client-visible status.
	KindInternal Kind = iota
	KindInvalid
	KindUnauthenticated
	KindPermissionDenied
	KindNotFound
	KindConflict
	KindGone
	KindPayloadTooLarge
	KindRateLimited
	KindClientClosed
	KindDeadlineExceeded
	KindUnavailable
	KindNotImplemented
)

// Status returns the HTTP status associated with k.
func (k Kind) Status() int {
	switch k {
	case KindInvalid:
		return http.StatusBadRequest
	case KindUnauthenticated:
		return http.StatusUnauthorized
	case KindPermissionDenied:
		return http.StatusForbidden
	case KindNotFound:
		return http.StatusNotFound
	case KindConflict:
		return http.StatusConflict
	case KindGone:
		return http.StatusGone
	case KindPayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case KindRateLimited:
		return http.StatusTooManyRequests
	case KindClientClosed:
		return StatusClientClosedRequest
	case KindDeadlineExceeded:
		return http.StatusGatewayTimeout
	case KindUnavailable:
		return http.StatusServiceUnavailable
	case KindNotImplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}

// IsClient reports whether k maps to an HTTP 4xx response.
func (k Kind) IsClient() bool {
	status := k.Status()
	return status >= 400 && status < 500
}

// PublicCode returns the wire-safe status-level code for k.
func (k Kind) PublicCode() Code {
	switch k {
	case KindUnavailable:
		return ErrServiceUnavailable
	case KindDeadlineExceeded:
		return ErrServerTimeout
	default:
		return ErrInternal
	}
}

// PublicCodeForStatus returns the wire-safe error code for an HTTP status.
// It is retained for framework-owned raw responses that start from a status
// before they construct an Error.
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
