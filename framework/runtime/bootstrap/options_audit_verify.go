package bootstrap

// options_audit_verify.go — With* options for the #1755 admin audit chain verify
// endpoint. Two orthogonal options (mirrors the projection harness: dep injection
// via WithProjection* + endpoint enable via WithProjectionRebuildEndpoint):
//
//   - WithAuditChainVerifier injects the dependency. The composition root calls it
//     UNCONDITIONALLY whenever the admin pool is provisioned — independent of any
//     operator/admin-listener decision — so it never couples admin-pool presence
//     (which already enables #1810 super-admin reads) to the admin plane.
//   - WithAuditChainVerifyEndpoint enables the route. The composition root calls it
//     ONLY in the operator-credentials block (alongside the AdminListener
//     declaration). So admin pool present + no operator creds → verifier injected,
//     endpoint dormant, no AdminListener — and #1810 reads are unaffected.

import (
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/audit"
)

// WithAuditChainVerifier injects the admin-pool-backed [audit.ChainVerifier] used
// by the framework audit chain verify endpoint (#1755). Cumulative-builder
// semantics: a typed-nil or bare-nil verifier is not stored (so the endpoint stays
// unserved when the admin pool is absent). The endpoint itself is enabled
// separately via WithAuditChainVerifyEndpoint; phase0
// (validateAuditChainVerifyEndpoint) fails fast if the endpoint is enabled but no
// verifier was injected.
func WithAuditChainVerifier(v *audit.ChainVerifier) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(v) {
			return
		}
		b.auditChainVerifier = v
	}
}

// WithAuditChainVerifyEndpoint opts the assembly into the framework audit chain
// verify control-plane endpoint:
//
//	POST /admin/v1/audit/chains/verify
//
// mounted by bootstrap on the AdminListener (the framework-owned-RouteGroup
// pattern — no contract.yaml, no host cell). The handler runs a full
// per-(namespace, tenant) chain integrity verify and returns a 200 report (200
// even when chains are tampered — allValid=false; only a run-level failure is 5xx).
//
// This is an operator→system control-plane action; authentication is the
// AdminListener's operator credential gate (AuthOperator). The AdminListener MUST
// be declared and a verifier MUST be injected (WithAuditChainVerifier) — phase0
// (validateAuditChainVerifyEndpoint) fails fast otherwise. Not calling this option
// leaves the endpoint unmounted with NO error (verify remains programmatic-only).
func WithAuditChainVerifyEndpoint() Option {
	return func(b *Bootstrap) {
		b.auditChainVerifyEnabled = true
	}
}
