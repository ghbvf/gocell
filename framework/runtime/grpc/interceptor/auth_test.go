package interceptor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

func bearerCtx() context.Context {
	md := metadata.Pairs(authMetadataKey, "Bearer tok")
	return metadata.NewIncomingContext(context.Background(), md)
}

func okHandler(called *bool) grpc.UnaryHandler {
	return func(context.Context, any) (any, error) {
		*called = true
		return "ok", nil
	}
}

func TestUnaryAuth(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Do"}

	t.Run("valid token forwards principal-enriched ctx", func(t *testing.T) {
		assertUnaryAuthForwardsPrincipal(t, info)
	})

	t.Run("missing metadata -> Unauthenticated", func(t *testing.T) {
		assertUnaryAuthMissingMetadata(t, info)
	})

	t.Run("non-bearer scheme -> Unauthenticated", func(t *testing.T) {
		assertUnaryAuthNonBearerScheme(t, info)
	})

	t.Run("verify infra outage -> Unavailable", func(t *testing.T) {
		assertUnaryAuthVerifyOutage(t, info)
	})

	t.Run("intent mismatch maps to same Unauthenticated as invalid token (enumeration-safe)", func(t *testing.T) {
		assertUnaryAuthIntentMismatchIsEnumerationSafe(t, info)
	})

	t.Run("verify reject -> Unauthenticated", func(t *testing.T) {
		assertUnaryAuthVerifyReject(t, info)
	})

	t.Run("password reset blocks non-exempt method", func(t *testing.T) {
		assertUnaryAuthPasswordResetBlocks(t, info)
	})

	t.Run("password reset exempt method passes", func(t *testing.T) {
		assertUnaryAuthPasswordResetExempt(t, info)
	})

	t.Run("public method bypasses auth", func(t *testing.T) {
		assertUnaryAuthPublicMethodBypasses(t, info)
	})

	t.Run("panicking publicMethod predicate returns Internal (not a raw panic)", func(t *testing.T) {
		assertUnaryAuthPublicMethodPanic(t, info)
	})

	t.Run("panicking verifier returns Internal (panic must not escape Recovery-less auth stage)", func(t *testing.T) {
		assertUnaryAuthVerifierPanic(t, info)
	})

	t.Run("nil verifier panics at construction (fail-fast wiring)", func(t *testing.T) {
		assertUnaryAuthNilVerifierPanics(t)
	})
}

// TestUnaryAuth_WithPublicMethodComposes verifies the OR-compose semantics of
// WithPublicMethod (#1675): multiple installed predicates union — a method is
// public if ANY returns true — rather than last-wins. This is what lets the
// registrar predicate (chain.go) coexist with a test harness's synthetic
// exemption without either clobbering the other.
func TestUnaryAuth_WithPublicMethodComposes(t *testing.T) {
	v := stubVerifier{claims: kauth.Claims{Subject: "u"}}
	predA := WithPublicMethod(func(m string) bool { return m == "/pkg.Svc/A" })
	predB := WithPublicMethod(func(m string) bool { return m == "/pkg.Svc/B" })

	cases := []struct {
		method     string
		wantBypass bool // true → handler reached without a token
	}{
		{"/pkg.Svc/A", true},  // matched by predA
		{"/pkg.Svc/B", true},  // matched by predB — proves predA did not clobber predB
		{"/pkg.Svc/C", false}, // matched by neither → authed (no token → Unauthenticated)
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			info := &grpc.UnaryServerInfo{FullMethod: tc.method}
			called := false
			// No auth metadata: a public method bypasses before token extraction.
			_, err := UnaryAuth(v, predA, predB)(context.Background(), nil, info, okHandler(&called))
			if tc.wantBypass {
				if err != nil || !called {
					t.Fatalf("%s: want public bypass (err=nil, handler reached); got err=%v called=%v", tc.method, err, called)
				}
				return
			}
			if status.Code(err) != codes.Unauthenticated || called {
				t.Fatalf("%s: want Unauthenticated and handler not reached; got err=%v called=%v", tc.method, err, called)
			}
		})
	}
}

