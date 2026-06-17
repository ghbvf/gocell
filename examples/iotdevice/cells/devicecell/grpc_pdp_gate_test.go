package devicecell

// grpc_pdp_gate_test.go — #2008 F3: assembly-level regression that the gRPC ABAC
// PDP gate actually gates the iotdevice DeviceCommandService end-to-end.
//
// The slice handler tests (devicecommandrpc/*_test.go) deliberately run WITHOUT the
// auth interceptor — they cover domain behavior only, since #2008 moved authorization
// out of the handler into the cross-cutting interceptor. This test closes that gap by
// wiring the REAL production path: interceptor.NewServerInterceptors + the real
// example PDP (deviceAuthorizer{}) + the real per-method permission overlay (the same
// map cell_gen.go derives) + the real generated DeviceCommandService descriptor, over
// a real socket. It asserts the gate's allow/deny/unauthenticated verdicts for both a
// unary (IssueCommand) and a server-streaming (WatchCommands) RPC.
//
// The handler is a thin probe (records reach for the unary; leaves WatchCommands at
// the embedded Unimplemented): this test asserts the GATE, not handler domain logic.
// For an allowed stream, reaching codes.Unimplemented (not PermissionDenied) proves
// the gate permitted the call through to the handler.

import (
	"context"
	"errors"
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

	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"

	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// Bearer tokens the probe verifier understands; the token string selects the role.
const (
	tokenOperator = "operator"
	tokenViewer   = "viewer"
	// tokenDeviceSelf authenticates as the device "device-1" itself (subject ==
	// device-1, non-privileged role) — the #2207 device-self watch path.
	tokenDeviceSelf = "device-self"
	// watchedDeviceID is the device_id the WatchCommands requests target; the
	// device-self token's subject equals it so the owner-scoped gate matches.
	watchedDeviceID = "device-1"
)

// rolesVerifier is a probe IntentTokenVerifier mapping a bearer token to a verified
// user principal with a role set — enough to drive the real deviceAuthorizer through
// the real AuthenticateBearer → WithPrincipal path (no JWT key plumbing needed).
type rolesVerifier struct{}

func (rolesVerifier) VerifyIntent(_ context.Context, token string, _ kauth.TokenIntent) (kauth.Claims, error) {
	switch token {
	case tokenOperator:
		return kauth.Claims{Subject: "operator-1", Roles: []string{dto.RoleOperator}, TokenUse: kauth.TokenIntentAccess}, nil
	case tokenViewer:
		return kauth.Claims{Subject: "viewer-1", Roles: []string{"role:viewer"}, TokenUse: kauth.TokenIntentAccess}, nil
	case tokenDeviceSelf:
		// The device itself: subject == watchedDeviceID, non-privileged role. Passes
		// device:consume only via the ownership rule (subject == resource), not opOrAdmin.
		return kauth.Claims{Subject: watchedDeviceID, Roles: []string{"role:device"}, TokenUse: kauth.TokenIntentAccess}, nil
	default:
		return kauth.Claims{}, errors.New("unknown token")
	}
}

// gateProbeServer is a minimal real-descriptor DeviceCommandService implementation
// that records whether IssueCommand was reached. WatchCommands is left to the embedded
// Unimplemented (returns codes.Unimplemented), which for an allowed stream proves the
// gate let the call through (Unimplemented != PermissionDenied).
type gateProbeServer struct {
	commandv1.UnimplementedDeviceCommandServiceServer
	mu           sync.Mutex
	issueReached bool
}

func (s *gateProbeServer) IssueCommand(context.Context, *commandv1.IssueCommandRequest) (*commandv1.IssueCommandResponse, error) {
	s.mu.Lock()
	s.issueReached = true
	s.mu.Unlock()
	return &commandv1.IssueCommandResponse{AckId: "cmd-probe"}, nil
}

func (s *gateProbeServer) reached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueReached
}

// methodPermissionsFixture mirrors the overlay cell_gen.go derives for the
// DeviceCommandService (#2207): IssueCommand stays device:command (coarse);
// WatchCommands is device:consume (owner-scoped). Kept as the same wire strings the
// generator emits so this e2e exercises the identical mapping.
var methodPermissionsFixture = map[string]string{
	"/device.command.v1.DeviceCommandService/IssueCommand":  "device:command",
	"/device.command.v1.DeviceCommandService/WatchCommands": "device:consume",
}

// methodResourcesFixture mirrors the GRPCServiceSpec.MethodResources cell_gen.go
// derives from endpoints.grpc.methods[].resource (#2207): WatchCommands extracts the
// per-message device_id field as the PDP resource so a device can watch its own queue.
var methodResourcesFixture = map[string]string{
	"/device.command.v1.DeviceCommandService/WatchCommands": "device_id",
}

