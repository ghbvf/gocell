package healthz

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewProbe_NameAndCheck(t *testing.T) {
	t.Parallel()

	called := false
	wantErr := errors.New("boom")
	p := NewProbe("foo_ready", func(ctx context.Context) error {
		called = true
		return wantErr
	})

	if got := p.Name(); got != "foo_ready" {
		t.Errorf("Name() = %q, want %q", got, "foo_ready")
	}
	if err := p.Check(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("Check() error = %v, want %v", err, wantErr)
	}
	if !called {
		t.Errorf("Check() did not invoke fn")
	}
}

func TestNewProbe_EmptyNamePanics(t *testing.T) {
	t.Parallel()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = NewProbe("", func(ctx context.Context) error { return nil })
	}()

	if recovered == nil {
		t.Errorf("expected panic on empty name")
		return
	}
	// Verify panic reason from panicregister.Approved wraps the site correctly:
	// the payload is *errcode.Error from errcode.Assertion, which implements error.
	err, ok := recovered.(error)
	if !ok {
		t.Fatalf("recovered value is %T, want error (from errcode.Assertion)", recovered)
	}
	// The panic reason string "healthz-probe-empty-name" appears in the Assertion message.
	if !strings.Contains(err.Error(), "healthz.NewProbe") {
		t.Errorf("panic error message %q does not contain %q", err.Error(), "healthz.NewProbe")
	}
}

func TestNewProbe_NilFnPanics(t *testing.T) {
	t.Parallel()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = NewProbe("name", nil)
	}()

	if recovered == nil {
		t.Errorf("expected panic on nil fn")
		return
	}
	err, ok := recovered.(error)
	if !ok {
		t.Fatalf("recovered value is %T, want error (from errcode.Assertion)", recovered)
	}
	if !strings.Contains(err.Error(), "healthz.NewProbe") {
		t.Errorf("panic error message %q does not contain %q", err.Error(), "healthz.NewProbe")
	}
}

// TestProbeSet_StubImplements verifies that a type satisfying ProbeSet compiles
// and that Probes() returns the expected slice.
func TestProbeSet_StubImplements(t *testing.T) {
	t.Parallel()

	stub := probeSetStub{probes: []Probe{
		NewProbe("a_ready", func(ctx context.Context) error { return nil }),
		NewProbe("b_ready", func(ctx context.Context) error { return nil }),
	}}
	if got := len(stub.Probes()); got != 2 {
		t.Errorf("Probes() len = %d, want 2", got)
	}
	if got := stub.Probes()[0].Name(); got != "a_ready" {
		t.Errorf("Probes()[0].Name() = %q, want %q", got, "a_ready")
	}
}

// Compile-time interface assertions — Probe, RepoProber, and ProbeSet must be
// named interfaces in the package so cells/adapters can satisfy them.
var (
	_ Probe      = (*funcProbe)(nil) // funcProbe is the internal implementation of NewProbe
	_ RepoProber = repoProberStub{}  // defined in test file
	_ ProbeSet   = probeSetStub{}    // defined in test file
)

type repoProberStub struct{}

func (repoProberStub) RepoReady(ctx context.Context) error { return nil }

type probeSetStub struct{ probes []Probe }

func (s probeSetStub) Probes() []Probe { return s.probes }
