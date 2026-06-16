package interceptor

import (
	"context"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// recordingTracer / recordingSpan capture span lifecycle for assertions.
type recordingTracer struct {
	span *recordingSpan
}

func (t *recordingTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	t.span.name = name
	t.span.SetAttributes(attrs...)
	return ctx, t.span
}

type recordingSpan struct {
	name        string
	attrs       []wrapper.Attr
	recordedErr error
	statusCode  wrapper.StatusCode
	statusDesc  string
	ended       bool
}

func (s *recordingSpan) SetAttributes(a ...wrapper.Attr)          { s.attrs = append(s.attrs, a...) }
func (s *recordingSpan) RecordError(err error)                    { s.recordedErr = err }
func (s *recordingSpan) SetStatus(c wrapper.StatusCode, d string) { s.statusCode = c; s.statusDesc = d }
func (s *recordingSpan) End()                                     { s.ended = true }

func (s *recordingSpan) attr(key string) (any, bool) {
	for _, a := range s.attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return nil, false
}

// stubVerifier is a configurable IntentTokenVerifier for auth tests.
type stubVerifier struct {
	claims kauth.Claims
	err    error
}

func (v stubVerifier) VerifyIntent(_ context.Context, _ string, _ kauth.TokenIntent) (kauth.Claims, error) {
	return v.claims, v.err
}

// panicVerifier panics inside VerifyIntent to exercise authorize's stage-level
// panic guard: the Auth interceptor runs OUTSIDE Recovery, so a verifier that
// panics must be collapsed into codes.Internal rather than escaping the chain.
type panicVerifier struct{ val any }

func (v panicVerifier) VerifyIntent(context.Context, string, kauth.TokenIntent) (kauth.Claims, error) {
	panic(v.val)
}

// stubAuthorizer is a configurable auth.Authorizer for the #2008 PDP gate tests.
// It satisfies runtime/auth.Authorizer structurally (no import needed). A non-nil
// panicVal makes Authorize panic, exercising the gate's coverage by authorize's
// stage-level panic guard.
type stubAuthorizer struct {
	dec      authz.Decision
	err      error
	panicVal any
}

func (a stubAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	if a.panicVal != nil {
		panic(a.panicVal)
	}
	return a.dec, a.err
}

// mustAllow builds a zero-obligation Allow decision (the normal permit path).
func mustAllow() authz.Decision {
	d, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic(err)
	}
	return d
}

// allowWithObligation builds an Allow carrying a non-zero (FieldMask) obligation,
// to exercise the gate's HTTP-F5-parity fail-closed-on-obligation path.
func allowWithObligation() authz.Decision {
	d, err := authz.Allow(authz.Obligations{FieldMask: authz.FieldMask{Fields: []string{"secret"}}})
	if err != nil {
		panic(err)
	}
	return d
}

// permResolverFor returns a PermissionResolver mapping exactly method → a fixed
// permission (PermDeviceCommand), reporting ok=false for any other method — the
// test analog of the registrar's PermissionForMethod.
func permResolverFor(method string) PermissionResolver {
	return func(m string) (authz.Permission, bool) {
		if m == method {
			return authz.PermDeviceCommand(), true
		}
		return authz.Permission{}, false
	}
}

// roleGateAuthorizer mimics a real cell PDP for the production-path e2e: it reads
// the principal from ctx (as the real Authorizer does) and allows only when the
// principal holds the given role, else denies. The test drives allow/deny via the
// principal's roles (set by the verifier), exercising the full chain rather than a
// fixed decision.
type roleGateAuthorizer struct{ role string }

func (a roleGateAuthorizer) Authorize(ctx context.Context, _, _, _ string) (authz.Decision, error) {
	if p, ok := auth.FromContext(ctx); ok && p != nil && p.HasRole(a.role) {
		return authz.Allow(authz.Obligations{})
	}
	return authz.Deny("role-gate: missing role"), nil
}

// rolesFromTokenVerifier is a test IntentTokenVerifier that treats the bearer token
// string as a single role on the returned principal. One server can then exercise
// both allow and deny by varying the client's token (e.g. "operator" vs "guest")
// without minting real JWTs.
type rolesFromTokenVerifier struct{}

func (rolesFromTokenVerifier) VerifyIntent(_ context.Context, token string, _ kauth.TokenIntent) (kauth.Claims, error) {
	return kauth.Claims{Subject: "subj-" + token, Roles: []string{token}}, nil
}
