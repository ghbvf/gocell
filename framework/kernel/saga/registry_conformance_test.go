package saga_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/kernel/saga/sagaregtest"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
)

// noopRunExt is a minimal valid StepFunc for use in the external test package.
var noopRunExt saga.StepFunc = func(_ context.Context, _ *saga.Instance, _ []byte) ([]byte, error) {
	return nil, nil
}

// makeTestDef is the local equivalent of the internal makeValidDef helper for
// the external test package.
func makeTestDef(id string) *saga.Definition {
	return &saga.Definition{
		ID: idutil.SafeID(id),
		Steps: []saga.Step{
			{Name: idutil.SafeID("step-1"), Run: noopRunExt},
		},
	}
}

// TestInMemoryRegistry_Conformance runs the sagaregtest conformance suite
// against InMemoryRegistry, verifying the full Resolver.Lookup contract.
// The conformance suite is reusable so future Resolver implementations (e.g.
// PR-07 contractgen-emitted SagaResolver_<Name>) can plug into it with a
// single call.
//
// This test lives in package saga_test (external test package) to avoid the
// import cycle: kernel/saga → sagaregtest → kernel/saga.
func TestInMemoryRegistry_Conformance(t *testing.T) {
	t.Parallel()
	defs := []*saga.Definition{
		makeTestDef("saga-conf-a"),
		makeTestDef("saga-conf-b"),
		makeTestDef("saga-conf-c"),
	}
	sagaregtest.RunConformance(t,
		func() saga.Resolver {
			reg, err := saga.NewInMemoryRegistry(defs...)
			if err != nil {
				t.Fatalf("NewInMemoryRegistry: %v", err)
			}
			return reg
		},
		func() saga.Resolver {
			reg, err := saga.NewInMemoryRegistry()
			if err != nil {
				t.Fatalf("NewInMemoryRegistry (empty): %v", err)
			}
			return reg
		},
		defs...,
	)
}
