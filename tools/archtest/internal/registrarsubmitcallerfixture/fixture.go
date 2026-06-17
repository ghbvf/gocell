//go:build archtest_fixture

// Package registrarsubmitcallerfixture is the RED fixture for
// REGISTRAR-SUBMIT-CALLER-01.
//
// It calls registry.ContractRegistrar.Submit DIRECTLY from a package that is NOT
// the governance registration gate — the exact bypass the funnel forbids:
// reaching the sealed `submitted` state without passing the governance gate. The
// caller-allowlist scan must fire on this call; total==0 means the scanner is
// fail-open.
package registrarsubmitcallerfixture

import (
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
)

// Violation submits a registration without going through the governance gate.
func Violation() (registry.ContractRegistration, error) {
	r := registry.NewContractRegistrar(clock.Real())
	return r.Submit(registry.SubmitInput{ID: "x", Kind: "event", Submitter: "y"})
}
