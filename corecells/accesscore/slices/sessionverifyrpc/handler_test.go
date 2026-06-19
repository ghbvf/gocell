package sessionverifyrpc

// These tests cover the gRPC session-verify handler's DOMAIN behavior only
// (token introspection + claims projection + error classification + tenant
// binding). The session:verify PDP gate is NOT a handler concern: the runtime gRPC
// auth interceptor runs it before the handler (declared in
// endpoints.grpc.methods[].permission, #2008) and is covered at the assembly level
// (corecells/accesscore/grpc_pdp_gate_test.go). These tests DO inject a caller
// principal (via ctxWithCaller / a bufconn interceptor) because the handler binds
// the introspected token to the caller's tenant (#1154 review F2) — the principal
// is what the interceptor sets in production.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	sessionverifyv1 "github.com/ghbvf/gocell/generated/contracts/grpc/auth/session/verify/v1"
)

// ctxWithCaller returns a context carrying an authenticated caller principal in the
// given tenant — what the gRPC auth interceptor sets before the handler runs. The
// handler reads it to bind the introspected token to the caller's tenant (F2).
func ctxWithCaller(tenantID string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "caller-1", TenantID: tenantID,
	})
}

// stubVerifier is a probe kauth.IntentTokenVerifier returning a fixed
// claims/error pair, letting the handler tests exercise every branch (valid /
// invalid / infra-unavailable) without JWT key plumbing or a session store.
type stubVerifier struct {
	claims kauth.Claims
	err    error
	called bool
}

func (s *stubVerifier) VerifyIntent(_ context.Context, _ string, _ kauth.TokenIntent) (kauth.Claims, error) {
	s.called = true
	return s.claims, s.err
}

var fixedExpiry = time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

// validClaims is the projection-source for the success path: every field the
// VerifyTokenResponse carries is set so the test asserts the projection field by
// field.
var validClaims = kauth.Claims{
	Subject:               "usr-1",
	TenantID:              "11111111-1111-1111-1111-111111111111",
	SessionID:             "sess-1",
	Roles:                 []string{"admin", "viewer"},
	ExpiresAt:             fixedExpiry,
	PasswordResetRequired: true,
	TokenUse:              kauth.TokenIntentAccess,
}

func TestServer_VerifyToken_Valid(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{claims: validClaims}
	srv := NewServer(v)

	// Caller in the SAME tenant as the introspected token → valid (F2 same-tenant bind).
	resp, err := srv.VerifyToken(ctxWithCaller(validClaims.TenantID), &sessionverifyv1.VerifyTokenRequest{Token: "good-token"})
	if err != nil {
		t.Fatalf("VerifyToken returned error: %v", err)
	}
	if !resp.GetValid() {
		t.Fatalf("valid token: GetValid() = false, want true")
	}
	if got := resp.GetSubject(); got != validClaims.Subject {
		t.Errorf("Subject = %q, want %q", got, validClaims.Subject)
	}
	if got := resp.GetTenantId(); got != validClaims.TenantID {
		t.Errorf("TenantId = %q, want %q", got, validClaims.TenantID)
	}
	if got := resp.GetSessionId(); got != validClaims.SessionID {
		t.Errorf("SessionId = %q, want %q", got, validClaims.SessionID)
	}
	if got := resp.GetRoles(); len(got) != 2 || got[0] != "admin" || got[1] != "viewer" {
		t.Errorf("Roles = %v, want [admin viewer]", got)
	}
	if got := resp.GetExpiresAtUnixNano(); got != fixedExpiry.UnixNano() {
		t.Errorf("ExpiresAtUnixNano = %d, want %d", got, fixedExpiry.UnixNano())
	}
	if !resp.GetPasswordResetRequired() {
		t.Errorf("PasswordResetRequired = false, want true")
	}
}

