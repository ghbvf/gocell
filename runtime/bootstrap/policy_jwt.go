package bootstrap

import (
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/go-chi/chi/v5"
)

// policyJWT applies JWT authentication middleware via auth.AuthMiddleware.
type policyJWT struct {
	verifier auth.IntentTokenVerifier
	opts     []auth.AuthOption
}

func (p *policyJWT) Describe() string { return "jwt" }

func (p *policyJWT) Apply(mux *chi.Mux) {
	mux.Use(auth.AuthMiddleware(p.verifier, p.opts...))
}

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
func PolicyJWT(v auth.IntentTokenVerifier, opts ...auth.AuthOption) *policyJWT {
	if v == nil {
		panic("bootstrap: PolicyJWT verifier must not be nil")
	}
	return &policyJWT{verifier: v, opts: opts}
}
