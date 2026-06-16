package auth

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

const authnDNeg6min = -6 * time.Minute

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

// --- T2: jwtClaimsToPrincipal mapping tests ---

// TestJWTClaimsToPrincipal_Shape pins the verified-Claims → *Principal mapping
// that AuthenticateBearer relies on: scalar fields, defensive-copied Roles, the
// exactly-three-entry Claims map, and ExpiresAt plumbing. The mapping is tested
// directly (no Authenticator wrapper) since jwtClaimsToPrincipal is the live
// conversion shared by the HTTP and gRPC auth cores.
func TestJWTClaimsToPrincipal_Shape(t *testing.T) {
	exp := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	claims := Claims{
		Subject:               "user-42",
		Roles:                 []string{"admin", "user"},
		SessionID:             "sess-xyz",
		Issuer:                "gocell-issuer",
		TokenUse:              TokenIntentAccess,
		Audience:              []string{"gocell"},
		PasswordResetRequired: true,
		TenantID:              tenantClaimUUID,
		ExpiresAt:             exp,
	}

	p, err := jwtClaimsToPrincipal(claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil principal")
	}

	assertJWTPrincipalScalars(t, p, claims)
	assertJWTPrincipalRoles(t, p, claims)
	assertJWTPrincipalClaimsMap(t, p, claims)
	if !p.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt not plumbed: got %v want %v", p.ExpiresAt, exp)
	}
}

// assertJWTPrincipalScalars verifies scalar fields (Kind, Subject, AuthMethod,
// TenantID, PasswordResetRequired) on the Principal produced by jwtClaimsToPrincipal.
func assertJWTPrincipalScalars(t *testing.T, p *Principal, claims Claims) {
	t.Helper()
	if p.Kind != PrincipalUser {
		t.Errorf("expected Kind=PrincipalUser, got %v", p.Kind)
	}
	if p.Subject != claims.Subject {
		t.Errorf("expected Subject=%q, got %q", claims.Subject, p.Subject)
	}
	if p.AuthMethod != "jwt" {
		t.Errorf("expected AuthMethod=%q, got %q", "jwt", p.AuthMethod)
	}
	if p.TenantID != claims.TenantID {
		t.Errorf("expected TenantID=%q, got %q", claims.TenantID, p.TenantID)
	}
	if !p.PasswordResetRequired {
		t.Error("expected PasswordResetRequired=true")
	}
}

// assertJWTPrincipalRoles verifies that Roles is a defensive copy of claims.Roles.
func assertJWTPrincipalRoles(t *testing.T, p *Principal, claims Claims) {
	t.Helper()
	if len(p.Roles) != len(claims.Roles) {
		t.Fatalf("expected %d roles, got %d", len(claims.Roles), len(p.Roles))
	}
	for i, r := range claims.Roles {
		if p.Roles[i] != r {
			t.Errorf("role[%d]: expected %q, got %q", i, r, p.Roles[i])
		}
	}
	originalFirstRole := claims.Roles[0]
	p.Roles[0] = "mutated"
	if claims.Roles[0] != originalFirstRole {
		t.Error("Principal.Roles must be a defensive copy; mutating it affected claims.Roles")
	}
}

// assertJWTPrincipalClaimsMap verifies the Claims map contains exactly the
// expected keys (sid, iss, token_use) and that Audience is excluded.
func assertJWTPrincipalClaimsMap(t *testing.T, p *Principal, claims Claims) {
	t.Helper()
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
	if _, ok := p.Claims["aud"]; ok {
		t.Error("Audience must not appear in Principal.Claims map")
	}
}

// --- T3: ServiceToken Authenticator tests ---

// mustNewInMemoryNonceStore is a test helper that constructs an InMemoryNonceStore
// with ServiceTokenNonceTTL, failing the test on error. Use instead of repeating
// require.NoError boilerplate in every test that needs a replay-safe store.
func mustNewInMemoryNonceStore(t *testing.T) NonceStore {
	t.Helper()
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL, clock.Real())
	if err != nil {
		t.Fatalf("mustNewInMemoryNonceStore: %v", err)
	}
	return store
}

// mustNewServiceTokenAuthenticator is a test helper that constructs a
// NewServiceTokenAuthenticator, failing the test on construction error.
func mustNewServiceTokenAuthenticator(t *testing.T, ring *HMACKeyRing, clk clock.Clock, opts ...ServiceTokenOption) Authenticator {
	t.Helper()
	a, err := NewServiceTokenAuthenticator(ring, clk, opts...)
	if err != nil {
		t.Fatalf("NewServiceTokenAuthenticator: %v", err)
	}
	return a
}

// --- fail-closed construction tests (new) ---

