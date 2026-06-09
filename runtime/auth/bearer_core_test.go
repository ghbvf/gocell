package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
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

func TestNewBearerHeaderAuthenticator(t *testing.T) {
	exp := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	claims := Claims{
		Subject:               "user-1",
		Roles:                 []string{"admin", "viewer"},
		SessionID:             "sess-9",
		Issuer:                "gocell-issuer",
		TokenUse:              TokenIntentAccess,
		TenantID:              tenantClaimUUID,
		PasswordResetRequired: true,
		ExpiresAt:             exp,
	}

	t.Run("success delegates to shared bearer core", func(t *testing.T) {
		runBearerHeaderSuccess(t, claims, exp)
	})

	t.Run("missing header is absent", func(t *testing.T) {
		runBearerHeaderMissing(t)
	})

	t.Run("wrong scheme is absent", func(t *testing.T) {
		runBearerHeaderWrongScheme(t)
	})

	t.Run("verify failure returns error and nil principal", func(t *testing.T) {
		runBearerHeaderVerifyFailure(t)
	})

	t.Run("empty subject is rejected by shared bearer core", func(t *testing.T) {
		runBearerHeaderEmptySubject(t)
	})
}

func TestAuthenticateBearer(t *testing.T) {
	t.Run("success injects principal and ctxkeys", func(t *testing.T) {
		runAuthenticateBearerSuccess(t)
	})

	t.Run("verify failure returns ctx unchanged and nil principal", func(t *testing.T) {
		runAuthenticateBearerVerifyFailure(t)
	})
}

func runBearerHeaderSuccess(t *testing.T, claims Claims, exp time.Time) {
	t.Helper()

	v := bearerCoreVerifier{claims: claims}
	a := NewBearerHeaderAuthenticator(v)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer tok")

	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if p == nil {
		t.Fatal("expected non-nil principal")
	}
	if p.Subject != claims.Subject {
		t.Errorf("Subject = %q, want %q", p.Subject, claims.Subject)
	}
	if p.TenantID != claims.TenantID {
		t.Errorf("TenantID = %q, want %q", p.TenantID, claims.TenantID)
	}
	if !p.PasswordResetRequired {
		t.Error("PasswordResetRequired = false, want true")
	}
	if p.Claims["sid"] != claims.SessionID {
		t.Errorf("Claims[sid] = %q, want %q", p.Claims["sid"], claims.SessionID)
	}
	if p.Claims["iss"] != claims.Issuer {
		t.Errorf("Claims[iss] = %q, want %q", p.Claims["iss"], claims.Issuer)
	}
	if p.Claims["token_use"] != string(claims.TokenUse) {
		t.Errorf("Claims[token_use] = %q, want %q", p.Claims["token_use"], claims.TokenUse)
	}
	if !p.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", p.ExpiresAt, exp)
	}
	originalFirstRole := claims.Roles[0]
	p.Roles[0] = "mutated"
	if claims.Roles[0] != originalFirstRole {
		t.Error("Roles must be a defensive copy")
	}
}

func runBearerHeaderMissing(t *testing.T) {
	t.Helper()

	a := NewBearerHeaderAuthenticator(bearerCoreVerifier{})
	p, ok, err := a.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if p == nil {
		t.Fatal("expected absent principal sentinel")
	}
}

func runBearerHeaderWrongScheme(t *testing.T) {
	t.Helper()

	a := NewBearerHeaderAuthenticator(bearerCoreVerifier{})
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "ServiceToken abc")

	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if p == nil {
		t.Fatal("expected absent principal sentinel")
	}
}

func runBearerHeaderVerifyFailure(t *testing.T) {
	t.Helper()

	sentinel := errors.New("verify boom")
	a := NewBearerHeaderAuthenticator(bearerCoreVerifier{err: sentinel})
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer bad")

	p, ok, err := a.Authenticate(req)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if p != nil {
		t.Fatalf("principal = %+v, want nil", p)
	}
}

func runBearerHeaderEmptySubject(t *testing.T) {
	t.Helper()

	a := NewBearerHeaderAuthenticator(bearerCoreVerifier{claims: Claims{Subject: ""}})
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer tok")

	p, ok, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for empty subject")
	}
	if ok {
		t.Fatal("expected ok=false")
	}
	if p != nil {
		t.Fatalf("principal = %+v, want nil", p)
	}
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ecErr.Code != errcode.ErrAuthUnauthorized {
		t.Errorf("Code = %v, want %v", ecErr.Code, errcode.ErrAuthUnauthorized)
	}
}

func runAuthenticateBearerSuccess(t *testing.T) {
	t.Helper()

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
}

func runAuthenticateBearerVerifyFailure(t *testing.T) {
	t.Helper()

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
	// The rejection must carry the generic auth-unauthorized code (a malformed
	// JWT must not be distinguishable from other 401s on the wire).
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ecErr.Code != errcode.ErrAuthUnauthorized {
		t.Errorf("expected ErrAuthUnauthorized, got %v", ecErr.Code)
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
