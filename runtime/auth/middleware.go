package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
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
func AuthMiddleware(verifier IntentTokenVerifier, opts ...AuthOption) func(http.Handler) http.Handler {
	cfg := defaultAuthConfig()
	for _, o := range opts {
		o(&cfg)
	}
	clock.MustHaveClock(cfg.clock, "auth.AuthMiddleware")

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
		cfg.metrics.recordTokenVerifyCounter("failure", reason)
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "missing or invalid authorization header"))
		return
	}

	start := cfg.clock.Now()
	claims, err := verifier.VerifyIntent(r.Context(), token, TokenIntentAccess)
	if err != nil {
		cfg.metrics.recordTokenVerify("failure", classifyTokenError(err), cfg.clock.Since(start))
		// S43: expected 4xx (invalid/expired token, unauthorized) → Warn;
		// infra errors (key load failure, verifier init error) → Error.
		if errcode.IsExpected4xx(err) {
			cfg.logger.Warn("token verification failed",
				slog.Any("error", err),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
			)
		} else {
			cfg.logger.Error("token verification failed",
				slog.Any("error", err),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
			)
		}
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "invalid token"))
		return
	}
	cfg.metrics.recordTokenVerify("success", "ok", cfg.clock.Since(start))

	// Password-reset enforcement: when the token carries password_reset_required=true,
	// only exempt endpoints (supplied by the composition root via
	// WithPasswordResetExemptMatcher) are allowed. All other business routes
	// receive 403 ERR_AUTH_PASSWORD_RESET_REQUIRED until the subject changes
	// their password and obtains a new token without the claim. If no matcher
	// is wired, the gate rejects every request — fail-closed default.
	if claims.PasswordResetRequired && !isPasswordResetExempt(cfg, r.Method, r.URL.Path) {
		cfg.logger.Info("auth: password reset required gate blocked request",
			slog.String("subject", claims.Subject),
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

	// Inject the unified Principal (F7 wiring).
	p := jwtClaimsToPrincipal(claims)
	ctx := WithPrincipal(r.Context(), p)
	ctx = withLogger(ctx, cfg.logger)
	next.ServeHTTP(w, r.WithContext(ctx))
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
		opts = append(opts, errcode.WithDetails(slog.String("changePasswordEndpoint", changeEndpointHint)))
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

// RequireRole checks that the authenticated subject has at least one of the
// specified roles. The AuthMiddleware must run before this middleware.
// On failure, it returns a 403 JSON response.
func RequireRole(authorizer Authorizer, roles ...string) func(http.Handler) http.Handler {
	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleRequireRole(authorizer, roles, roleSet, next, w, r)
		})
	}
}

func handleRequireRole(
	authorizer Authorizer, roles []string, roleSet map[string]bool,
	next http.Handler, w http.ResponseWriter, r *http.Request,
) {
	p, ok := FromContext(r.Context())
	if !ok {
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required"))
		return
	}

	if hasMatchingRoleList(p.Roles, roleSet) {
		next.ServeHTTP(w, r)
		return
	}

	if authorizer != nil {
		allowed, err := checkAuthorizer(authorizer, r, p.Subject, roles)
		if err != nil {
			loggerFrom(r.Context()).Error("authorization check failed",
				slog.Any("error", err),
				slog.String("subject", p.Subject),
			)
			httputil.WriteError(r.Context(), w,
				errcode.New(errcode.KindInternal, errcode.ErrInternal, "internal server error"))
			return
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
	}

	loggerFrom(r.Context()).Info("authorization role check failed",
		slog.String("subject", p.Subject),
		slog.String("path", r.URL.Path),
		slog.Any("required_roles", roles),
	)
	httputil.WriteError(r.Context(), w,
		errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "insufficient permissions"))
}

func hasMatchingRoleList(roleList []string, roleSet map[string]bool) bool {
	for _, role := range roleList {
		if roleSet[role] {
			return true
		}
	}
	return false
}

func checkAuthorizer(authorizer Authorizer, r *http.Request, subject string, roles []string) (bool, error) {
	for _, role := range roles {
		allowed, err := authorizer.Authorize(r.Context(), subject, r.URL.Path, role)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
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

func extractBearerToken(r *http.Request) string {
	token, _ := extractBearerTokenWithReason(r)
	return token
}
