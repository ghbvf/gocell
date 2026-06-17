package sessionverifyrpc

// These tests cover the gRPC session-verify handler's DOMAIN behavior only
// (token introspection + claims projection + error classification).
// Authorization (session:verify) is NOT a handler concern: the runtime gRPC auth
// interceptor runs the ABAC PDP gate before the handler is invoked (declared in
// endpoints.grpc.methods[].permission, #2008). The gate's allow/deny behavior is
// covered at the assembly level (corecells/accesscore/grpc_pdp_gate_test.go), so
// these tests assume an already-authorized caller and inject no principal.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	sessionverifyv1 "github.com/ghbvf/gocell/generated/contracts/grpc/auth/session/verify/v1"
)

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

	resp, err := srv.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "good-token"})
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
	// An infrastructure outage (session store / key provider) must surface as a
	// gRPC error, NOT a uniform valid=false — masking an outage as a credential
	// failure would pollute SLO buckets and hide the incident.
	infra := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "authentication service unavailable")
	srv := NewServer(&stubVerifier{err: infra})

	resp, err := srv.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "any-token"})
	if err == nil {
		t.Fatalf("infra-unavailable must return an error, got nil (resp=%v)", resp)
	}
	if !errors.Is(err, infra) {
		t.Errorf("returned error should wrap/equal the infra error, got %v", err)
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

// TestServer_VerifyToken_OverGRPC exercises the full proto round-trip over a real
// transport (bufconn) WITHOUT the auth interceptor — proving the generated
// RegisterSessionVerifyServiceServer wiring and response marshaling work
// end-to-end. The PDP gate is covered separately at the assembly level.
func TestServer_VerifyToken_OverGRPC(t *testing.T) {
	t.Parallel()
	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	sessionverifyv1.RegisterSessionVerifyServiceServer(grpcServer, NewServer(&stubVerifier{claims: validClaims}))
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := sessionverifyv1.NewSessionVerifyServiceClient(conn)
	resp, err := client.VerifyToken(context.Background(), &sessionverifyv1.VerifyTokenRequest{Token: "good-token"})
	if err != nil {
		t.Fatalf("VerifyToken over gRPC: %v", err)
	}
	if !resp.GetValid() || resp.GetSubject() != validClaims.Subject {
		t.Fatalf("round-trip response = %+v, want valid=true subject=%q", resp, validClaims.Subject)
	}
}