// TestUnaryAuth_WithPasswordResetExemptComposes verifies the OR-compose semantics of
// WithPasswordResetExempt (#1382): multiple installed predicates union — a method is
// password-reset-exempt if ANY returns true — rather than last-wins. This is what lets
// the registrar predicate (chain.go) coexist with a test harness's synthetic exemption
// without either clobbering the other.
func TestUnaryAuth_WithPasswordResetExemptComposes(t *testing.T) {
	// Use a principal with PasswordResetRequired:true so the gate is active.
	v := stubVerifier{claims: kauth.Claims{Subject: "u", PasswordResetRequired: true}}
	// Wire a permission + authorizer so the non-public, non-public gate passes after reset.
	perm := WithPermissionResolver(func(m string) (authz.Permission, bool) {
		if m == "/pkg.Svc/A" || m == "/pkg.Svc/B" || m == "/pkg.Svc/C" {
			return authz.PermDeviceCommand(), true
		}
		return authz.Permission{}, false
	})
	authorizer := WithPDPAuthorizer(stubAuthorizer{dec: mustAllow()})
	predA := WithPasswordResetExempt(func(m string) bool { return m == "/pkg.Svc/A" })
	predB := WithPasswordResetExempt(func(m string) bool { return m == "/pkg.Svc/B" })

	cases := []struct {
		method     string
		wantExempt bool // true → handler reached even with reset-required principal
	}{
		{"/pkg.Svc/A", true},  // matched by predA
		{"/pkg.Svc/B", true},  // matched by predB — proves predA did not clobber predB
		{"/pkg.Svc/C", false}, // matched by neither → blocked with PermissionDenied
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			info := &grpc.UnaryServerInfo{FullMethod: tc.method}
			called := false
			_, err := UnaryAuth(v, predA, predB, perm, authorizer)(bearerCtx(), nil, info, okHandler(&called))
			if tc.wantExempt {
				if err != nil || !called {
					t.Fatalf("%s: want exempt bypass (err=nil, handler reached); got err=%v called=%v", tc.method, err, called)
				}
				return
			}
			if status.Code(err) != codes.PermissionDenied || called {
				t.Fatalf("%s: want PermissionDenied and handler not reached; got err=%v called=%v", tc.method, err, called)
			}
		})
	}
}

// TestUnaryAuth_PermissionGate exercises the #2008 per-method PDP gate that runs
// after authentication on the non-public path. It is the gRPC analog of the HTTP
// RequirePermission decision table, asserting fail-closed at every step. All cases
// use a valid bearer token (authentication succeeds) so the gate is what decides.
func TestUnaryAuth_PermissionGate(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}
	validVerifier := stubVerifier{claims: kauth.Claims{Subject: "user-1"}}
	resolver := WithPermissionResolver(permResolverFor(method))

	cases := []struct {
		name       string
		opts       []AuthOption
		wantCode   codes.Code // codes.OK means "permit, handler reached"
		wantCalled bool
	}{
		{
			name:       "allow with zero obligations permits",
			opts:       []AuthOption{resolver, WithPDPAuthorizer(stubAuthorizer{dec: mustAllow()})},
			wantCode:   codes.OK,
			wantCalled: true,
		},
		{
			name:     "deny -> PermissionDenied",
			opts:     []AuthOption{resolver, WithPDPAuthorizer(stubAuthorizer{dec: authz.Deny("test-deny")})},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "no permission mapping for method -> PermissionDenied (strict fail-closed)",
			opts:     []AuthOption{WithPermissionResolver(permResolverFor("/pkg.Svc/Other")), WithPDPAuthorizer(stubAuthorizer{dec: mustAllow()})},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "nil resolver -> PermissionDenied (no mapping)",
			opts:     []AuthOption{WithPDPAuthorizer(stubAuthorizer{dec: mustAllow()})},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "authorizer not wired -> PermissionDenied",
			opts:     []AuthOption{resolver},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "allow with non-zero obligation -> PermissionDenied (F5 parity)",
			opts:     []AuthOption{resolver, WithPDPAuthorizer(stubAuthorizer{dec: allowWithObligation()})},
			wantCode: codes.PermissionDenied,
		},
		{
			name: "PDP unavailable error -> Unavailable",
			opts: []AuthOption{resolver, WithPDPAuthorizer(stubAuthorizer{
				err: errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "policy store down"),
			})},
			wantCode: codes.Unavailable,
		},
		{
			name:     "PDP other error -> PermissionDenied",
			opts:     []AuthOption{resolver, WithPDPAuthorizer(stubAuthorizer{err: errcode.New(errcode.KindInternal, errcode.ErrInternal, "boom")})},
			wantCode: codes.PermissionDenied,
		},
		{
			name:     "PDP panic -> Internal (collapsed by the auth stage guard)",
			opts:     []AuthOption{resolver, WithPDPAuthorizer(stubAuthorizer{panicVal: "pdp exploded"})},
			wantCode: codes.Internal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			_, err := UnaryAuth(validVerifier, tc.opts...)(bearerCtx(), nil, info, okHandler(&called))
			if tc.wantCode == codes.OK {
				if err != nil {
					t.Fatalf("want permit, got err=%v (code=%v)", err, status.Code(err))
				}
			} else if status.Code(err) != tc.wantCode {
				t.Fatalf("code = %v, want %v (err=%v)", status.Code(err), tc.wantCode, err)
			}
			if called != tc.wantCalled {
				t.Fatalf("handler called = %v, want %v", called, tc.wantCalled)
			}
		})
	}
}

