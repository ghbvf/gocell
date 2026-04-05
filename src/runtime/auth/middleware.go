package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// DefaultPublicEndpoints is the default set of paths that do not require
// authentication.
var DefaultPublicEndpoints = []string{
	"/healthz",
	"/readyz",
	"/api/v1/auth/login",
	"/api/v1/auth/callback",
}

// AuthMiddleware extracts a Bearer token from the Authorization header,
// verifies it using the provided TokenVerifier, and stores the resulting
// Claims in the request context. On failure, it returns a 401 JSON response.
//
// publicEndpoints specifies paths that bypass authentication. If nil,
// DefaultPublicEndpoints is used.
func AuthMiddleware(verifier TokenVerifier, publicEndpoints []string) func(http.Handler) http.Handler {
	whitelist := publicEndpoints
	if whitelist == nil {
		whitelist = DefaultPublicEndpoints
	}
	publicSet := make(map[string]bool, len(whitelist))
	for _, p := range whitelist {
		publicSet[p] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip authentication for whitelisted endpoints.
			if publicSet[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			token := extractBearerToken(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "missing or invalid authorization header")
				return
			}

			claims, err := verifier.Verify(r.Context(), token)
			if err != nil {
				slog.Warn("token verification failed",
					slog.Any("error", err),
					slog.String("path", r.URL.Path),
				)
				writeAuthError(w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "invalid token")
				return
			}

			ctx := WithClaims(r.Context(), claims)
			ctx = ctxkeys.WithSubject(ctx, claims.Subject)
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
				writeAuthError(w, http.StatusUnauthorized, "ERR_AUTH_UNAUTHORIZED", "authentication required")
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
						slog.Error("authorization check failed",
							slog.Any("error", err),
							slog.String("subject", sub),
						)
						writeAuthError(w, http.StatusInternalServerError, "ERR_INTERNAL", "authorization check failed")
						return
					}
					if allowed {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			writeAuthError(w, http.StatusForbidden, "ERR_AUTH_FORBIDDEN", "insufficient permissions")
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

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
