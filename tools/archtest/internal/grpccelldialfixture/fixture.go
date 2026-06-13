//go:build archtest_fixture

// Package grpccelldialfixture is the GRPC-CELL-NO-CLIENT-DIAL-01 negative
// fixture. It stages the forbidden cross-cell gRPC *client*-construction forms a
// cell must never use — the chokepoint being any construction of / reference to
// a gRPC ClientConn, which (since a cell reaches external systems only through
// adapters/) can only mean dialing a sibling cell in-process. It also stages a
// legitimate server-registration GREEN anchor (naming grpc.ServiceRegistrar)
// that must NOT fire, proving the scan distinguishes client construction from
// server registration by SYMBOL, not by file/import (a cell legitimately imports
// google.golang.org/grpc for the cellgen-generated Register callback — see
// .golangci.yml cells-isolation — so an import-level ban is impossible).
//
// Loaded only under the archtest_fixture build tag (via Run(t, Fixture(...)));
// never imported from production code.
//
// DO NOT use this package in production code.
package grpccelldialfixture

import "google.golang.org/grpc"

// dialSibling constructs a gRPC client connection — the forbidden chokepoint.
// Returns *grpc.ClientConn, so it also exercises the client-conn type detection.
func dialSibling() (*grpc.ClientConn, error) {
	return grpc.NewClient("passthrough:///sibling-cell") // VIOLATION [dial]
}

// siblingProxy holds a *grpc.ClientConn — a cell retaining a cross-cell client
// connection as state.
type siblingProxy struct {
	conn *grpc.ClientConn // VIOLATION [conn-type]
}

// wrapConn names grpc.ClientConnInterface — the client-conn abstraction a cell
// would hold to invoke a sibling cell's generated stub.
func wrapConn(cc grpc.ClientConnInterface) grpc.ClientConnInterface { // VIOLATION [conn-type]
	return cc
}

// registerServer is the GREEN anchor: legitimate server registration names
// grpc.ServiceRegistrar (server side) and must NOT fire.
func registerServer(r grpc.ServiceRegistrar) { _ = r }

// Keep the fixture symbols referenced so the package type-checks cleanly.
var (
	_ = dialSibling
	_ = wrapConn
	_ = registerServer
	_ = siblingProxy{}
)
