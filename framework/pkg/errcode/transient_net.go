// Package errcode — TRANSIENT-NET-HELPER-FORM-01: see archtest godoc.
//
// IsTransientNet is the single-source predicate that classifies an error
// chain as "transient because it carries a net.Error". Adapter classifiers
// (S3 / Redis / Vault isTransient*Error) delegate their net.Error branch to
// this helper so the transient set stays symmetric across adapters and any
// future widening / narrowing happens in one place.
//
// Form (locked by archtest TRANSIENT-NET-HELPER-FORM-01):
//
//   - *url.Error is unwrapped first: Op=="parse" → permanent (operator
//     misconfig); other Ops recurse on the wrapped cause.
//   - Otherwise `var n net.Error; return errors.As(err, &n)`.
//
// `*url.Error` itself implements `net.Error` via embedded Timeout() /
// Temporary() forwarders, so a bare `errors.As(err, &netErr)` would
// silently classify URI parse failures as recoverable. RabbitMQ's
// `classifyStructuredDialError` has the same precedent in adapter-local
// code (`*url.Error` tried before `net.Error`).
//
// Symmetry rationale: any error chain that surfaces a net.Error implementation
// (transport timeout, *net.OpError dial refused / connection reset,
// *net.DNSError) represents a transport-level failure where the next retry
// may reach a healthier endpoint. Truly unknown errors (JSON decode, SDK
// internal bugs) do not implement net.Error and fall through to false
// (fail-closed-on-unknown — consumer Requeues, retry-then-budget-DLX path
// never loses an event).
//
// Adapter inventory routed through this helper (ADR
// docs/architecture/202605161800-adr-adapter-error-classification.md
// §Adapter transient inventory):
//
//   - adapters/s3.isTransientS3Error
//   - adapters/redis.isTransientRedisError
//   - adapters/vault.isTransientVaultError
//
// Postgres isRetryablePGError keeps Timeout() filter (pgconn.SafeToRetry
// already handles dial-refused), and rabbitmq classifyStructuredDialError
// uses Timeout() to discriminate code (both branches transient) — both
// allowlisted in ADAPTER-NET-TRANSIENT-FUNNEL-01.
//
// ref: aws/aws-sdk-go-v2 aws/retry/retryable_error.go (url.Error unwrap pattern)
// ref: hashicorp/go-retryablehttp client.go baseRetryPolicy (url.Error permanent shortlist)
// ref: redis/go-redis error.go (net.Error fan-in)
package errcode

import (
	"errors"
	"net"
	"net/url"
)

// IsTransientNet reports whether err contains a net.Error in its chain that
// represents a transport-level transient failure.
//
// Returns true for: any net.Error (timeout class, *net.OpError dial refused /
// connection reset, *net.DNSError).
//
// Returns false for: nil; errors that do not implement net.Error; and
// *url.Error{Op:"parse"} (URL parsing is an operator-side misconfiguration,
// not a transient transport failure). For other *url.Error Ops (HTTP client
// I/O), recurses on the wrapped cause so a `url.Error` wrapping a real
// `*net.OpError` correctly classifies as transient.
//
// For classified *errcode.Error values, use errcode.IsTransient — that
// predicate keys on the WrapInfra transient marker and handles both
// classified and unclassified errors. IsTransientNet is the adapter-side
// helper for the net.Error fan-in branch only.
func IsTransientNet(err error) bool {
	// *url.Error implements net.Error via embedded forwarders; identify and
	// unwrap it before the generic net.Error branch so URI parse / config
	// failures are not silently routed to transient. ref: aws/aws-sdk-go-v2
	// aws/retry/retryable_error.go.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Op == "parse" {
			return false
		}
		return IsTransientNet(urlErr.Err)
	}
	var n net.Error
	return errors.As(err, &n)
}
