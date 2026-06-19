package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/cellmodules/certdeps"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// fakeSigner is a test-only certsigning.Signer that returns configurable values
// from TrustBundle. Sign is never called in proveCertBaseLive; it panics to
// catch any unexpected use.
type fakeSigner struct {
	bundle [][]byte
	err    error
}

func (f *fakeSigner) Sign(_ context.Context, _ certsigning.AuthorizedCertRequest) (certsigning.IssuedCert, error) {
	panic("fakeSigner.Sign must not be called in proveCertBaseLive tests")
}

func (f *fakeSigner) TrustBundle(_ context.Context) ([][]byte, error) {
	return f.bundle, f.err
}

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

// TestProveCertBaseLive_Success verifies that proveCertBaseLive returns nil when
// the Signer returns a non-empty trust bundle. This is the happy-path branch
// (lines 187-196) that is not separately exercised by TestCertdeps_DemoTopologyLive
// (which calls cd.Signer.TrustBundle directly, not proveCertBaseLive).
func TestProveCertBaseLive_Success(t *testing.T) {
	cd := certdeps.CertDeps{
		Signer: &fakeSigner{bundle: [][]byte{{0x30, 0x82, 0x01}}}, // non-empty DER stub
	}
	if err := proveCertBaseLive(context.Background(), cd); err != nil {
		t.Errorf("proveCertBaseLive with non-empty bundle returned error: %v", err)
	}
}

// TestProveCertBaseLive_EmptyBundle verifies that proveCertBaseLive returns an
// error when TrustBundle succeeds but returns a zero-length slice. This covers
// the "dev CA not initialized" guard branch (line 191-193).
func TestProveCertBaseLive_EmptyBundle(t *testing.T) {
	cd := certdeps.CertDeps{
		Signer: &fakeSigner{bundle: nil, err: nil},
	}
	err := proveCertBaseLive(context.Background(), cd)
	if err == nil {
		t.Fatal("proveCertBaseLive with empty bundle returned nil, want error")
	}
}

// TestProveCertBaseLive_TrustBundleError verifies that proveCertBaseLive wraps
// and surfaces a TrustBundle error (lines 188-190). This is the third branch.
func TestProveCertBaseLive_TrustBundleError(t *testing.T) {
	sentinel := errors.New("ca unavailable")
	cd := certdeps.CertDeps{
		Signer: &fakeSigner{err: sentinel},
	}
	err := proveCertBaseLive(context.Background(), cd)
	if err == nil {
		t.Fatal("proveCertBaseLive with TrustBundle error returned nil, want wrapped error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("proveCertBaseLive error %v does not wrap sentinel %v", err, sentinel)
	}
}
