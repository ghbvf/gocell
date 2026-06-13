//go:build archtest_fixture

// Package grpccelldialfixture is the GRPC-CELL-NO-CLIENT-DIAL-01 negative
// fixture. It stages the forbidden cross-cell gRPC CLIENT forms a cell must
// never use — both CONSTRUCTING a client (dial primitive, generated New*Client)
// and merely HOLDING a generated <Svc>Client interface (the injection-then-call
// escape: a composition root injects a built client, the cell keeps only the
// interface and calls it). It also stages the legitimate server-registration
// GREEN surface (grpc.ServiceRegistrar + the generated <Svc>Server interface +
// Register<Svc>Server) that must NOT fire — proving the scan distinguishes the
// client surface from the server surface by SYMBOL (suffix Client vs Server),
// not by file/import (a cell legitimately imports both grpc and the generated
// contract for server registration).
//
// Loaded only under the archtest_fixture build tag (via Run(t, Fixture(...)));
// never imported from production code. It DOES import the real generated module
// (tools/go.mod require+replace github.com/ghbvf/gocell/generated), so the
// constructor + interface RED paths have real AST coverage (#1961 F2).
//
// DO NOT use this package in production code.
package grpccelldialfixture

import (
	"google.golang.org/grpc"

	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
)

// dialSibling constructs a gRPC client connection — the dial chokepoint.
// Returns *grpc.ClientConn, so it also exercises the client-conn type detection.
func dialSibling() (*grpc.ClientConn, error) {
	return grpc.NewClient("passthrough:///sibling-cell") // VIOLATION [dial]
}

// siblingProxy holds a *grpc.ClientConn — a cell retaining a cross-cell client
// connection as state.
type siblingProxy struct {
	conn *grpc.ClientConn // VIOLATION [conn-type]
}

// wrapConn names grpc.ClientConnInterface — the client-conn abstraction.
func wrapConn(cc grpc.ClientConnInterface) grpc.ClientConnInterface { // VIOLATION [conn-type]
	return cc
}

// makeSiblingClient constructs a generated client stub.
func makeSiblingClient(cc grpc.ClientConnInterface) commandv1.DeviceCommandServiceClient { // VIOLATION [generated-client]
	return commandv1.NewDeviceCommandServiceClient(cc) // VIOLATION [generated-client]
}

// injectedClientHolder holds the generated <Svc>Client INTERFACE — the
// injection-then-call escape (#1961 F1): the cell keeps only the interface, a
// composition root injects a built client, the cell calls it; no ClientConn /
// New*Client ever appears in the cell.
type injectedClientHolder struct {
	client commandv1.DeviceCommandServiceClient // VIOLATION [generated-client]
}

// registerServer is the GREEN anchor: the server-registration surface
// (grpc.ServiceRegistrar + the generated <Svc>Server interface + the
// Register<Svc>Server registrar — all suffixed "Server") must NOT fire.
func registerServer(r grpc.ServiceRegistrar, srv commandv1.DeviceCommandServiceServer) {
	commandv1.RegisterDeviceCommandServiceServer(r, srv)
}

// Keep the fixture symbols referenced so the package type-checks cleanly.
var (
	_ = dialSibling
	_ = wrapConn
	_ = makeSiblingClient
	_ = registerServer
	_ = siblingProxy{}
	_ = injectedClientHolder{}
)