// startGatedDeviceCommandServer wires the production gRPC auth chain (real PDP, real
// overlay, real descriptor) over a real socket and returns a connected client + the
// probe handler.
func startGatedDeviceCommandServer(t *testing.T) (commandv1.DeviceCommandServiceClient, *gateProbeServer) {
	t.Helper()

	probe := &gateProbeServer{}
	bundle := interceptor.NewServerInterceptors(interceptor.Deps{
		Clock:           clock.Real(),
		Collector:       metrics.NewInMemoryGRPCCollector(),
		Verifier:        rolesVerifier{},
		Authorizer:      deviceAuthorizer{}, // the REAL example-owned PDP
		CellIDClosedSet: []string{"devicecell"},
	})
	reg := bundle.Registrar()
	srv := grpc.NewServer(bundle.ServerOptions()...)
	reg.BindServer(srv)
	require.NoError(t, reg.Register(cell.GRPCServiceSpec{
		ContractID:        "grpc.device.command.v1",
		CellID:            "devicecell",
		Listener:          cell.PrimaryListener,
		MethodPermissions: methodPermissionsFixture,
		MethodResources:   methodResourcesFixture,
		Register: func(r grpc.ServiceRegistrar) {
			commandv1.RegisterDeviceCommandServiceServer(r, probe)
		},
	}), "registering the gated DeviceCommandService must succeed (Authorizer wired)")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return commandv1.NewDeviceCommandServiceClient(conn), probe
}

func bearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// TestDeviceCommandGRPC_PDPGate_Unary_EndToEnd asserts the unary IssueCommand gate:
// no token → Unauthenticated; non-privileged role → PermissionDenied; operator →
// handler reached. Verdicts come from the real deviceAuthorizer over the real chain.
func TestDeviceCommandGRPC_PDPGate_Unary_EndToEnd(t *testing.T) {
	t.Parallel()
	client, probe := startGatedDeviceCommandServer(t)
	req := &commandv1.IssueCommandRequest{DeviceId: "device-1", CommandType: "reboot", Payload: []byte("{}")}

	// No authorization metadata → gate rejects before the handler (Unauthenticated).
	_, err := client.IssueCommand(context.Background(), req)
	assert.Equal(t, codes.Unauthenticated, status.Code(err), "missing token must be Unauthenticated")
	assert.False(t, probe.reached(), "handler must not run for an unauthenticated caller")

	// Authenticated but non-privileged (no operator/admin) → PDP denies.
	_, err = client.IssueCommand(bearer(context.Background(), tokenViewer), req)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "viewer must be PermissionDenied for device:command")
	assert.False(t, probe.reached(), "handler must not run for a denied caller")

	// Operator → PDP allows → handler reached.
	resp, err := client.IssueCommand(bearer(context.Background(), tokenOperator), req)
	require.NoError(t, err, "operator must pass the device:command gate")
	assert.Equal(t, "cmd-probe", resp.GetAckId())
	assert.True(t, probe.reached(), "handler must run for the authorized operator")
}

// TestDeviceCommandGRPC_PDPGate_Stream_EndToEnd asserts the owner-scoped WatchCommands
// gate (#2207). WatchCommands is device:consume with resource=device_id: the gate
// defers to the first RecvMsg, extracts device_id, and forwards it as the PDP resource.
// An allowed call reaches the (Unimplemented) handler; a denial surfaces on Recv.
func TestDeviceCommandGRPC_PDPGate_Stream_EndToEnd(t *testing.T) {
	t.Parallel()
	client, _ := startGatedDeviceCommandServer(t)
	req := &commandv1.WatchCommandsRequest{DeviceId: watchedDeviceID}

	// Viewer → neither admin/operator nor the device itself → PermissionDenied.
	stream, err := client.WatchCommands(bearer(context.Background(), tokenViewer), req)
	require.NoError(t, err, "stream open RPC itself returns; the gate verdict surfaces on Recv")
	_, err = stream.Recv()
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "viewer must be PermissionDenied for WatchCommands")

	// Operator → opOrAdmin passes device:consume regardless of resource → reaches handler.
	stream, err = client.WatchCommands(bearer(context.Background(), tokenOperator), req)
	require.NoError(t, err)
	_, err = stream.Recv()
	assert.Equal(t, codes.Unimplemented, status.Code(err),
		"operator must pass the gate (reaching the unimplemented handler, not a denial)")

	// #2207 device-self: the device watching its OWN queue (subject == device_id) passes
	// the owner-scoped gate — the consume-semantics parity this issue closes.
	stream, err = client.WatchCommands(bearer(context.Background(), tokenDeviceSelf), req)
	require.NoError(t, err)
	_, err = stream.Recv()
	assert.Equal(t, codes.Unimplemented, status.Code(err),
		"the device itself must pass the device:consume gate for its own queue (#2207)")

	// Cross-device: the device-1 principal watching device-2's queue (subject != device_id)
	// is denied — ownership is per-message, tenant/device-agnostic.
	stream, err = client.WatchCommands(bearer(context.Background(), tokenDeviceSelf),
		&commandv1.WatchCommandsRequest{DeviceId: "device-2"})
	require.NoError(t, err)
	_, err = stream.Recv()
	assert.Equal(t, codes.PermissionDenied, status.Code(err),
		"a device must NOT watch another device's queue (subject != resource)")
}