// TestUnaryAuth_PermissionGate_LogsDeny asserts the PDP gate emits a structured
// log on deny (the observability parity with HTTP enforcePermission): an operator
// triaging a denial can recover subject / method / permission / reason from the log
// without reconstructing it from the access log (which carries only the status code).
func TestUnaryAuth_PermissionGate_LogsDeny(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}

	var buf bytes.Buffer
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	_, err := UnaryAuth(stubVerifier{claims: kauth.Claims{Subject: "user-1"}},
		WithPermissionResolver(permResolverFor(method)),
		WithPDPAuthorizer(stubAuthorizer{dec: authz.Deny("test-deny-reason")}),
	)(bearerCtx(), nil, info, okHandler(new(bool)))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
	got := buf.String()
	wantFields := []string{
		"permission denied by PDP",
		`"method":"/pkg.Svc/Do"`,
		`"subject":"user-1"`,
		`"permission":"device:command"`,
		`"reason":"test-deny-reason"`,
	}
	for _, want := range wantFields {
		if !strings.Contains(got, want) {
			t.Errorf("deny log missing %q; got: %s", want, got)
		}
	}
}

func assertUnaryAuthForwardsPrincipal(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "user-1"}}
	var gotPrincipal *auth.Principal
	// #2008: a non-public authed method now also passes the PDP gate before the
	// handler runs, so wire a permitting resolver + authorizer to reach it.
	_, err := UnaryAuth(v,
		WithPermissionResolver(permResolverFor(info.FullMethod)),
		WithPDPAuthorizer(stubAuthorizer{dec: mustAllow()}),
	)(bearerCtx(), nil, info,
		func(ctx context.Context, _ any) (any, error) {
			p, _ := auth.FromContext(ctx)
			gotPrincipal = p
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if gotPrincipal == nil || gotPrincipal.Subject != "user-1" {
		t.Fatalf("principal = %+v, want user-1", gotPrincipal)
	}
}

func assertUnaryAuthMissingMetadata(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "x"}}
	called := false
	_, err := UnaryAuth(v)(context.Background(), nil, info, okHandler(&called))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
	if called {
		t.Fatalf("handler must not run on missing auth")
	}
}

func assertUnaryAuthNonBearerScheme(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "x"}}
	md := metadata.Pairs(authMetadataKey, "Basic abc")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	called := false
	_, err := UnaryAuth(v)(ctx, nil, info, okHandler(&called))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func assertUnaryAuthVerifyOutage(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{err: errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "jwks down")}
	_, err := UnaryAuth(v)(bearerCtx(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil })
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", status.Code(err))
	}
}

func assertUnaryAuthIntentMismatchIsEnumerationSafe(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	intentErr := stubVerifier{err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent, "wrong intent")}
	plainErr := stubVerifier{err: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "bad signature")}
	pass := func(context.Context, any) (any, error) { return "ok", nil }

	_, e1 := UnaryAuth(intentErr)(bearerCtx(), nil, info, pass)
	_, e2 := UnaryAuth(plainErr)(bearerCtx(), nil, info, pass)
	s1, s2 := status.Convert(e1), status.Convert(e2)
	if s1.Code() != codes.Unauthenticated || s2.Code() != codes.Unauthenticated {
		t.Fatalf("codes = %v / %v, want both Unauthenticated", s1.Code(), s2.Code())
	}
	if s1.Message() != s2.Message() {
		t.Fatalf("messages differ (%q vs %q): token-type leak via gRPC status", s1.Message(), s2.Message())
	}
}