func TestNewServiceTokenAuthenticator_NilRing_ReturnsError(t *testing.T) {
	_, err := NewServiceTokenAuthenticator(nil, clock.Real(),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	if err == nil {
		t.Fatal("expected error for nil ring, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != errcode.ErrAuthKeyMissing {
		t.Errorf("expected ErrAuthKeyMissing, got %v", ec.Code)
	}
}

func TestNewServiceTokenAuthenticator_TypedNilRing_ReturnsError(t *testing.T) {
	var ring *HMACKeyRing
	_, err := NewServiceTokenAuthenticator(ring, clock.Real(),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	if err == nil {
		t.Fatal("expected error for typed-nil ring, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != errcode.ErrAuthKeyMissing {
		t.Errorf("expected ErrAuthKeyMissing, got %v", ec.Code)
	}
}

func TestNewServiceTokenAuthenticator_NilNonceStore_ReturnsError(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	// No WithServiceTokenNonceStore → cfg.nonceStore stays nil → must error.
	_, err := NewServiceTokenAuthenticator(ring, clock.Real())
	if err == nil {
		t.Fatal("expected error for nil NonceStore, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != errcode.ErrCellInvalidConfig {
		t.Errorf("expected ErrCellInvalidConfig, got %v", ec.Code)
	}
}

func TestNewServiceTokenAuthenticator_NoopNonceStore_ReturnsError(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	_, err := NewServiceTokenAuthenticator(ring, clock.Real(),
		WithServiceTokenNonceStore(NewNoopNonceStore()))
	if err == nil {
		t.Fatal("expected error for NoopNonceStore, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != errcode.ErrCellInvalidConfig {
		t.Errorf("expected ErrCellInvalidConfig, got %v", ec.Code)
	}
}

func TestNewServiceTokenAuthenticator_InMemoryNonceStore_OK(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	a, err := NewServiceTokenAuthenticator(ring, clock.Real(),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil Authenticator")
	}
}

// --- existing ServiceToken Authenticator tests (updated to error-first + explicit store) ---

func TestServiceTokenAuthenticator_NoHeader_Absent(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	a := mustNewServiceTokenAuthenticator(t, ring, clock.Real(),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false (absent credential)")
	}
	if p == nil {
		t.Fatal("expected absent principal sentinel")
	}
}

func TestServiceTokenAuthenticator_BearerSchemeIgnored_Absent(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	a := mustNewServiceTokenAuthenticator(t, ring, clock.Real(),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error (Bearer ignored), got: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false: Bearer scheme must be absent for ServiceToken authenticator")
	}
	if p == nil {
		t.Fatal("expected absent principal sentinel")
	}
}

func TestServiceTokenAuthenticator_InvalidMAC_Error(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	// Construct a token with wrong HMAC. Do not replace with a fixed suffix:
	// the generated MAC is random and may already end with that value.
	goodToken := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", now)
	parts := strings.Split(goodToken, ":")
	if len(parts) != 4 {
		t.Fatalf("expected generated service token to have 4 parts, got %d: %q", len(parts), goodToken)
	}
	lastMACByte := parts[3][len(parts[3])-2:]
	if lastMACByte == "00" {
		parts[3] = parts[3][:len(parts[3])-2] + "ff"
	} else {
		parts[3] = parts[3][:len(parts[3])-2] + "00"
	}
	badToken := strings.Join(parts, ":")
	if badToken == goodToken {
		t.Fatal("test setup failed: bad token must differ from good token")
	}
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
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	oldTime := now.Add(authnDNeg6min)
	// Token is signed for 6 minutes ago — exceeds ServiceTokenMaxAge.
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", oldTime)
	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
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
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	store := mustNewInMemoryNonceStore(t)
	a, err := NewServiceTokenAuthenticator(
		ring, clockmock.New(now),
		WithServiceTokenNonceStore(store),
	)
	if err != nil {
		t.Fatalf("NewServiceTokenAuthenticator: %v", err)
	}
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", now)

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
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))

	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", now)
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
	// AuthMethod must be "service_token".
	if p.AuthMethod != "service_token" {
		t.Errorf("expected AuthMethod=%q, got %q", "service_token", p.AuthMethod)
	}
	// PasswordResetRequired must be false.
	if p.PasswordResetRequired {
		t.Error("expected PasswordResetRequired=false for service principal")
	}
	// Wave 2 target: Subject="" and Roles=nil (caller identity via CallerCellID).
	// The old assertions on Subject=ServiceNameInternal and Roles=BuiltinServiceRoles(...)
	// are removed here; TestNewServiceTokenAuthenticator_PrincipalCallerCell (below)
	// pins the Wave 2 CallerCellID shape and will be RED until Wave 2 ships.
}

// --- F4: ServiceToken Authenticator — legacy 2-part + future timestamp ---

// TestServiceTokenAuthenticator_LegacyTwoPart_Error verifies that a 2-part
// legacy format token is rejected at the Authenticator level (not just via
// ServiceTokenMiddleware), returning an error that classifies as a 4xx.
func TestServiceTokenAuthenticator_LegacyTwoPart_Error(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))

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
// with a timestamp beyond the allowed service-token clock skew is rejected.
func TestServiceTokenAuthenticator_FutureTimestamp_Error(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	// "now" as seen by the authenticator.
	now := time.Now()
	// Token is signed just beyond the explicit future-skew window.
	futureTime := now.Add(ServiceTokenClockSkew + time.Second)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", futureTime)

	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
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

func TestServiceTokenAuthenticator_FarFutureTimestampOverflow_Error(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Unix(1_700_000_000, 0)
	farFuture := time.Unix(math.MaxInt64/2, 0)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", farFuture)

	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for far-future timestamp token, got nil")
	}
	if ok {
		t.Fatal("expected ok=false for far-future timestamp token")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

func TestServiceTokenAuthenticator_FutureTimestampWithinSkew_Accepted(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	futureTime := now.Add(ServiceTokenClockSkew)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", futureTime)

	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected token within skew to pass, got error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for token within future skew")
	}
	if p == nil {
		t.Fatal("expected principal for token within future skew")
	}
}

// TestNewServiceTokenAuthenticator_PrincipalCallerCell verifies that after Wave 2
// implements the 4-part token, the Principal carries CallerCellID='accesscore',
// empty Subject, and nil Roles.
//
// Spec: 4-part token GenerateServiceToken(ring, callerCell, method, path, query, ts)
// → after successful auth → Principal.CallerCellID == callerCell, Subject == "", Roles == nil.
func TestNewServiceTokenAuthenticator_PrincipalCallerCell(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	// Spec: 4-part signature: GenerateServiceToken(ring, callerCell, method, path, query, ts)
	token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", "", "", now)

	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for valid 4-part token")
	}
	if p == nil {
		t.Fatal("expected non-nil principal")
	}
	// Spec: CallerCellID must be propagated from the token.
	if p.CallerCellID != "accesscore" {
		t.Errorf("expected CallerCellID=%q, got %q", "accesscore", p.CallerCellID)
	}
	// Spec: Subject must be empty for service principal (identity via CallerCellID).
	if p.Subject != "" {
		t.Errorf("expected empty Subject, got %q", p.Subject)
	}
	// Spec: Roles must be nil for service principal.
	if p.Roles != nil {
		t.Errorf("expected nil Roles, got %v", p.Roles)
	}
}

// TestServiceToken_NeverMintsDevicePrincipal proves concept isolation (#1898
// FR-003): the service-token path is physically separate from the device-token
// path, so a service token can never derive a PrincipalDevice and its
// CallerCellID is never treated as a device subject. The service principal also
// fails closed at RowVisibility (no device row scope from a caller-cell id).
func TestServiceToken_NeverMintsDevicePrincipal(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", "", "", now)

	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := a.Authenticate(req)
	if err != nil || !ok || p == nil {
		t.Fatalf("service-token authenticate failed: ok=%v err=%v", ok, err)
	}
	if p.Kind != PrincipalService {
		t.Fatalf("service token must mint PrincipalService, got %v", p.Kind)
	}
	if p.Kind == PrincipalDevice {
		t.Fatal("service token must NEVER mint PrincipalDevice (concept isolation)")
	}
	// callerCellID is identity-for-services, NOT a device subject.
	if p.CallerCellID == "" {
		t.Fatal("service principal must carry CallerCellID")
	}
	if p.Subject != "" {
		t.Fatalf("service principal Subject must be empty (callerCellID is not a device subject), got %q", p.Subject)
	}
	// A service principal cannot derive a device row scope — fail closed.
	if _, err := p.RowVisibility(req.Context()); err == nil {
		t.Fatal("service principal must fail closed at RowVisibility (no device row scope)")
	}
}

func TestServiceTokenAuthenticator_PastTimestampAtMaxAge_Error(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	oldTime := now.Add(-ServiceTokenMaxAge)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", "", "", oldTime)

	a := mustNewServiceTokenAuthenticator(t, ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)))
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	p, ok, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected token at max age boundary to be expired")
	}
	if ok {
		t.Fatal("expected ok=false for expired token")
	}
	if p != nil {
		t.Fatalf("expected nil principal, got %v", p)
	}
}