func TestServer_VerifyToken_CrossTenant_Denied(t *testing.T) {
	t.Parallel()
	// F2: a caller in tenant B introspecting a tenant-A token must get valid=false
	// (no cross-tenant session-state leak), uniform with a bad token — no subject /
	// tenant / roles leaked.
	srv := NewServer(&stubVerifier{claims: validClaims}) // token tenant = validClaims.TenantID (A)
	otherTenant := "22222222-2222-2222-2222-222222222222"

	resp, err := srv.VerifyToken(ctxWithCaller(otherTenant), &sessionverifyv1.VerifyTokenRequest{Token: "good-token"})
	if err != nil {
		t.Fatalf("cross-tenant must not error, got: %v", err)
	}
	if resp.GetValid() {
		t.Fatalf("cross-tenant introspection: GetValid() = true, want false")
	}
	if resp.GetSubject() != "" || resp.GetTenantId() != "" || len(resp.GetRoles()) != 0 {
		t.Errorf("cross-tenant must not leak claims, got subject=%q tenant=%q roles=%v",
			resp.GetSubject(), resp.GetTenantId(), resp.GetRoles())
	}
}

func TestServer_VerifyToken_NoCallerPrincipal_Denied(t *testing.T) {
	t.Parallel()
	// Defense-in-depth: a verified token with NO caller principal in ctx (interceptor
	// not run, or a wiring bug) fails closed — valid=false, no claims leaked.
	srv := NewServer(&stubVerifier{claims: validClaims})
	resp, err := srv.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "good-token"})
	if err != nil {
		t.Fatalf("no-principal must not error, got: %v", err)
	}
	if resp.GetValid() {
		t.Fatalf("no caller principal: GetValid() = true, want false (fail-closed)")
	}
}

func TestServer_VerifyToken_InvalidOrExpired_IsUniformFalse(t *testing.T) {
	t.Parallel()
	// Both an unauthenticated token and a malformed/invalid one must collapse to a
	// uniform valid=false response (no error, no reason enumeration).
	cases := []struct {
		name string
		err  error
	}{
		{"unauthenticated", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, "invalid or expired authentication token")},
		{"invalid", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid or expired authentication token")},
		// Non-errcode plain errors must also collapse to valid=false (proves ONLY
		// KindUnavailable propagates, and nothing else).
		{"plain-error", errors.New("non-errcode error from verifier")},
		// KindInternal (unexpected server fault) must collapse to valid=false, not
		// propagate — internal errors are not infrastructure-unavailable outages.
		{"internal-errcode", errcode.New(errcode.KindInternal, errcode.ErrInternal, "unexpected internal error")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := NewServer(&stubVerifier{err: tc.err})
			resp, err := srv.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "bad-token"})
			if err != nil {
				t.Fatalf("invalid token must NOT return a gRPC error, got: %v", err)
			}
			if resp.GetValid() {
				t.Fatalf("invalid token: GetValid() = true, want false")
			}
			if resp.GetSubject() != "" {
				t.Errorf("invalid token must not leak subject, got %q", resp.GetSubject())
			}
		})
	}
}

func TestServer_VerifyToken_InfraUnavailable_ReturnsError(t *testing.T) {
	t.Parallel()
	// An infrastructure outage (session store / key provider) must propagate as a
	// raw *errcode.Error (KindUnavailable) from the handler — NOT wrapped in a gRPC
	// status. The chain's UnaryErrcodeMap interceptor (PR-12 #1155) maps it to
	// codes.Unavailable on the wire (proven by TestServer_VerifyToken_InfraUnavailable_OverGRPC).
	// Keeping the handler free of grpc/status lets us assert the domain error
	// identity here instead of the transport representation.
	infra := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "authentication service unavailable")
	srv := NewServer(&stubVerifier{err: infra})

	resp, err := srv.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "any-token"})
	if err == nil {
		t.Fatalf("infra-unavailable must return an error, got nil (resp=%v)", resp)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("infra-unavailable error must be *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindUnavailable {
		t.Errorf("infra-unavailable *errcode.Error must have KindUnavailable, got Kind=%v", ec.Kind)
	}
}

func TestServer_VerifyToken_EmptyToken_ShortCircuits(t *testing.T) {
	t.Parallel()
	// An empty token must short-circuit to valid=false WITHOUT calling the verifier.
	// The stub would return valid claims if called, so a valid=false result proves
	// the handler short-circuited.
	v := &stubVerifier{claims: validClaims}
	srv := NewServer(v)

	resp, err := srv.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: ""})
	if err != nil {
		t.Fatalf("empty token must not error, got: %v", err)
	}
	if resp.GetValid() {
		t.Fatalf("empty token: GetValid() = true, want false")
	}
	if v.called {
		t.Errorf("empty token must not call the verifier (short-circuit)")
	}
}

