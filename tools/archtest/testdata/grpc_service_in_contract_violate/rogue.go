package grpc_service_in_contract_violate

import "github.com/ghbvf/gocell/kernel/cell"

// rogueGRPCService calls reg.GRPCService from a hand-written, non-generated,
// non-test file. This must trigger GRPC-SERVICE-IN-CONTRACT-01/A — hand-rolled
// gRPC service registrations bypass the cellgen single source of truth.
func rogueGRPCService(reg cell.Registrar) {
	// VIOLATION: reg.GRPCService called from a non-allowlisted file.
	_ = reg.GRPCService(fixtureSpec())
}
