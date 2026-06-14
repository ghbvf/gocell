package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
)

// AuthMiddleware extracts a Bearer token from the Authorization header,
// verifies it using the provided IntentTokenVerifier (always enforcing
// token_use=access for business endpoints), and stores the resulting Claims
// in the request context. On failure, it returns a 401 JSON response.
//
// The parameter is IntentTokenVerifier (not TokenVerifier) by design: the
// access-vs-refresh distinction is a hard safety invariant — any verifier
// plugged into business routes must be able to enforce it at the type level,
// so we refuse to compile call sites that pass an intent-unaware verifier.
//
// Public-endpoint bypass is provided through AuthOption values
// (WithPublicEndpointMatcher). The Router installs this via a lazy closure
// during FinalizeAuth so that route declarations from all Cells are aggregated
// before the predicate is compiled.
//
// Internal listener routes that authenticate via service-token or mTLS live on
// a physically separate listener+mux and never reach this middleware — no
// in-band bypass predicate is needed.
//
// ref: go-kratos/kratos — public bypass via selector at composition layer
// ref: go-zero — JWT opt-in per route group, no hidden runtime defaults
func AuthMiddleware(clk clock.Clock, verifier IntentTokenVerifier, opts ...AuthOption) func(http.Handler) http.Handler {
	cfg := defaultAuthConfig()
	cfg.clock = clk
	for _, o := range opts {
		o(&cfg)
	}
	clock.MustHaveClock(clk, "auth.AuthMiddleware")

	isPublic := cfg.publicMatcher
	if isPublic == nil {
		isPublic = func(*http.Request) bool { return false }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r) {
				next.ServeHTTP(w, r)
				return
			}
			handleAuthRequest(w, r, next, verifier, cfg)
		})
	}
}

// handleAuthRequest extracts the bearer token, verifies it (with intent=access
// when the verifier supports it), records metrics, and either forwards to
// next with claims and Principal attached or writes a 401 response.
//
// Enumeration defense: all verification failures — including
// ErrAuthInvalidTokenIntent — are mapped to the generic ERR_AUTH_UNAUTHORIZED
// response code so clients cannot distinguish token-type mismatch from
// signature invalidity or expiry. The specific failure reason is observable
// via the "reason" label on the auth_token_verify_total metric (ops-only
// signal) and in structured logs; it is never forwarded to the HTTP response.
func handleAuthRequest(w http.ResponseWriter, r *http.Request, next http.Handler, verifier IntentTokenVerifier, cfg authConfig) {
	token, reason := extractBearerTokenWithReason(r)
	if token == "" {
		cfg.metrics.recordTokenVerifyCounter(r.Context(), "failure", reason)
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "missing or invalid authorization header"))
		return
	}

	start := cfg.clock.Now()
	ctx, p, err := AuthenticateBearer(r.Context(), verifier, token)
	if err != nil {
		cfg.metrics.recordTokenVerify(r.Context(), "failure", classifyTokenError(err), cfg.clock.Since(start))
		// S43: expected 4xx (invalid/expired token, unauthorized) → Warn;
		// infra errors (key load failure, verifier init error) → Error.
		if errcode.IsExpected4xx(err) {
			cfg.logger.Warn(
				"token verification failed",
				slog.Any("error", err),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
			)
		} else {
			cfg.logger.Error(
				"token verification failed",
				slog.Any("error", err),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
			)
		}
		// KindUnavailable signals a transient infrastructure outage (e.g. JWT key
		// provider sealed). Surface as 503 with a dedicated code so operators can
		// route auth-infra outages separately from invalid-credential 401s.
		var authErr *errcode.Error
		if errors.As(err, &authErr) && authErr.Kind == errcode.KindUnavailable {
			httputil.WriteError(r.Context(), w,
				errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "authentication service unavailable"))
			return
		}
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "invalid token"))
		return
	}
	cfg.metrics.recordTokenVerify(r.Context(), "success", "ok", cfg.clock.Since(start))

	// Password-reset enforcement: when the token carries password_reset_required=true,
	// only exempt endpoints (supplied by the composition root via
	// WithPasswordResetExemptMatcher) are allowed. All other business routes
	// receive 403 ERR_AUTH_PASSWORD_RESET_REQUIRED until the subject changes
	// their password and obtains a new token without the claim. If no matcher
	// is wired, the gate rejects every request — fail-closed default.
	if PasswordResetBlocked(p, isPasswordResetExempt(cfg, r.Method, r.URL.Path)) {
		cfg.logger.Info(
			"auth: password reset required gate blocked request",
			slog.String("subject", p.Subject),
			slog.String("path", r.URL.Path),
			slog.String("method", r.Method),
		)
		hint := ""
		if cfg.passwordResetChangeEndpointHint != nil {
			hint = cfg.passwordResetChangeEndpointHint()
		}
		writePasswordResetRequired(r.Context(), w, hint)
		return
	}

	// Principal was already injected into ctx by AuthenticateBearer (WithPrincipal
	// + principal ctxkeys). Attach the request logger and forward. The enriched
	// ctx built before a password-reset block is discarded on that early return,
	// so no principal leaks into a rejected request.
	ctx = withLogger(ctx, cfg.logger)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// AuthenticateBearer verifies the bearer token (intent=access) and, on success,
