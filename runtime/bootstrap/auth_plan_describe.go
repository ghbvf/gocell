package bootstrap

// auth_plan_describe.go — human-readable summary of a ListenerAuth chain.
//
// THIS IS THE ONLY FILE IN THE ENTIRE CODEBASE ALLOWED TO CONTAIN THE
// STRING LITERALS "jwt", "mtls", "service-token", "verbose-token", or "none"
// as meaningful auth-kind identifiers. All other files must derive descriptions
// via Describe() or describeAuthChain(). The archtest in
// tools/archtest/auth_plan_test.go enforces this invariant at CI time.
//
// ref: zeromicro/go-zero rest/engine.go appendAuthHandler@master — single dispatch point.

import (
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
)

// describeAuthChain returns a human-readable summary of a ListenerAuth chain
// suitable for startup log lines and phase0 error messages. When the chain is
// empty or contains only AuthNone, returns "none". When a single plan is present
// its Describe() value is returned directly. Multiple plans are joined with "+".
//
// Examples:
//
//	[]                                    → "none"
//	[AuthNone{}]                          → "none"
//	[AuthJWT{}]                           → "jwt"
//	[AuthMTLS{}, AuthServiceToken{}]      → "mtls+service-token"
func describeAuthChain(chain []cell.ListenerAuth) string {
	if len(chain) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(chain))
	for _, p := range chain {
		d := p.Describe()
		if d != "" && d != "none" {
			parts = append(parts, d)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "+")
}

// chainProtectsRoutes reports whether the chain is "auth-flavored" — i.e.
// installs middleware that prevents unauthenticated access. AuthNone and an
// empty chain are NOT auth-flavored. Returns true for JWT, mTLS, service-token
// (and combinations thereof).
//
// This replaces the old isAuthFlavoredPolicy string-match helper.
func chainProtectsRoutes(chain []cell.ListenerAuth) bool {
	for _, p := range chain {
		switch p.(type) {
		case cell.AuthJWT, cell.AuthJWTFromAssembly, cell.AuthMTLS, cell.AuthServiceToken:
			return true
		}
	}
	return false
}

// chainContainsAuthMTLS reports whether any plan in the chain is AuthMTLS.
func chainContainsAuthMTLS(chain []cell.ListenerAuth) bool {
	for _, p := range chain {
		if _, ok := p.(cell.AuthMTLS); ok {
			return true
		}
	}
	return false
}

// chainContainsInternalGuard reports whether the listener chain has the
// service-token guard required for /internal/v1/* routes. JWT is intentionally
// excluded: internal routes use caller_cell allowlist via contract.clients
// (enforced by auth.RequireCallerCell). AuthMTLS may be layered with
// service-token, but mTLS alone does not currently establish caller identity.
func chainContainsInternalGuard(chain []cell.ListenerAuth) bool {
	for _, p := range chain {
		if _, ok := p.(cell.AuthServiceToken); ok {
			return true
		}
	}
	return false
}

// explicitAuthNone reports whether the chain contains at least one AuthNone
// entry, distinguishing a deliberate AuthNone{} from a nil/empty chain omission.
// Used by the OPS-07 non-loopback Warn log to help operators distinguish
// "intentionally no auth" from "forgot to wire auth".
func explicitAuthNone(chain []cell.ListenerAuth) bool {
	for _, p := range chain {
		if _, ok := p.(cell.AuthNone); ok {
			return true
		}
	}
	return false
}
