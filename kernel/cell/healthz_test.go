package cell

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// probeEmitter implements both outbox.Emitter and healthz.ProbeSet, modeling
// a DirectEmitter that exposes fail-open-rate probes.
type probeEmitter struct {
	probes []healthz.Probe
}

func (e *probeEmitter) Emit(context.Context, outbox.Entry) error { return nil }
func (e *probeEmitter) Probes() []healthz.Probe                  { return e.probes }

// plainEmitter implements outbox.Emitter only — it is NOT a healthz.ProbeSet,
// modeling a WriterEmitter / NoopEmitter that exposes no probes.
type plainEmitter struct{}

func (plainEmitter) Emit(context.Context, outbox.Entry) error { return nil }

func newRecorder() *RegistryRecorder {
	return NewRegistryRecorder(nil, outbox.DurabilityDurable)
}

func probe(name string) healthz.Probe {
	return healthz.NewProbe(healthz.MustProbeName(name), func(context.Context) error { return nil })
}

func TestRegisterEmitterHealthProbes(t *testing.T) {
	t.Parallel()

	// Success / no-op paths only; the Register-error path is covered by
	// TestRegisterEmitterHealthProbes_RegisterErrorPropagates below.
	tests := []struct {
		name       string
		emitter    func() outbox.Emitter
		wantProbes []healthz.ProbeName
	}{
		{
			name:       "bare-nil emitter registers nothing",
			emitter:    func() outbox.Emitter { return nil },
			wantProbes: nil,
		},
		{
			name: "typed-nil emitter registers nothing (no Probes() panic)",
			emitter: func() outbox.Emitter {
				var e *probeEmitter // typed nil
				return e
			},
			wantProbes: nil,
		},
		{
			name:       "non-ProbeSet emitter registers nothing",
			emitter:    func() outbox.Emitter { return plainEmitter{} },
			wantProbes: nil,
		},
		{
			name: "ProbeSet emitter with zero probes registers nothing",
			emitter: func() outbox.Emitter {
				return &probeEmitter{probes: nil}
			},
			wantProbes: nil,
		},
		{
			name: "ProbeSet emitter registers its single probe",
			emitter: func() outbox.Emitter {
				return &probeEmitter{probes: []healthz.Probe{probe("outbox_failopen_rate_testcell")}}
			},
			wantProbes: []healthz.ProbeName{healthz.MustProbeName("outbox_failopen_rate_testcell")},
		},
		{
			name: "ProbeSet emitter registers all probes",
			emitter: func() outbox.Emitter {
				return &probeEmitter{probes: []healthz.Probe{probe("p_one"), probe("p_two")}}
			},
			wantProbes: []healthz.ProbeName{healthz.MustProbeName("p_one"), healthz.MustProbeName("p_two")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := newRecorder()
			if err := RegisterEmitterHealthProbes(reg, tt.emitter()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := reg.Snapshot().Probes
			if len(got) != len(tt.wantProbes) {
				t.Fatalf("registered %d probes, want %d", len(got), len(tt.wantProbes))
			}
			for i, want := range tt.wantProbes {
				if got[i].Name() != want {
					t.Errorf("probe[%d] = %q, want %q", i, got[i].Name().String(), want.String())
				}
			}
		})
	}
}

// TestRegisterEmitterHealthProbes_RegisterErrorPropagates asserts a duplicate
// probe name surfaced by the Aggregator bubbles out of the helper.
func TestRegisterEmitterHealthProbes_RegisterErrorPropagates(t *testing.T) {
	t.Parallel()
	reg := newRecorder()
	// Two probes with the same name: the second Register returns ErrDuplicateProbe.
	em := &probeEmitter{probes: []healthz.Probe{probe("dup"), probe("dup")}}
	err := RegisterEmitterHealthProbes(reg, em)
	if err == nil {
		t.Fatalf("expected duplicate-probe error, got nil")
	}
	if !errors.Is(err, healthz.ErrDuplicateProbe) {
		t.Errorf("error = %v, want ErrDuplicateProbe", err)
	}
}
