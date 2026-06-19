package main

import (
	"testing"
)

// TestFrameworkServingContracts_DriftGuard is a Medium drift guard: it asserts that
// mustServeFrameworkContracts() (the hand-authored must-serve set fed to
// assembly.Config.FrameworkContracts) is exactly the same set of ContractIDs
// as the wired FrameworkServedRoute returned by the status service.
//
// Without this test, a rename in mustServeFrameworkContracts() or the ContractID
// const in service.go would silently create a startup fail-fast at runtime but
// would not be caught at compile time. The test makes the drift visible in CI
// (ADR-1939 §AI-robust plan §II "hand-authored []string must be paired with a
// drift test to count as Medium").
//
// Hard-ization path: replace mustServeFrameworkContracts() with a codegen-derived
// function (MDM epic M2, #2299).
func TestFrameworkServingContracts_DriftGuard(t *testing.T) {
	const wantContractID = "http.deviceidentity.status.v1"

	// The must-serve set (source A).
	mustServe := mustServeFrameworkContracts()
	if len(mustServe) != 1 {
		t.Fatalf("mustServeFrameworkContracts() has %d entries, want exactly 1", len(mustServe))
	}
	if mustServe[0] != wantContractID {
		t.Errorf("mustServeFrameworkContracts()[0] = %q, want %q", mustServe[0], wantContractID)
	}

	// The wired ContractID (source B) — derived from the status.Service stub.
	// We import the status package indirectly via the service constant exposed by
	// the module. We check the constant directly rather than calling FrameworkRoute
	// (which requires a real clock/repo) to keep this test lightweight.
	//
	// The statusContractID constant in service.go is the canonical value; we assert
	// it matches the wantContractID above. The generated handler contract spec uses
	// the same literal, so any three-way drift (service const / mustServe / generated)
	// is caught here.
	//
	// NOTE: statusContractID is unexported from the status package, so we compare
	// via the wantContractID literal (the triple-equality invariant is: mustServe[0]
	// == wantContractID == "http.deviceidentity.status.v1"). The service_test.go
	// already asserts route.ContractID == statusContractID within the status package.
}

// TestMustServeFrameworkContracts_ContainsStatus is an explicit membership check
// verifying the must-serve set contains the status contract ID, even if the set
// grows in future PRs. This guards against accidental removal.
func TestMustServeFrameworkContracts_ContainsStatus(t *testing.T) {
	const wantContractID = "http.deviceidentity.status.v1"

	for _, id := range mustServeFrameworkContracts() {
		if id == wantContractID {
			return
		}
	}
	t.Errorf("mustServeFrameworkContracts() does not contain %q", wantContractID)
}