// assertClaimsProjection verifies that resp carries every field from the
// canonical validClaims fixture. Shared by TestServer_VerifyToken_Valid (direct
// handler call) and TestServer_VerifyToken_OverGRPC (bufconn round-trip) to
// prove proto marshaling loses no field.
func assertClaimsProjection(t *testing.T, resp *sessionverifyv1.VerifyTokenResponse) {
	t.Helper()
	if !resp.GetValid() {
		t.Errorf("GetValid() = false, want true")
	}
	if got := resp.GetSubject(); got != validClaims.Subject {
		t.Errorf("Subject = %q, want %q", got, validClaims.Subject)
	}
	if got := resp.GetTenantId(); got != validClaims.TenantID {
		t.Errorf("TenantId = %q, want %q", got, validClaims.TenantID)
	}
	if got := resp.GetSessionId(); got != validClaims.SessionID {
		t.Errorf("SessionId = %q, want %q", got, validClaims.SessionID)
	}
	if got := resp.GetRoles(); len(got) != len(validClaims.Roles) || got[0] != validClaims.Roles[0] || got[1] != validClaims.Roles[1] {
		t.Errorf("Roles = %v, want %v", got, validClaims.Roles)
	}
	if got := resp.GetExpiresAtUnixNano(); got != fixedExpiry.UnixNano() {
		t.Errorf("ExpiresAtUnixNano = %d, want %d", got, fixedExpiry.UnixNano())
	}
	if !resp.GetPasswordResetRequired() {
		t.Errorf("PasswordResetRequired = false, want true")
	}
}

// newBufconnClient creates a bufconn gRPC server with the given handler and
// returns a connected client + cleanup. A server-side unary interceptor injects a
// caller principal in callerTenant — standing in for the production auth
// interceptor — so the handler's F2 tenant bind has a caller to compare against.
// UnaryErrcodeMap (PR-12 #1155) is chained so *errcode.Error values from the
// handler are mapped to the correct gRPC status codes on the wire, matching
// the production chain (NewServerInterceptors). Used by multiple bufconn round-trip
// tests.
func newBufconnClient(
	t *testing.T, handler sessionverifyv1.SessionVerifyServiceServer, callerTenant string,
) sessionverifyv1.SessionVerifyServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			return h(auth.WithPrincipal(ctx, &auth.Principal{Kind: auth.PrincipalUser, Subject: "bufconn-caller", TenantID: callerTenant}), req)
		},
		interceptor.UnaryErrcodeMap(),
	))
	sessionverifyv1.RegisterSessionVerifyServiceServer(grpcServer, handler)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return sessionverifyv1.NewSessionVerifyServiceClient(conn)
}

// TestServer_VerifyToken_OverGRPC exercises the full proto round-trip over a real
// transport (bufconn) WITHOUT the auth interceptor — proving the generated
// RegisterSessionVerifyServiceServer wiring and response marshaling work
// end-to-end, with field-by-field assertion to detect any proto marshaling loss.
// The PDP gate is covered separately at the assembly level.
func TestServer_VerifyToken_OverGRPC(t *testing.T) {
	t.Parallel()
	client := newBufconnClient(t, NewServer(&stubVerifier{claims: validClaims}), validClaims.TenantID)

	resp, err := client.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "good-token"})
	if err != nil {
		t.Fatalf("VerifyToken over gRPC: %v", err)
	}
	assertClaimsProjection(t, resp)
}

// TestServer_VerifyToken_InfraUnavailable_OverGRPC proves that a KindUnavailable
// verifier error surfaces as codes.Unavailable over the real gRPC wire (bufconn).
// The in-proc handler test (TestServer_VerifyToken_InfraUnavailable_ReturnsError)
// confirms the handler returns status.Error(codes.Unavailable, ...); this test
// confirms the code survives serialization over the transport.
func TestServer_VerifyToken_InfraUnavailable_OverGRPC(t *testing.T) {
	t.Parallel()
	infraErr := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "authentication service unavailable")
	client := newBufconnClient(t, NewServer(&stubVerifier{err: infraErr}), validClaims.TenantID)

	_, err := client.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "any-token"})
	if err == nil {
		t.Fatalf("infra-unavailable must return an error over the wire, got nil")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("infra-unavailable over gRPC must be codes.Unavailable, got %v (err=%v)", got, err)
	}
}
