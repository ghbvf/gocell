package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// bearerCoreVerifier is a minimal IntentTokenVerifier stub for exercising the
// transport-agnostic AuthenticateBearer core in isolation.
type bearerCoreVerifier struct {
	claims Claims
	err    error
}

func (v bearerCoreVerifier) VerifyIntent(_ context.Context, _ string, _ TokenIntent) (Claims, error) {
	return v.claims, v.err
}

func TestAuthenticateBearer(t *testing.T) {
	t.Run("success injects principal and ctxkeys", func(t *testing.T) {
		v := bearerCoreVerifier{claims: Claims{Subject: "user-1", SessionID: "sess-9"}}

		ctx, p, err := AuthenticateBearer(context.Background(), v, "tok")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p == nil || p.Subject != "user-1" {
			t.Fatalf("principal subject = %+v, want user-1", p)
		}
		if got, ok := FromContext(ctx); !ok || got.Subject != "user-1" {
			t.Fatalf("FromContext = %+v ok=%v, want user-1", got, ok)
		}
		if actor, ok := ctxkeys.ActorIDFrom(ctx); !ok || actor != "user-1" {
			t.Fatalf("actor ctxkey = %q ok=%v, want user-1", actor, ok)
		}
		if sub, ok := ctxkeys.SubjectIDFrom(ctx); !ok || sub != "user-1" {
			t.Fatalf("subject ctxkey = %q ok=%v, want user-1", sub, ok)
		}
		if sid, ok := ctxkeys.SessionIDFrom(ctx); !ok || sid != "sess-9" {
			t.Fatalf("session ctxkey = %q ok=%v, want sess-9", sid, ok)
		}
	})

	t.Run("verify failure returns ctx unchanged and nil principal", func(t *testing.T) {
		sentinel := errors.New("verify boom")
		v := bearerCoreVerifier{err: sentinel}
		base := context.Background()

		ctx, p, err := AuthenticateBearer(base, v, "tok")
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want sentinel", err)
		}
		if p != nil {
			t.Fatalf("principal = %+v, want nil on failure", p)
		}
		// ctx returned unchanged: no principal must leak from a failed verify.
		if _, ok := FromContext(ctx); ok {
			t.Fatalf("principal leaked into ctx after verify failure")
		}
	})
}

func TestAuthenticateBearer_EmptySubject(t *testing.T) {
	// G1.A: a JWT whose "sub" claim is empty is a signing bug / OIDC
	// misconfiguration. AuthenticateBearer must reject it before injecting any
	// principal or ctxkeys, keeping the input ctx unmodified (matching the
	// verify-failure contract documented in the AuthenticateBearer godoc).
	v := bearerCoreVerifier{claims: Claims{Subject: ""}} // user JWT with empty sub
	base := context.Background()

	ctx, p, err := AuthenticateBearer(base, v, "tok")
	if err == nil {
		t.Fatal("expected non-nil error for empty subject, got nil")
	}
	if p != nil {
		t.Fatalf("principal = %+v, want nil on empty-subject rejection", p)
	}
	// ctx must be the input ctx — no principal or ctxkeys must leak.
	if _, ok := FromContext(ctx); ok {
		t.Fatal("principal leaked into ctx after empty-subject rejection")
	}
	if _, ok := ctxkeys.ActorIDFrom(ctx); ok {
		t.Fatal("actor ctxkey leaked into ctx after empty-subject rejection")
	}
	if _, ok := ctxkeys.SubjectIDFrom(ctx); ok {
		t.Fatal("subject ctxkey leaked into ctx after empty-subject rejection")
	}
}

func TestPasswordResetBlocked(t *testing.T) {
	tests := []struct {
		name   string
		p      *Principal
		exempt bool
		want   bool
	}{
		{name: "reset required, not exempt", p: &Principal{PasswordResetRequired: true}, exempt: false, want: true},
		{name: "reset required, exempt", p: &Principal{PasswordResetRequired: true}, exempt: true, want: false},
		{name: "no reset", p: &Principal{PasswordResetRequired: false}, exempt: false, want: false},
		{name: "nil principal", p: nil, exempt: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PasswordResetBlocked(tt.p, tt.exempt); got != tt.want {
				t.Fatalf("PasswordResetBlocked = %v, want %v", got, tt.want)
			}
		})
	}
}
