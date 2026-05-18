// Package errcode — TRANSIENT-NET-HELPER-FORM-01: see archtest godoc.
//
// IsTransientNet is the single-source predicate that classifies an error
// chain as "transient because it carries a net.Error". Adapter classifiers
// (S3 / Redis / Vault isTransient*Error) delegate their net.Error branch to
// this helper so the transient set stays symmetric across adapters and any
// future widening / narrowing happens in one place.
//
// Form: `var n net.Error; return errors.As(err, &n)`. Locked by archtest
// TRANSIENT-NET-HELPER-FORM-01 (form-uniqueness; Timeout() / OpError-specific
// SelectorExpr in the body are RED).
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
// ref: aws/aws-sdk-go-v2 aws/retry/retryable_error.go:84-157
// ref: redis/go-redis error.go:83-147
// ref: hashicorp/vault api/client.go IsRetryableErr (via go-retryablehttp)
package errcode

import (
	"errors"
	"net"
)

// IsTransientNet reports whether err contains a net.Error in its chain.
//
// Returns true for any net.Error (timeout class, *net.OpError dial refused /
// connection reset, *net.DNSError). Returns false for nil and for errors
// that do not implement net.Error.
func IsTransientNet(err error) bool {
	var n net.Error
	return errors.As(err, &n)
}
