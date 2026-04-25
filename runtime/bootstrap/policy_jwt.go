package bootstrap

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// PolicyJWT returns a cell.Policy that installs JWT authentication middleware
// using the provided IntentTokenVerifier.
//
// Fail-fast: a nil verifier panics at construction time with the message
// "bootstrap: PolicyJWT verifier must not be nil". This mirrors the existing
// fail-fast pattern used by other bootstrap constructors — programmer errors
// at composition-root setup time should surface immediately rather than
// silently producing a broken auth layer at request time.
//
// ref: go-kratos/kratos transport/http/server.go — middleware injected at server build time.
// ref: zeromicro/go-zero rest/server.go — WithJwt at server option level, fail-fast on empty secret.
func PolicyJWT(v auth.IntentTokenVerifier, opts ...auth.AuthOption) cell.Policy {
	if v == nil {
		panic("bootstrap: PolicyJWT verifier must not be nil; use PolicyJWTFromAssembly(asm) to discover from an authProvider cell")
	}
	return cell.Policy{
		Name: "jwt",
		Middleware: func(next http.Handler) http.Handler {
			return auth.AuthMiddleware(v, opts...)(next)
		},
	}
}