// TestValidateCallerCell tests the validateCallerCell function directly.
// Cases reflect metadata.CellIDPattern (^[a-z][a-z0-9]+$): lowercase ASCII
// letters + digits, ≥2 chars, must start with a letter — same regex
// schema/governance enforce. Dash, single char, leading digit, uppercase,
// underscore are all invalid.
func TestValidateCallerCell(t *testing.T) {
	tests := []struct {
		name       string
		callerCell string
		wantErr    bool
		wantCode   errcode.Code
		wantMsg    string // partial substring expected in error message
	}{
		{
			name:       "empty string — missing",
			callerCell: "",
			wantErr:    true,
			wantCode:   errcode.ErrAuthUnauthorized,
			wantMsg:    "caller cell missing",
		},
		{
			name:       "uppercase Accesscore — invalid (must be lowercase)",
			callerCell: "Accesscore",
			wantErr:    true,
			wantCode:   errcode.ErrAuthUnauthorized,
			wantMsg:    "caller cell id",
		},
		{
			name:       "dash access-core — invalid (no-dash CellIDPattern)",
			callerCell: "access-core",
			wantErr:    true,
			wantCode:   errcode.ErrAuthUnauthorized,
			wantMsg:    "caller cell id",
		},
		{
			name:       "starts with digit — invalid",
			callerCell: "123abc",
			wantErr:    true,
			wantCode:   errcode.ErrAuthUnauthorized,
			wantMsg:    "caller cell id",
		},
		{
			name:       "single letter — invalid (≥2 chars required)",
			callerCell: "a",
			wantErr:    true,
			wantCode:   errcode.ErrAuthUnauthorized,
			wantMsg:    "caller cell id",
		},
		{
			name:       "alphanumeric with hyphens — invalid (no-dash CellIDPattern)",
			callerCell: "a-b-c-123",
			wantErr:    true,
			wantCode:   errcode.ErrAuthUnauthorized,
			wantMsg:    "caller cell id",
		},
		{
			name:       "underscore foo_bar — invalid",
			callerCell: "foo_bar",
			wantErr:    true,
			wantCode:   errcode.ErrAuthUnauthorized,
			wantMsg:    "caller cell id",
		},
		{
			name:       "concat accesscore — valid",
			callerCell: "accesscore",
			wantErr:    false,
		},
		{
			name:       "two char min ab — valid",
			callerCell: "ab",
			wantErr:    false,
		},
		{
			name:       "letter plus digit a0 — valid",
			callerCell: "a0",
			wantErr:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertValidateCallerCell(t, tc.callerCell, tc.wantErr, tc.wantCode, tc.wantMsg)
		})
	}
}

