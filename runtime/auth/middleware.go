package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// DefaultPublicEndpoints is intentionally empty. Public route policy must be
// declared at the composition root (main.go / bootstrap call site), not in
// runtime/auth. Callers pass explicit publicEndpoints to WithAuthMiddleware.
//
// Infra endpoints (/healthz, /readyz, /metrics) bypass auth via the router's
// outerMux architecture and do not need to be listed here.
//
// ref: go-kratos/kratos — public bypass via selector at composition layer
// ref: go-zero — JWT opt-in per route group, no hidden runtime defaults
var DefaultPublicEndpoints = []string{}

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
// publicEndpoints specifies paths that bypass authentication. If nil,
// DefaultPublicEndpoints is used. Paths are normalized via path.Clean before
// matching, consistent with other security middleware in this package.
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
// publicEndpoints specifies paths that bypass authentication. If nil,
// DefaultPublicEndpoints is used. Paths are normalized via path.Clean before
// matching, consistent with other security middleware in this package.
//
// Pass WithPublicEndpointMatcher(fn) to use a method-aware compiled predicate
// instead of the []string path-only list. When the matcher is provided, the
// publicEndpoints parameter is ignored for bypass decisions.
func AuthMiddleware(verifier IntentTokenVerifier, publicEndpoints []string, opts ...AuthOption) func(http.Handler) http.Handler {
	cfg := defaultAuthConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// Build the bypass check. Prefer the compiled method-aware matcher (set via
	// WithPublicEndpointMatcher) over the legacy path-only []string approach.
	var isPublic func(*http.Request) bool
	if cfg.publicMatcher != nil {
		// Method-aware path: used when wired through router.WithPublicEndpoints.
		isPublic = cfg.publicMatcher
	} else {
		// Legacy path-only path: used by direct callers of WithAuthMiddleware.
		publicPaths := publicEndpoints
		if publicPaths == nil {
			publicPaths = DefaultPublicEndpoints
		}

		// Defense-in-depth: detect callers that accidentally pass "METHOD /path" format
		// to the legacy []string path (which does path.Clean on the whole string and
		// never matches). Panic to fail-fast — caller should use
		// router.WithPublicEndpoints instead (which compiles into publicMatcher).
		for _, p := range publicPaths {
			if strings.ContainsRune(p, ' ') {
				panic(fmt.Sprintf(
					"auth.AuthMiddleware: legacy publicEndpoints entry %q looks like METHOD /path format; "+
						"pass it through router.WithPublicEndpoints (which sets WithPublicEndpointMatcher) instead",
					p))
			}
		}

		publicSet := make(map[string]bool, len(publicPaths))
		for _, p := range publicPaths {
			publicSet[path.Clean(p)] = true
		}
		isPublic = func(r *http.Request) bool {
			return publicSet[path.Clean(r.URL.Path)]
		}
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
// next with claims attached or writes a 401 response.
//
// Enumeration defense: all verification failures — including
// ErrAuthInvalidTokenIntent — are mapped to the generic ERR_AUTH_UNAUTHORIZED
// response code so clients cannot distinguish token-type mismatch from
// signature invalidity or expiry. The specific failure reason is observable
// via the "reason" label on the auth_token_verify_total metric (ops-only
// signal) and in structured logs; it is never forwarded to the HTTP response.
func handleAuthRequest(w http.ResponseWriter, r *http.Request, next http.Handler, verifier IntentTokenVerifier, cfg authConfig) {
	token := extractBearerToken(r)
	if token == "" {
		cfg.metrics.recordTokenVerifyCounter("failure", "missing")
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing or invalid authorization header")
		return
	}

	start := time.Now()
	claims, err := verifier.VerifyIntent(r.Context(), token, TokenIntentAccess)
	if err != nil {
		cfg.metrics.recordTokenVerify("failure", classifyTokenError(err), time.Since(start))
		cfg.logger.Error("token verification failed",
			"error", err,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
		)
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid token")
		return
	}
	cfg.metrics.recordTokenVerify("success", "ok", time.Since(start))

	// Password-reset enforcement: when the token carries password_reset_required=true,
	// only the exempt endpoints (change-password, logout) are allowed. All other
	// business routes receive 403 ERR_AUTH_PASSWORD_RESET_REQUIRED until the
	// subject changes their password and obtains a new token without the claim.
	if claims.PasswordResetRequired && !isPasswordResetExempt(r.Method, r.URL.Path) {
		writePasswordResetRequired(r.Context(), w)
		return
	}

	ctx := WithClaims(r.Context(), claims)
	ctx = ctxkeys.WithSubject(ctx, claims.Subject)
	ctx = withLogger(ctx, cfg.logger)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// writePasswordResetRequired writes a 403 ERR_AUTH_PASSWORD_RESET_REQUIRED
// response with a details.change_password_endpoint hint to help clients
// navigate to the correct endpoint (P2-10 fix).
func writePasswordResetRequired(ctx context.Context, w http.ResponseWriter) {
	errBody := map[string]any{
		"code":    string(errcode.ErrAuthPasswordResetRequired),
		"message": "password reset required before accessing this endpoint",
		"details": map[string]any{
			"change_password_endpoint": "POST /api/v1/access/users/{id}/password",
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	if err := json.NewEncoder(w).Encode(map[string]any{"error": errBody}); err != nil {
		slog.Error("auth middleware: encode password-reset-required response",
			slog.Any("error", err))
	}
}

// isPasswordResetExempt reports whether the given HTTP method and URL path are
// exempt from the password-reset enforcement check. Only two endpoints are
// exempt:
//   - POST /api/v1/access/users/{id}/password — change password
//   - DELETE /api/v1/access/sessions/{id}    — logout
//
// Path matching uses matchPathTemplate, which treats {xxx} segments as
// single-segment wildcards (no "/" allowed within a wildcard).
func isPasswordResetExempt(method, urlPath string) bool {
	switch {
	case method == http.MethodPost &&
		matchPathTemplate("/api/v1/access/users/{id}/password", urlPath):
		return true
	case method == http.MethodDelete &&
		matchPathTemplate("/api/v1/access/sessions/{id}", urlPath):
		return true
	default:
		return false
	}
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

func handleRequireRole(authorizer Authorizer, roles []string, roleSet map[string]bool, next http.Handler, w http.ResponseWriter, r *http.Request) {
	claims, ok := ClaimsFrom(r.Context())
	if !ok {
		httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "authentication required")
		return
	}

	if hasMatchingRole(claims, roleSet) {
		next.ServeHTTP(w, r)
		return
	}

	if authorizer != nil {
		allowed, err := checkAuthorizer(authorizer, r, claims.Subject, roles)
		if err != nil {
			loggerFrom(r.Context()).Error("authorization check failed",
				"error", err,
				"subject", claims.Subject,
			)
			httputil.WriteError(r.Context(), w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
			return
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
	}

	httputil.WriteError(r.Context(), w, http.StatusForbidden, "ERR_AUTH_FORBIDDEN", "insufficient permissions")
}

func hasMatchingRole(claims Claims, roleSet map[string]bool) bool {
	for _, role := range claims.Roles {
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

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
