package main

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"

	"github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status"
	statusmem "github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status/mem"
)

// TestFrameworkServingContracts_DriftGuard is a Medium drift guard: it asserts that
// mustServeFrameworkContracts() (Source A: the hand-authored must-serve set fed to
// assembly.Config.FrameworkContracts) and status.NewService(...).FrameworkRoute().ContractID
// (Source B: the runtime wiring path) agree on the single framework-contract ID.
//
// Both sources are machine-read: Source A via mustServeFrameworkContracts(), Source B via
// constructing a real status.Service and calling FrameworkRoute(). Neither is a
// hard-coded literal, so any rename in either path causes this test to fail rather
// than silently diverging until a runtime startup fail-fast.
//
// Hard-ization path: replace mustServeFrameworkContracts() with a codegen-derived
// function (MDM epic M2, #2299).
func TestFrameworkServingContracts_DriftGuard(t *testing.T) {
	// Source A: the hand-authored must-serve set (references status.ContractID via const).
	mustServe := mustServeFrameworkContracts()
	if len(mustServe) != 1 {
		t.Fatalf("mustServeFrameworkContracts() has %d entries, want exactly 1", len(mustServe))
	}

	// Source B: the runtime wiring path — construct a real status.Service and read
	// FrameworkRoute().ContractID. Uses an in-memory repo and a real clock so no
	// mocking is required; the call is lightweight and does not start a server.
	clk := clock.Real()
	repo := statusmem.New(clk)
	svc := status.NewService(repo, clk)
	wiredContractID := svc.FrameworkRoute().ContractID

	// Both sources must agree: if mustServeFrameworkContracts() or ContractID drift,
	// bootstrap's validateFrameworkServing will fail-fast at startup — this test
	// makes that drift visible in CI long before a binary is built.
	if mustServe[0] != wiredContractID {
		t.Errorf("drift detected: mustServeFrameworkContracts()[0]=%q != FrameworkRoute().ContractID=%q",
			mustServe[0], wiredContractID)
	}
}

// TestMustServeFrameworkContracts_ContainsStatus is an explicit membership check
// verifying the must-serve set contains the status contract ID, even if the set
// grows in future PRs. This guards against accidental removal.
func TestMustServeFrameworkContracts_ContainsStatus(t *testing.T) {
	for _, id := range mustServeFrameworkContracts() {
		if id == status.ContractID {
			return
		}
	}
	t.Errorf("mustServeFrameworkContracts() does not contain status.ContractID (%q)", status.ContractID)
}