func assertUnaryAuthVerifyReject(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{err: errcode.New(errcode.KindUnauthenticated, errcode.ErrInternal, "bad")}
	_, err := UnaryAuth(v)(bearerCtx(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func assertUnaryAuthPasswordResetBlocks(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "u", PasswordResetRequired: true}}
	called := false
	_, err := UnaryAuth(v)(bearerCtx(), nil, info, okHandler(&called))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
	}
	if called {
		t.Fatalf("handler must not run when reset blocks")
	}
}

func assertUnaryAuthPasswordResetExempt(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "u", PasswordResetRequired: true}}
	called := false
	exempt := WithPasswordResetExempt(func(m string) bool { return m == info.FullMethod })
	// #2008: past the reset gate, the non-public method still passes the PDP gate.
	_, err := UnaryAuth(v, exempt,
		WithPermissionResolver(permResolverFor(info.FullMethod)),
		WithPDPAuthorizer(stubAuthorizer{dec: mustAllow()}),
	)(bearerCtx(), nil, info, okHandler(&called))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !called {
		t.Fatalf("handler must run for exempt method")
	}
}

func assertUnaryAuthPublicMethodBypasses(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "u"}}
	called := false
	pub := WithPublicMethod(func(m string) bool { return true })
	// No auth metadata at all; public predicate must bypass before extraction.
	_, err := UnaryAuth(v, pub)(context.Background(), nil, info, okHandler(&called))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !called {
		t.Fatalf("handler must run for public method")
	}
}

func assertUnaryAuthPublicMethodPanic(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "u"}}
	panicPred := WithPublicMethod(func(m string) bool { panic("predicate exploded") })
	_, err := UnaryAuth(v, panicPred)(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil })
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal (predicate panic must not escape)", status.Code(err))
	}
}

func assertUnaryAuthVerifierPanic(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	var buf bytes.Buffer
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	called := false
	_, err := UnaryAuth(panicVerifier{val: "verifier exploded"})(bearerCtx(), nil, info, okHandler(&called))
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal (verifier panic must not escape the Recovery-less auth stage)", status.Code(err))
	}
	if called {
		t.Fatalf("handler must not run when the verifier panics")
	}
	// stage=auth lets operators distinguish an auth-stage panic (e.g. a verifier
	// defect) from a business handler panic without parsing the stack.
	if !strings.Contains(buf.String(), `"stage":"auth"`) {
		t.Fatalf("auth-stage panic log must carry stage=auth: %s", buf.String())
	}
}

func assertUnaryAuthNilVerifierPanics(t *testing.T) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("UnaryAuth(nil) must panic at construction (security dep is required)")
		}
	}()
	_ = UnaryAuth(nil)
}

func TestBearerFromMetadata(t *testing.T) {
	tests := []struct {
		name   string
		md     metadata.MD
		want   string
		wantOK bool
	}{
		{name: "valid bearer", md: metadata.Pairs(authMetadataKey, "Bearer abc"), want: "abc", wantOK: true},
		{name: "case-insensitive scheme", md: metadata.Pairs(authMetadataKey, "bearer abc"), want: "abc", wantOK: true},
		{name: "wrong scheme", md: metadata.Pairs(authMetadataKey, "Basic abc"), wantOK: false},
		{name: "empty token", md: metadata.Pairs(authMetadataKey, "Bearer  "), wantOK: false},
		{name: "no key", md: metadata.Pairs("other", "x"), wantOK: false},
		// F8: multiple authorization values are ambiguous — reject.
		{name: "multiple authorization values", md: metadata.MD{authMetadataKey: []string{"Bearer tok1", "Bearer tok2"}}, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(context.Background(), tt.md)
			got, ok := bearerFromMetadata(ctx)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("bearerFromMetadata = (%q,%v), want (%q,%v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