// returns a context enriched with the verified *Principal: both WithPrincipal
// and the principal ctxkeys (actor/subject/session) that carry identity across
// the async outbox boundary. It is the single transport-agnostic authentication
// core shared by the HTTP AuthMiddleware (handleAuthRequest) and the gRPC unary
// auth interceptor (runtime/grpc/interceptor) so neither transport duplicates
// the verify→principal→ctx bridge.
//
// SECURITY: there is deliberately NO claims-input variant. A *Principal can only
// be produced from a token that the verifier accepts — no caller (cell,
// interceptor, …) can forge a principal by fabricating Claims. The principal
// ctxkey writes stay physically inside this file, so the
// CTXKEYS-PRINCIPAL-WRITE-CALLER-01 funnel is intact; exporting this function
// does not widen the trust boundary because it gates on VerifyIntent.
//
// On verify failure the input ctx is returned unchanged together with the raw
// verifier error; callers classify it (errcode.IsExpected4xx / KindUnavailable)
// to render their transport-specific rejection.
//
// G1.A: tokens with an empty "sub" claim are rejected before any principal or
// ctxkeys are injected. An empty subject indicates a JWT signing bug or OIDC
// misconfiguration; accepting it would allow a bearer with roles to pass
// RequireAnyRole unchecked (aligned with authenticator.go G1.A).
func AuthenticateBearer(ctx context.Context, verifier IntentTokenVerifier, token string) (context.Context, *Principal, error) {
	claims, err := verifier.VerifyIntent(ctx, token, TokenIntentAccess)
	if err != nil {
		return ctx, nil, err
	}
	p, err := jwtClaimsToPrincipal(claims)
	if err != nil {
		// Device-token validation failed closed (missing subject/tenant or a
		// privileged role); surface the raw error so the caller renders its
		// transport-specific rejection.
		return ctx, nil, err
	}
	if p.Kind == PrincipalUser && p.Subject == "" {
		return ctx, nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "token subject missing")
	}
	ctx = WithPrincipal(ctx, p)
	ctx = injectPrincipalCtxKeys(ctx, p)
	return ctx, p, nil
}

// PasswordResetBlocked reports whether a verified principal carrying
// password_reset_required must be rejected on a non-exempt operation. It
// operates on the VERIFIED *Principal (not forgeable claims); each transport
// renders its own rejection (HTTP 403 ERR_AUTH_PASSWORD_RESET_REQUIRED vs gRPC
// codes.PermissionDenied). A nil principal is treated as not-blocked (the caller
// has already handled the unauthenticated case).
func PasswordResetBlocked(p *Principal, exempt bool) bool {
	return p != nil && p.PasswordResetRequired && !exempt
}

// injectPrincipalCtxKeys writes the authenticated principal identity into the
// pkg/ctxkeys typed keys so that any outbox.NewEntry constructed downstream in
// the same request carries the principal across the async boundary (NewEntry
// reads these keys at its single injection trust boundary via
// outbox.ContextPrincipal). This is the producer-side half of the
// principal-propagation contract; the consumer-side restore lives in
// SubscriberWithMiddleware.
//
// It is the single source for the bridge: BOTH the JWT path (Middleware) and
// the service-token path (ServiceTokenMiddleware) call it after WithPrincipal,
// so neither authentication scheme can silently skip principal propagation.
//
// Mapping:
//   - actor_id   = actorOf(p) — the acting party. For JWT principals this is the
//     subject (develop has no "act" claim); for service principals it is the
//     CallerCellID (see actorOf).
//   - subject_id = p.Subject  — the subject-of-record (JWT "sub"); empty for
//     service principals.
//   - session_id = p.Claims["sid"] — server-side session binding, when present.
//   - tenant_id  = p.TenantID — the tenant isolation boundary, sourced from the
//     JWT "tenant_id" claim (already validated + canonicalized by the JWT
//     verifier, JWTVerifier.VerifyIntent). Empty for service principals (a service token's callerCell
//     is NOT a tenant — spec §service-token: tenant must come from a subject/
//     tenant claim, never the caller cell id), anonymous principals, and
//     single-tenant deployments.
//
// Only non-empty values are written so anonymous / sessionless / subjectless /
// tenantless tokens do not stamp empty principal fields onto produced entries.
func injectPrincipalCtxKeys(ctx context.Context, p *Principal) context.Context {
	if actor := actorOf(p); actor != "" {
		ctx = ctxkeys.WithActorID(ctx, actor)
	}
	if p.Subject != "" {
		ctx = ctxkeys.WithSubjectID(ctx, p.Subject)
	}
	if sid := p.Claims["sid"]; sid != "" {
		ctx = ctxkeys.WithSessionID(ctx, sid)
	}
	if p.TenantID != "" {
		ctx = ctxkeys.WithTenantID(ctx, p.TenantID)
	}
	return ctx
}

