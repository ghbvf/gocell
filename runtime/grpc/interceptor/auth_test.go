package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	})

	t.Run("missing metadata -> Unauthenticated", func(t *testing.T) {
		v := stubVerifier{claims: kauth.Claims{Subject: "x"}}
		called := false
		_, err := UnaryAuth(v)(context.Background(), nil, info, okHandler(&called))
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
		}
		if called {
			t.Fatalf("handler must not run on missing auth")
		}
	})

	t.Run("non-bearer scheme -> Unauthenticated", func(t *testing.T) {
		v := stubVerifier{claims: kauth.Claims{Subject: "x"}}
		md := metadata.Pairs(authMetadataKey, "Basic abc")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		called := false
		_, err := UnaryAuth(v)(ctx, nil, info, okHandler(&called))
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
		}
	})

	t.Run("verify infra outage -> Unavailable", func(t *testing.T) {
		v := stubVerifier{err: errcode.New(errcode.KindUnavailable, errcode.ErrInternal, "jwks down")}
		_, err := UnaryAuth(v)(bearerCtx(), nil, info,
			func(context.Context, any) (any, error) { return "ok", nil })
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("code = %v, want Unavailable", status.Code(err))
		}
	})

	t.Run("verify reject -> Unauthenticated", func(t *testing.T) {
		v := stubVerifier{err: errcode.New(errcode.KindUnauthenticated, errcode.ErrInternal, "bad")}
		_, err := UnaryAuth(v)(bearerCtx(), nil, info,
			func(context.Context, any) (any, error) { return "ok", nil })
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
		}
	})

	t.Run("password reset blocks non-exempt method", func(t *testing.T) {
		v := stubVerifier{claims: kauth.Claims{Subject: "u", PasswordResetRequired: true}}
		called := false
		_, err := UnaryAuth(v)(bearerCtx(), nil, info, okHandler(&called))
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
		}
		if called {
			t.Fatalf("handler must not run when reset blocks")
		}
	})

	t.Run("password reset exempt method passes", func(t *testing.T) {
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
	})

	t.Run("public method bypasses auth", func(t *testing.T) {
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
	})

	t.Run("nil verifier fails closed with Internal", func(t *testing.T) {
		called := false
		_, err := UnaryAuth(nil)(bearerCtx(), nil, info, okHandler(&called))
		if status.Code(err) != codes.Internal {
			t.Fatalf("code = %v, want Internal", status.Code(err))
		}
		if called {
			t.Fatalf("handler must not run when misconfigured")
		}
	})
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
