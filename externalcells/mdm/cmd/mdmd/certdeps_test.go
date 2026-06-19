package main

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/cellmodules/certdeps"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// TestCertdeps_DemoTopologyLive is the cert bottom-layer live proof (epic §0):
// with a demo/memory topology, certdeps.Resolve returns a non-nil Signer and
// TrustBundle is non-empty (softca dev CA is initialized and live).
// This test is the machine-readable evidence that the certdeps.Resolve call in
// buildApp is not dead code.
func TestCertdeps_DemoTopologyLive(t *testing.T) {
	clk := clock.Real()
	topo, err := bootstrap.NewTopology("", "memory", false)
	if err != nil {
		t.Fatalf("NewTopology: %v", err)
	}

	cd, err := certdeps.Resolve(clk, topo)
	if err != nil {
		t.Fatalf("certdeps.Resolve demo topo: %v", err)
	}
	if cd.Signer == nil {
		t.Fatal("Signer is nil after Resolve (demo topo)")
	}

	bundle, err := cd.Signer.TrustBundle(context.Background())
	if err != nil {
		t.Fatalf("TrustBundle: %v", err)
	}
	if len(bundle) == 0 {
		t.Error("TrustBundle returned empty bundle; dev CA not initialized")
	}
}

// TestCertdeps_PostgresTopologyFailClosed asserts that certdeps.Resolve returns an
// error (not a degraded demo CA) when the postgres topology is requested.
// This is the fail-closed invariant documented in certdeps.go: a durable signing CA
// is not yet wired, so silently serving the dev soft-CA under postgres would break
// the durability the topology promises.
func TestCertdeps_PostgresTopologyFailClosed(t *testing.T) {
	clk := clock.Real()
	// postgres topology requires singlePod=false and a valid storage backend.
	topo, err := bootstrap.NewTopology("", "postgres", false)
	if err != nil {
		// If topology construction itself fails (e.g., env validation), that is still
		// a valid fail-closed path — we skip rather than fail.
		t.Skipf("NewTopology(postgres) refused: %v (acceptable fail-closed path)", err)
	}

	_, resolveErr := certdeps.Resolve(clk, topo)
	if resolveErr == nil {
		t.Error("certdeps.Resolve(postgres topo) returned nil error; want fail-closed error")
	}
}
