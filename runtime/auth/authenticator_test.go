package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// stubAuthenticator is a test double for the Authenticator interface.
type stubAuthenticator struct {
	principal *Principal
	ok        bool
	err       error
	calls     int
}

func (s *stubAuthenticator) Authenticate(_ *http.Request) (*Principal, bool, error) {
	s.calls++
	return s.principal, s.ok, s.err
}

func newRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, "/", nil)
}

func TestAuthenticatorFunc_Adapter(t *testing.T) {
	want := &Principal{Kind: PrincipalUser, Subject: "u1"}
	fn := AuthenticatorFunc(func(_ *http.Request) (*Principal, bool, error) {
		return want, true, nil
	})
	got, ok, err := fn.Authenticate(newRequest(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != want {
		t.Error("expected same Principal pointer")
	}
}

func TestUnionAuthenticator_FirstMatchWins(t *testing.T) {
	want := &Principal{Kind: PrincipalUser, Subject: "first"}
	first := &stubAuthenticator{principal: want, ok: true}
	second := &stubAuthenticator{principal: &Principal{Subject: "second"}, ok: true}
	third := &stubAuthenticator{principal: &Principal{Subject: "third"}, ok: true}

	u := NewUnionAuthenticator(first, second, third)
	got, ok, err := u.Authenticate(newRequest(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != want {
		t.Errorf("expected first principal, got %v", got)
	}
	if second.calls != 0 {
		t.Errorf("second authenticator should not have been called, got %d calls", second.calls)
	}
	if third.calls != 0 {
		t.Errorf("third authenticator should not have been called, got %d calls", third.calls)
	}
}

func TestUnionAuthenticator_SkipAbsentCredential(t *testing.T) {
	want := &Principal{Kind: PrincipalService, Subject: "svc"}
	first := &stubAuthenticator{principal: nil, ok: false, err: nil}
	second := &stubAuthenticator{principal: want, ok: true}

	u := NewUnionAuthenticator(first, second)
	got, ok, err := u.Authenticate(newRequest(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != want {
		t.Errorf("expected second principal, got %v", got)
	}
	if first.calls != 1 {
		t.Errorf("first authenticator should have been called once, got %d", first.calls)
	}
	if second.calls != 1 {
		t.Errorf("second authenticator should have been called once, got %d", second.calls)
	}
}

func TestUnionAuthenticator_InvalidCredentialShortCircuits(t *testing.T) {
	errInvalid := errors.New("token expired")
	first := &stubAuthenticator{principal: nil, ok: false, err: errInvalid}
	second := &stubAuthenticator{principal: &Principal{Subject: "should-not-reach"}, ok: true}

	u := NewUnionAuthenticator(first, second)
	got, ok, err := u.Authenticate(newRequest(t))
	if !errors.Is(err, errInvalid) {
		t.Errorf("expected errInvalid, got %v", err)
	}
	if ok {
		t.Error("expected ok=false on error")
	}
	if got != nil {
		t.Errorf("expected nil principal on error, got %v", got)
	}
	if second.calls != 0 {
		t.Errorf("second authenticator must not be called on error, got %d calls", second.calls)
	}
}

func TestUnionAuthenticator_EmptyChildren(t *testing.T) {
	u := NewUnionAuthenticator()
	got, ok, err := u.Authenticate(newRequest(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false with no children")
	}
	if got != nil {
		t.Errorf("expected nil principal, got %v", got)
	}
}

func TestUnionAuthenticator_AllAbsent(t *testing.T) {
	first := &stubAuthenticator{principal: nil, ok: false}
	second := &stubAuthenticator{principal: nil, ok: false}

	u := NewUnionAuthenticator(first, second)
	got, ok, err := u.Authenticate(newRequest(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false when all absent")
	}
	if got != nil {
		t.Errorf("expected nil principal, got %v", got)
	}
	if first.calls != 1 {
		t.Errorf("first should be called once, got %d", first.calls)
	}
	if second.calls != 1 {
		t.Errorf("second should be called once, got %d", second.calls)
	}
}

// --- T2: JWT Authenticator tests ---

// stubVerifier is a minimal IntentTokenVerifier test double.
type stubVerifier struct {
	claims Claims
	err    error
}

func (s *stubVerifier) VerifyIntent(_ context.Context, _ string, _ TokenIntent) (Claims, error) {
	return s.claims, s.err
}

func newGetRequest(t *testing.T, authHeader string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req
}

func TestJWTAuthenticator_NoAuthHeader_Absent(t *testing.T) {
	v := &stubVerifier{}
	a := NewJWTAuthenticator(v)
	p, ok, err := a.Authenticate(newGetRequest(t, ""))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false (absent credential)")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestJWTAuthenticator_NonBearerScheme_Absent(t *testing.T) {
	v := &stubVerifier{}
	a := NewJWTAuthenticator(v)
	// Non-Bearer scheme must be treated as absent so Union can try next authenticator.
	p, ok, err := a.Authenticate(newGetRequest(t, "Basic dXNlcjpwYXNz"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for non-Bearer scheme")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestJWTAuthenticator_InvalidToken_Error(t *testing.T) {
	errInvalid := errors.New("signature invalid")
	v := &stubVerifier{err: errInvalid}
	a := NewJWTAuthenticator(v)
	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer bad-token"))
	if !errors.Is(err, errInvalid) {
		t.Fatalf("expected errInvalid, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false on error")
	}
	if p != nil {
		t.Fatalf("expected nil principal on error, got %v", p)
	}
}

func TestJWTAuthenticator_IntentMismatch_Error(t *testing.T) {
	errIntent := errors.New("wrong intent: refresh token presented at access endpoint")
	v := &stubVerifier{err: errIntent}
	a := NewJWTAuthenticator(v)
	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer refresh-token"))
	if !errors.Is(err, errIntent) {
		t.Fatalf("expected intent error, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false on intent mismatch")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestJWTAuthenticator_Success_PrincipalShape(t *testing.T) {
	claims := Claims{
		Subject:               "user-42",
		Roles:                 []string{"admin", "user"},
		SessionID:             "sess-xyz",
		Issuer:                "gocell-issuer",
		TokenUse:              TokenIntentAccess,
		Audience:              []string{"gocell"},
		PasswordResetRequired: true,
	}
	v := &stubVerifier{claims: claims}
	a := NewJWTAuthenticator(v)

	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer valid-access-token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true on success")
	}
	if p == nil {
		t.Fatal("expected non-nil principal")
	}

	// Kind must be PrincipalUser.
	if p.Kind != PrincipalUser {
		t.Errorf("expected Kind=PrincipalUser, got %v", p.Kind)
	}
	// Subject must match claims.Subject.
	if p.Subject != claims.Subject {
		t.Errorf("expected Subject=%q, got %q", claims.Subject, p.Subject)
	}
	// AuthMethod must be "jwt".
	if p.AuthMethod != "jwt" {
		t.Errorf("expected AuthMethod=%q, got %q", "jwt", p.AuthMethod)
	}
	// PasswordResetRequired must be forwarded.
	if !p.PasswordResetRequired {
		t.Error("expected PasswordResetRequired=true")
	}
	// Roles must be a copy of claims.Roles.
	if len(p.Roles) != len(claims.Roles) {
		t.Fatalf("expected %d roles, got %d", len(claims.Roles), len(p.Roles))
	}
	for i, r := range claims.Roles {
		if p.Roles[i] != r {
			t.Errorf("role[%d]: expected %q, got %q", i, r, p.Roles[i])
		}
	}
	// Roles must be a copy — mutating Principal.Roles must not affect original.
	originalFirstRole := claims.Roles[0]
	p.Roles[0] = "mutated"
	if claims.Roles[0] != originalFirstRole {
		t.Error("Principal.Roles must be a defensive copy; mutating it affected claims.Roles")
	}

	// Claims map must have exactly 3 keys: sid, iss, token_use.
	if len(p.Claims) != 3 {
		t.Errorf("expected exactly 3 Claims map entries, got %d: %v", len(p.Claims), p.Claims)
	}
	if p.Claims["sid"] != claims.SessionID {
		t.Errorf("expected Claims[sid]=%q, got %q", claims.SessionID, p.Claims["sid"])
	}
	if p.Claims["iss"] != claims.Issuer {
		t.Errorf("expected Claims[iss]=%q, got %q", claims.Issuer, p.Claims["iss"])
	}
	if p.Claims["token_use"] != string(claims.TokenUse) {
		t.Errorf("expected Claims[token_use]=%q, got %q", string(claims.TokenUse), p.Claims["token_use"])
	}
	// Audience must NOT appear in Claims map.
	if _, ok := p.Claims["aud"]; ok {
		t.Error("Audience must not appear in Principal.Claims map")
	}
}

// --- T3: ServiceToken Authenticator tests ---

func TestServiceTokenAuthenticator_NoHeader_Absent(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	a := NewServiceTokenAuthenticator(ring)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false (absent credential)")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestServiceTokenAuthenticator_BearerSchemeIgnored_Absent(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	a := NewServiceTokenAuthenticator(ring)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error (Bearer ignored), got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false: Bearer scheme must be absent for ServiceToken authenticator")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestServiceTokenAuthenticator_InvalidMAC_Error(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	a := NewServiceTokenAuthenticator(ring, WithServiceTokenClock(func() time.Time { return now }))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	// Construct a token with wrong HMAC (last byte flipped).
	goodToken := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", now)
	badToken := goodToken[:len(goodToken)-2] + "ff"
	req.Header.Set("Authorization", "ServiceToken "+badToken)
	p, ok, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for invalid MAC, got nil")
	}
	if ok {
		t.Fatal("expected ok=false on invalid MAC")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestServiceTokenAuthenticator_Expired_Error(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	oldTime := now.Add(-6 * time.Minute)
	// Token is signed for 6 minutes ago — exceeds ServiceTokenMaxAge.
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", oldTime)
	a := NewServiceTokenAuthenticator(ring, WithServiceTokenClock(func() time.Time { return now }))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	p, ok, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for expired token")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestServiceTokenAuthenticator_NonceReplay_Error(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	store, err := NewInMemoryNonceStore(5 * time.Minute)
	if err != nil {
		t.Fatalf("NewInMemoryNonceStore: %v", err)
	}
	a := NewServiceTokenAuthenticator(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithNonceStore(store),
	)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", now)

	// First use — must succeed.
	req1 := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req1.Header.Set("Authorization", "ServiceToken "+token)
	p1, ok1, err1 := a.Authenticate(req1)
	if err1 != nil {
		t.Fatalf("first use unexpected error: %v", err1)
	}
	if !ok1 {
		t.Fatal("first use expected ok=true")
	}
	if p1 == nil {
		t.Fatal("first use expected non-nil principal")
	}

	// Second use (replay) — must error.
	req2 := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req2.Header.Set("Authorization", "ServiceToken "+token)
	p2, ok2, err2 := a.Authenticate(req2)
	if err2 == nil {
		t.Fatal("replay expected error, got nil")
	}
	if ok2 {
		t.Fatal("replay expected ok=false")
	}
	if p2 != nil {
		t.Fatalf("replay expected nil principal, got %v", p2)
	}
}

func TestServiceTokenAuthenticator_Success_PrincipalShape(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	a := NewServiceTokenAuthenticator(ring, WithServiceTokenClock(func() time.Time { return now }))

	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true on success")
	}
	if p == nil {
		t.Fatal("expected non-nil principal")
	}

	// Kind must be PrincipalService.
	if p.Kind != PrincipalService {
		t.Errorf("expected Kind=PrincipalService, got %v", p.Kind)
	}
	// Subject must be ServiceNameInternal.
	if p.Subject != ServiceNameInternal {
		t.Errorf("expected Subject=%q, got %q", ServiceNameInternal, p.Subject)
	}
	// AuthMethod must be "service_token".
	if p.AuthMethod != "service_token" {
		t.Errorf("expected AuthMethod=%q, got %q", "service_token", p.AuthMethod)
	}
	// PasswordResetRequired must be false.
	if p.PasswordResetRequired {
		t.Error("expected PasswordResetRequired=false for service principal")
	}
	// Roles must match BuiltinServiceRoles(ServiceNameInternal).
	expectedRoles := BuiltinServiceRoles(ServiceNameInternal)
	if len(p.Roles) != len(expectedRoles) {
		t.Fatalf("expected %d roles, got %d", len(expectedRoles), len(p.Roles))
	}
	for i, r := range expectedRoles {
		if p.Roles[i] != r {
			t.Errorf("role[%d]: expected %q, got %q", i, r, p.Roles[i])
		}
	}
	// Roles must be a copy — mutating Principal.Roles must not affect BuiltinServiceRoles.
	if len(p.Roles) > 0 {
		original := p.Roles[0]
		p.Roles[0] = "mutated"
		fresh := BuiltinServiceRoles(ServiceNameInternal)
		if fresh[0] != original {
			t.Error("Principal.Roles must be a defensive copy")
		}
	}
}

// TestJWTAuthenticator_EmptySubject_Error verifies that a JWT token with an
// empty "sub" claim is rejected at the Authenticator layer (G1.A primary defence).
// An empty subject indicates a malformed JWT — downstream code must never
// receive a PrincipalUser with Subject="".
func TestJWTAuthenticator_EmptySubject_Error(t *testing.T) {
	// Verifier succeeds but returns claims with empty Subject.
	v := &stubVerifier{claims: Claims{
		Subject:   "",
		Roles:     []string{"admin"},
		SessionID: "sess-1",
		Issuer:    "gocell-issuer",
		TokenUse:  TokenIntentAccess,
	}}
	a := NewJWTAuthenticator(v)

	p, ok, err := a.Authenticate(newGetRequest(t, "Bearer token-with-empty-sub"))
	if err == nil {
		t.Fatal("expected error for JWT with empty subject, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for JWT with empty subject")
	}
	if p != nil {
		t.Fatalf("expected nil principal for JWT with empty subject, got %v", p)
	}
	// Must be an auth unauthorized error.
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ecErr.Code != errcode.ErrAuthUnauthorized {
		t.Errorf("expected ErrAuthUnauthorized, got %v", ecErr.Code)
	}
}

// --- F3: Union cross-bleed test ---

// TestUnionAuthenticator_BearerAndServiceToken_NoCrossBleed verifies that when
// Union(JWT, ServiceToken) receives "Authorization: ServiceToken <payload>",
// the JWT authenticator returns absent (nil, false, nil) — because it sees a
// non-Bearer scheme — and the ServiceToken authenticator validates the
// credential and returns a valid Principal. No cross-bleed between the two.
func TestUnionAuthenticator_BearerAndServiceToken_NoCrossBleed(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()

	// trackingVerifier records whether VerifyIntent was called.
	// The JWT authenticator short-circuits before calling VerifyIntent when
	// extractBearerToken returns "" (non-Bearer scheme), so it must NOT be called.
	verifierCalled := false
	trackingVerifier := &trackingIntentVerifier{onVerify: func() { verifierCalled = true }}

	jwtAuth := NewJWTAuthenticator(trackingVerifier)
	svcAuth := NewServiceTokenAuthenticator(ring, WithServiceTokenClock(func() time.Time { return now }))
	union := NewUnionAuthenticator(jwtAuth, svcAuth)

	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := union.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true: ServiceToken authenticator should have matched")
	}
	if p == nil {
		t.Fatal("expected non-nil principal")
	}
	if p.Kind != PrincipalService {
		t.Errorf("expected Kind=PrincipalService, got %v", p.Kind)
	}
	if verifierCalled {
		t.Error("JWT VerifyIntent must not be called for a ServiceToken header (non-Bearer scheme returns absent before verification)")
	}
}

// trackingIntentVerifier is a test double that calls onVerify when VerifyIntent
// is invoked, then returns zero Claims and nil error.
type trackingIntentVerifier struct {
	onVerify func()
}

func (v *trackingIntentVerifier) VerifyIntent(_ context.Context, _ string, _ TokenIntent) (Claims, error) {
	if v.onVerify != nil {
		v.onVerify()
	}
	return Claims{}, nil
}

// --- F4: ServiceToken Authenticator — legacy 2-part + future timestamp ---

// TestServiceTokenAuthenticator_LegacyTwoPart_Error verifies that a 2-part
// legacy format token is rejected at the Authenticator level (not just via
// ServiceTokenMiddleware), returning an error that classifies as a 4xx.
func TestServiceTokenAuthenticator_LegacyTwoPart_Error(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	a := NewServiceTokenAuthenticator(ring, WithServiceTokenClock(func() time.Time { return now }))

	// Build a 2-part token: {timestamp}:{hex_hmac} (no nonce).
	tsStr := fmt.Sprintf("%d", now.Unix())
	legacyToken := tsStr + ":deadbeef00112233"
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+legacyToken)

	p, ok, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for legacy 2-part token, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for legacy 2-part token")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

// TestServiceTokenAuthenticator_FutureTimestamp_Error verifies that a token
// with a timestamp in the future (age > ServiceTokenMaxAge) is rejected.
func TestServiceTokenAuthenticator_FutureTimestamp_Error(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	// "now" as seen by the authenticator.
	now := time.Now()
	// Token is signed 10 minutes in the future relative to the authenticator's clock.
	futureTime := now.Add(10 * time.Minute)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", futureTime)

	a := NewServiceTokenAuthenticator(ring, WithServiceTokenClock(func() time.Time { return now }))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for future timestamp token, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for future timestamp token")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}
