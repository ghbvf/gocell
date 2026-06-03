package auth

// operator.go — AuthOperator, the operator-credential ListenerAuth plan for the
// admin control-plane (cell.AdminListener, /admin/v1/*).
//
// Design: mirrors AuthServiceToken (auth_plan.go). The plan carries kernel-level
// dependencies (raw credentials, a kernel-projection rate limiter, an optional
// observer func); the bootstrap apply-switch
// (runtime/bootstrap/auth_plan_apply.go) constructs the concrete
// runtime/auth.NewBootstrapMiddleware from them, so kernel/auth holds no
// net/http machinery. OperatorRateLimiter follows the same kernel-projection
// idiom as NonceStore / HMACKeyring in auth_types.go.
//
// This is the listener-level operator gate. It is distinct from the route-level
// runtime/auth.Route.BootstrapAuth used by the per-cell setup/admin endpoint
// (FMT-28): both reuse runtime/auth.NewBootstrapMiddleware, but at different
// scopes. AuthOperator authenticates an operator over a network-isolated
// (loopback) admin listener; there is no caller-cell allowlist (that is the
// cell→cell InternalListener model).
//
// ref: EventStoreDB projections HTTP admin API — network-isolated admin port +
//      operator (basic-auth) credentials.

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/pkg/validation"
)

// OperatorRateLimiter decides whether a request identified by key (typically the
// client IP) should be allowed. It is the kernel projection of
// runtime/auth.BootstrapRateLimiter so that the AuthOperator plan can hold its
// limiter as a kernel-level interface, keeping kernel/ free of runtime/ imports
// (same idiom as NonceStore / HMACKeyring in auth_types.go). The composition
// root injects a concrete per-IP limiter (e.g. adapters/ratelimit.TokenBucket);
// there is deliberately no built-in allow-all implementation — operator
// credentials must always be rate-limited to defeat brute-force enumeration.
type OperatorRateLimiter interface {
	Allow(key string) bool
}

// AuthOperator is the operator-credential listener plan for the admin
// control-plane (cell.AdminListener). Bootstrap installs
// runtime/auth.NewBootstrapMiddleware with these credentials, rate limiter, and
// optional auth-fail observer: per-IP rate limit → HTTP Basic Auth →
// constant-time credential comparison → uniform 401. operator→system actions
// (e.g. projection rebuild) are authenticated by the env operator credentials,
// NOT by a caller-cell allowlist.
type AuthOperator struct {
	// Username and Password are the env operator credentials, compared in
	// constant time against the HTTP Basic Auth header. Required; empty values
	// are rejected by NewAuthOperator. Held as raw bytes (not a struct mirroring
	// runtime/auth.BootstrapCredentials) so the apply-switch maps them into the
	// runtime type at construction with no parallel struct.
	Username []byte
	Password []byte
	// Limiter is the per-IP rate limiter applied before credential parsing.
	// Required; nil (bare or typed) is rejected by NewAuthOperator.
	Limiter OperatorRateLimiter
	// OnAuthFail is an optional observer invoked after a 401/429 is written
	// (reason ∈ {"missing_header","wrong_credentials","rate_limited"}); nil
	// disables it. Routes operator auth failures to an audit log without
	// kernel/auth importing cells/.
	OnAuthFail func(ctx context.Context, reason string)
}

// NewAuthOperator constructs an AuthOperator plan. Returns an error when either
// credential is empty or the limiter is nil: an operator gate with empty
// credentials would authenticate every request, and one without a rate limiter
// cannot defeat brute-force. The observer is optional (nil disables it).
func NewAuthOperator(
	username, password []byte,
	limiter OperatorRateLimiter,
	onAuthFail func(ctx context.Context, reason string),
) (AuthOperator, error) {
	if len(username) == 0 || len(password) == 0 {
		return AuthOperator{}, fmt.Errorf(
			"auth: NewAuthOperator requires non-empty operator username and password")
	}
	if validation.IsNilInterface(limiter) {
		return AuthOperator{}, fmt.Errorf(
			"auth: NewAuthOperator limiter must not be nil;" +
				" operator credentials require per-IP rate limiting")
	}
	return AuthOperator{
		Username:   username,
		Password:   password,
		Limiter:    limiter,
		OnAuthFail: onAuthFail,
	}, nil
}

func (AuthOperator) authPlanKind() AuthKind { return AuthKindOperator }
func (AuthOperator) Describe() string       { return "operator" }

// listenerAuthOK seals the ListenerAuth interface (see AuthNone.listenerAuthOK).
func (AuthOperator) listenerAuthOK() {}

// Compile-time assertion.
var _ ListenerAuth = AuthOperator{}
