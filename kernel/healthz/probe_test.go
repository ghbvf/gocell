package healthz

import (
	"context"
	"errors"
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

	defer func() {
		if recover() == nil {
			t.Errorf("expected panic on empty name")
		}
	}()
	_ = NewProbe("", func(ctx context.Context) error { return nil })
}

func TestNewProbe_NilFnPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Errorf("expected panic on nil fn")
		}
	}()
	_ = NewProbe("name", nil)
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