// assertValidateCallerCell is the per-case assertion helper for
// TestValidateCallerCell. Extracted to keep the parent function's cognitive
// complexity within the project limit (S3776).
func assertValidateCallerCell(t *testing.T, callerCell string, wantErr bool, wantCode errcode.Code, wantMsg string) {
	t.Helper()
	err := validateCallerCell(callerCell)
	if !wantErr {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ecErr.Code != wantCode {
		t.Errorf("expected code %v, got %v", wantCode, ecErr.Code)
	}
	if wantMsg != "" && !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("expected error message to contain %q, got %q", wantMsg, err.Error())
	}
}

func TestNewAnonymousAuthenticator(t *testing.T) {
	a := NewAnonymousAuthenticator()
	p, ok, err := a.Authenticate(newRequest(t))
	if err != nil {
		t.Fatalf("anonymous authenticator should not error: %v", err)
	}
	if !ok {
		t.Fatal("anonymous authenticator must always return ok=true")
	}
	if p == nil {
		t.Fatal("anonymous authenticator must return non-nil principal")
	}
	if p.Kind != PrincipalAnonymous {
		t.Errorf("expected PrincipalAnonymous, got %v", p.Kind)
	}
	if !p.ExpiresAt.IsZero() {
		t.Error("anonymous principal must have zero ExpiresAt (no expiry)")
	}
}

func TestNewContextAuthenticator_PrincipalInCtx(t *testing.T) {
	want := &Principal{Kind: PrincipalUser, Subject: "u1"}
	a := NewContextAuthenticator()

	req := newRequest(t)
	req = req.WithContext(WithPrincipal(req.Context(), want))

	got, ok, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when principal is in ctx")
	}
	if got != want {
		t.Errorf("expected same pointer, got %v want %v", got, want)
	}
}

func TestNewContextAuthenticator_NoPrincipalInCtx(t *testing.T) {
	a := NewContextAuthenticator()
	p, ok, err := a.Authenticate(newRequest(t))
	if err != nil {
		t.Fatalf("absent ctx principal must not error, got %v", err)
	}
	if ok {
		t.Error("expected ok=false when principal not in ctx")
	}
	if p == nil || p.Kind != PrincipalUnknown {
		t.Errorf("expected absentPrincipal, got %v", p)
	}
}
