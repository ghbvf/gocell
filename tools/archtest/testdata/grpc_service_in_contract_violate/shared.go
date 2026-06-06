// Package grpc_service_in_contract_violate is a synthetic fixture for the
// GRPC-SERVICE-IN-CONTRACT-01 archtest rule. It contains reg.GRPCService
// callsites in: the sanctioned cell_gen.go (banner → allowed), a sibling
// generated healthz_gen.go (banner, wrong basename → must fire), and a rogue
// non-generated file (→ must fire).
//
// DO NOT use this package in production code.
package grpc_service_in_contract_violate

import (
	"github.com/ghbvf/gocell/kernel/cell"
)

// fixtureSpec builds a minimal GRPCServiceSpec for the fixture callsites.
// Register is non-nil (a no-op func) so that Validate() passes.
func fixtureSpec() cell.GRPCServiceSpec {
	return cell.GRPCServiceSpec{
		ContractID: "grpc.fixture.v1",
		CellID:     "fixturecell",
		Listener:   cell.PrimaryListener,
		Register: func() {}, // wrong type intentionally: real type is func(grpc.ServiceRegistrar); this fixture only feeds the AST scan and never reaches runtime, so Validate()'s nil-only check passes and the grpc import is avoided.
	}
}
