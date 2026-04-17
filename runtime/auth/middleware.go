package auth

import (
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
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
// verifies it using the provided TokenVerifier, and stores the resulting
// Claims in the request context. On failure, it returns a 401 JSON response.
//
// publicEndpoints specifies paths that bypass authentication. If nil,
// DefaultPublicEndpoints is used. Paths are normalized via path.Clean before
// matching, consistent with other security middleware in this package.
func AuthMiddleware(verifier TokenVerifier, publicEndpoints []string, opts ...AuthOption) func(http.Handler) http.Handler {
	cfg := defaultAuthConfig()
	for _, o := range opts {
		o(&cfg)
	}

	publicPaths := publicEndpoints
	if publicPaths == nil {
		publicPaths = DefaultPublicEndpoints
	}
	publicSet := make(map[string]bool, len(publicPaths))
	for _, p := range publicPaths {
		publicSet[path.Clean(p)] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip authentication for public endpoints.
			if publicSet[path.Clean(r.URL.Path)] {
				next.ServeHTTP(w, r)
				return
			}

			token := extractBearerToken(r)
			if token == "" {
				cfg.metrics.recordTokenVerifyCounter("failure", "missing")
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing or invalid authorization header")
				return
			}

			start := time.Now()
			claims, err := verifier.Verify(r.Context(), token)
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

			ctx := WithClaims(r.Context(), claims)
			ctx = ctxkeys.WithSubject(ctx, claims.Subject)
			ctx = withLogger(ctx, cfg.logger)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
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
			claims, ok := ClaimsFrom(r.Context())
			if !ok {
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "authentication required")
				return
			}

			// Check if any of the user's roles match the required roles.
			for _, role := range claims.Roles {
				if roleSet[role] {
					next.ServeHTTP(w, r)
					return
				}
			}

			// If an Authorizer is provided, try policy-based authorization.
			if authorizer != nil {
				sub := claims.Subject
				for _, role := range roles {
					allowed, err := authorizer.Authorize(r.Context(), sub, r.URL.Path, role)
					if err != nil {
						loggerFrom(r.Context()).Error("authorization check failed",
							"error", err,
							"subject", sub,
						)
						httputil.WriteError(r.Context(), w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
						return
					}
					if allowed {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			httputil.WriteError(r.Context(), w, http.StatusForbidden, "ERR_AUTH_FORBIDDEN", "insufficient permissions")
		})
	}
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