// actorOf returns the acting party for the principal. RFC 8693 §4.1 expresses
// delegation via an "act" claim ("act.sub" = the acting party); GoCell does not
// issue or verify an "act" claim on develop, so for JWT (user) principals there
// is no impersonation chain and the actor is identically the subject-of-record.
// Service principals carry identity via CallerCellID (their Subject is empty —
// see Principal godoc), so the calling cell IS the acting party. When an "act"
// claim is introduced, this is the single place to derive the actor from it.
func actorOf(p *Principal) string {
	if p.Subject != "" {
		return p.Subject
	}
	return p.CallerCellID
}

// writePasswordResetRequired writes a 403 ERR_AUTH_PASSWORD_RESET_REQUIRED
// response through the canonical errcode envelope. changeEndpointHint is
// emitted as details[].key=changePasswordEndpoint when non-empty; empty
// hint produces an empty details array (canonical schema form).
//
// The blocked request's method and path are logged by the call site via
// slog.Info — they are intentionally absent from the response body to avoid
// leaking internal routing information to clients.
//
// The hint is derived by Router.FinalizeAuth from the first declared
// PasswordResetExempt=true + Method=POST AuthRouteMeta, or set directly via
// auth.WithPasswordResetChangeEndpointHintFn.
//
// PR #391 P1-A: details is the canonical array<{key,value}> form per
// contracts/shared/errors/error-response-v1.schema.json. Routing through
// httputil.WriteError keeps wire shape identical to every other 4xx in
// the framework.
func writePasswordResetRequired(ctx context.Context, w http.ResponseWriter, changeEndpointHint string) {
	opts := []errcode.Option{}
	if changeEndpointHint != "" {
		opts = append(opts, errcode.WithDetails(errcode.PublicString("changePasswordEndpoint", changeEndpointHint)))
	}
	httputil.WriteError(ctx, w, errcode.New(
		errcode.KindPermissionDenied,
		errcode.ErrAuthPasswordResetRequired,
		"password reset required before accessing this endpoint",
		opts...,
	))
}

// isPasswordResetExempt reports whether the given HTTP method and URL path are
// exempt from the password-reset enforcement check. The decision is delegated
// to the injected matcher so runtime/auth does not encode cell-specific routes.
// When no matcher is configured (fail-closed), every request is treated as
// non-exempt. See WithPasswordResetExemptMatcher for wiring details.
func isPasswordResetExempt(cfg authConfig, method, urlPath string) bool {
	if cfg.passwordResetExempt == nil {
		return false
	}
	return cfg.passwordResetExempt(method, urlPath)
}

// matchPathTemplate reports whether the concrete path matches the template.
// Template segments of the form {xxx} match any single non-empty path segment
// that does not contain "/". All other segments must match exactly.
//
// Examples:
//
//	matchPathTemplate("/api/v1/users/{id}/password", "/api/v1/users/usr-abc/password") → true
//	matchPathTemplate("/api/v1/users/{id}/password", "/api/v1/users/usr-abc/other")    → false
//	matchPathTemplate("/api/v1/users/{id}/password", "/api/v1/users//password")        → false (empty segment)
func matchPathTemplate(template, concrete string) bool {
	tParts := strings.Split(strings.Trim(template, "/"), "/")
	cParts := strings.Split(strings.Trim(concrete, "/"), "/")
	if len(tParts) != len(cParts) {
		return false
	}
	for i, t := range tParts {
		c := cParts[i]
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
			// Wildcard segment: must be non-empty and must not contain "/"
			// (already guaranteed by the split, but guard empty segment).
			if c == "" {
				return false
			}
			continue
		}
		if t != c {
			return false
		}
	}
	return true
}

// extractBearerTokenWithReason returns the raw Bearer token value and a
// human-readable reason string for observability. When the token is absent the
// reason is one of:
//
//   - "missing"      — no Authorization header at all
//   - "wrong_scheme" — Authorization header present but non-Bearer scheme
//
// When a token is found, reason is "".
func extractBearerTokenWithReason(r *http.Request) (token, reason string) {
	raw := r.Header.Get("Authorization")
	if raw == "" {
		return "", "missing"
	}
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", "wrong_scheme"
	}
	return strings.TrimSpace(parts[1]), ""
}
