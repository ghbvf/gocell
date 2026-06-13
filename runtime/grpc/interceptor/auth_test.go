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

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/runtime/auth"
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

func assertUnaryAuthForwardsPrincipal(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	v := stubVerifier{claims: kauth.Claims{Subject: "user-1"}}
	var gotPrincipal *auth.Principal
	_, err := UnaryAuth(v)(bearerCtx(), nil, info,
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
	_, err := UnaryAuth(v, exempt)(bearerCtx(), nil, info, okHandler(&called))
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
