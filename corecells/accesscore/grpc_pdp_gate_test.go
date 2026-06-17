package accesscore

// grpc_pdp_gate_test.go — #1154 assembly-level regression that the gRPC ABAC PDP
// gate actually gates accesscore's SessionVerifyService end-to-end.
//
// The slice handler tests (slices/sessionverifyrpc/handler_test.go) deliberately
// run WITHOUT the auth interceptor — they cover domain behavior only, since #2008
// moved authorization out of the handler into the cross-cutting interceptor. This
// test closes that gap by wiring the REAL production path: interceptor.NewServerInterceptors
// + the REAL accesscore PDP (authorizationdecide.Service over the built-in baseline)
// + the real per-method permission overlay (the same map cell_gen.go derives) + the
// real generated SessionVerifyService descriptor, over a real socket. It asserts the
// gate's allow/deny/unauthenticated verdicts for session:verify.
//
// It is the cell-scoped home for the gate matrix (fast, no bundle/PG/redis). The
// corebundle integration test proves the listener is wired end-to-end (single
// happy-path); it does not re-run this matrix.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/authorizationdecide"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	sessionverifyv1 "github.com/ghbvf/gocell/generated/contracts/grpc/auth/session/verify/v1"
)

const (
	svGateTenantID    = "11111111-1111-1111-1111-111111111111"
	svTokenAdmin      = "admin"
	svTokenViewer     = "viewer"
	svTokenSuperAdmin = "superadmin"
)

// svGateVerifier is a probe IntentTokenVerifier mapping a bearer token to a verified
// user principal with a role set + tenant — enough to drive the REAL accesscore PDP
// (which requires a tenant in context) through the AuthenticateBearer → WithPrincipal
// path without JWT key plumbing.
type svGateVerifier struct{}

func (svGateVerifier) VerifyIntent(_ context.Context, token string, _ kauth.TokenIntent) (kauth.Claims, error) {
	switch token {
	case svTokenAdmin:
		return kauth.Claims{Subject: "admin-1", TenantID: svGateTenantID, Roles: []string{auth.RoleAdmin}, TokenUse: kauth.TokenIntentAccess}, nil
	case svTokenViewer:
		return kauth.Claims{Subject: "viewer-1", TenantID: svGateTenantID, Roles: []string{"viewer"}, TokenUse: kauth.TokenIntentAccess}, nil
	case svTokenSuperAdmin:
		return kauth.Claims{Subject: "sadmin-1", TenantID: svGateTenantID, Roles: []string{auth.RoleSuperAdmin}, TokenUse: kauth.TokenIntentAccess}, nil
	default:
		return kauth.Claims{}, errors.New("unknown token")
	}
}

// svGateProbe is a minimal real-descriptor SessionVerifyService implementation that
// records whether VerifyToken was reached. This test asserts the GATE, not handler
// domain logic, so an allowed call reaching the probe proves the gate permitted it.
type svGateProbe struct {
	sessionverifyv1.UnimplementedSessionVerifyServiceServer
	mu      sync.Mutex
	reached bool
}

func (s *svGateProbe) VerifyToken(context.Context, *sessionverifyv1.VerifyTokenRequest) (*sessionverifyv1.VerifyTokenResponse, error) {
	s.mu.Lock()
	s.reached = true
	s.mu.Unlock()
	return &sessionverifyv1.VerifyTokenResponse{Valid: true, Subject: "probe"}, nil
}

func (s *svGateProbe) wasReached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reached
}

// reset zeroes the reached flag so each gate assertion is independent of the
// prior sub-check's state (removes ordering dependency between sub-checks).
func (s *svGateProbe) reset() {
	s.mu.Lock()
	s.reached = false
	s.mu.Unlock()
}

// startGatedSessionVerifyServer wires the production gRPC auth chain (REAL accesscore
// PDP over an empty policy store → only the built-in baseline applies, real overlay,
// real descriptor) over a real socket and returns a connected client + the probe.
func startGatedSessionVerifyServer(t *testing.T) (sessionverifyv1.SessionVerifyServiceClient, *svGateProbe) {
	t.Helper()

	pdp, err := authorizationdecide.NewService(
		clock.Real(),
		mem.NewPolicyRepository(), // empty → baseline-only decisions
		mem.NewResourceAttributeProvider(),
		slog.Default(),
		authorizationdecide.WithTxManager(outbox.DemoCellTxManager()),
	)
	require.NoError(t, err)

	probe := &svGateProbe{}
	bundle := interceptor.NewServerInterceptors(interceptor.Deps{
		Clock:           clock.Real(),
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Verifier:        svGateVerifier{},
		Authorizer:      pdp, // the REAL accesscore PDP
		CellIDClosedSet: []string{"accesscore"},
	})
	reg := bundle.Registrar()
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg.BindServer(srv)
	require.NoError(t, reg.Register(cell.GRPCServiceSpec{
		ContractID: "grpc.auth.session.verify.v1",
		CellID:     "accesscore",
		Listener:   cell.PrimaryListener,
		MethodPermissions: map[string]string{
			"/auth.session.verify.v1.SessionVerifyService/VerifyToken": "session:verify",
		},
		Register: func(r grpc.ServiceRegistrar) {
			sessionverifyv1.RegisterSessionVerifyServiceServer(r, probe)
		},
	}), "registering the gated SessionVerifyService must succeed (Authorizer wired)")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return sessionverifyv1.NewSessionVerifyServiceClient(conn), probe
}

func svBearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// TestSessionVerifyGRPC_PDPGate_EndToEnd asserts the session:verify gate: no token →
// Unauthenticated; non-admin role → PermissionDenied (handler not reached); admin →
// gate allows → handler reached; super-admin → gate allows → handler reached.
// Verdicts come from the REAL accesscore baseline PDP over the real interceptor chain.
// Each sub-check calls probe.reset() first to remove ordering dependency.
func TestSessionVerifyGRPC_PDPGate_EndToEnd(t *testing.T) {
	t.Parallel()
	client, probe := startGatedSessionVerifyServer(t)
	req := &sessionverifyv1.VerifyTokenRequest{Token: "subject-token-to-introspect"}

	// No authorization metadata → gate rejects before the handler (Unauthenticated).
	probe.reset()
	_, err := client.VerifyToken(context.Background(), req)
	assert.Equal(t, codes.Unauthenticated, status.Code(err), "missing token must be Unauthenticated")
	assert.False(t, probe.wasReached(), "handler must not run for an unauthenticated caller")

	// Authenticated but non-admin (viewer) → baseline denies session:verify.
	probe.reset()
	_, err = client.VerifyToken(svBearer(context.Background(), svTokenViewer), req)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "viewer must be PermissionDenied for session:verify")
	assert.False(t, probe.wasReached(), "handler must not run for a denied caller")

	// Admin → baseline allows → handler reached.
	probe.reset()
	resp, err := client.VerifyToken(svBearer(context.Background(), svTokenAdmin), req)
	require.NoError(t, err, "admin must pass the session:verify gate")
	assert.True(t, resp.GetValid())
	assert.True(t, probe.wasReached(), "handler must run for the authorized admin")

	// Super-admin → baseline allows → handler reached (symmetric with admin).
	probe.reset()
	resp, err = client.VerifyToken(svBearer(context.Background(), svTokenSuperAdmin), req)
	require.NoError(t, err, "super-admin must pass the session:verify gate")
	assert.True(t, resp.GetValid())
	assert.True(t, probe.wasReached(), "handler must run for the authorized super-admin")
}
