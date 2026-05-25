package sagaregtest

import (
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// RunConformance runs the Resolver conformance suite against the supplied
// factory. factory must return a fresh, independent Resolver pre-loaded with
// the given defs on each call. emptyFactory must return a Resolver with no
// definitions (used for the nil-defs lookup test).
// Every subtest is registered as a t.Run so individual cases can be filtered
// with -run.
//
// Usage:
//
//	func TestMyRegistry_Conformance(t *testing.T) {
//	    defs := []*saga.Definition{...}
//	    sagaregtest.RunConformance(t,
//	        func() saga.Resolver { reg, _ := saga.NewInMemoryRegistry(defs...); return reg },
//	        func() saga.Resolver { reg, _ := saga.NewInMemoryRegistry(); return reg },
//	        defs...,
//	    )
//	}
func RunConformance(t *testing.T, factory func() saga.Resolver, emptyFactory func() saga.Resolver, defs ...*saga.Definition) {
	t.Helper()

	cases := []struct {
		name string
		run  func(*testing.T, func() saga.Resolver, func() saga.Resolver, []*saga.Definition)
	}{
		{"Lookup_RegisteredID_ReturnsDefinition", conformLookupRegisteredID},
		{"Lookup_UnregisteredID_ReturnsFalse", conformLookupUnregisteredID},
		{"Lookup_EmptyID_ReturnsFalse", conformLookupEmptyID},
		{"Lookup_Concurrent_NoDataRace", conformLookupConcurrent},
		{"Lookup_NilDefsRegistry_AllLookupsFalse", conformLookupNilDefs},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, factory, emptyFactory, defs) })
	}
}

// conformLookupRegisteredID asserts that Lookup returns the exact same pointer
// and ok=true for every definition that was registered at construction time.
func conformLookupRegisteredID(t *testing.T, factory func() saga.Resolver, _ func() saga.Resolver, defs []*saga.Definition) {
	t.Helper()
	if len(defs) == 0 {
		t.Skip("no definitions provided — skipping registered-ID lookup check")
		return
	}
	reg := factory()
	for _, def := range defs {
		got, ok := reg.Lookup(def.ID)
		if !ok {
			t.Errorf("Lookup(%q): expected ok=true, got false", def.ID)
			continue
		}
		if got != def {
			t.Errorf("Lookup(%q): returned different pointer; expected pointer equality", def.ID)
		}
	}
}

// conformLookupUnregisteredID asserts that Lookup returns (nil, false) for an
// ID that was never registered.
func conformLookupUnregisteredID(t *testing.T, factory func() saga.Resolver, _ func() saga.Resolver, _ []*saga.Definition) {
	t.Helper()
	reg := factory()
	got, ok := reg.Lookup(idutil.SafeID("saga-definitely-not-registered-xyz-12345"))
	if ok {
		t.Error("Lookup of unregistered ID: expected ok=false, got true")
	}
	if got != nil {
		t.Errorf("Lookup of unregistered ID: expected nil definition, got %v", got)
	}
}

// conformLookupEmptyID asserts that Lookup with an empty SafeID returns
// (nil, false) without panicking.
func conformLookupEmptyID(t *testing.T, factory func() saga.Resolver, _ func() saga.Resolver, _ []*saga.Definition) {
	t.Helper()
	reg := factory()
	got, ok := reg.Lookup(idutil.SafeID(""))
	if ok {
		t.Error("Lookup of empty ID: expected ok=false, got true")
	}
	if got != nil {
		t.Errorf("Lookup of empty ID: expected nil definition, got %v", got)
	}
}

// conformLookupConcurrent asserts that concurrent Lookup calls do not race.
// Run with go test -race to observe data-race detection.
func conformLookupConcurrent(t *testing.T, factory func() saga.Resolver, _ func() saga.Resolver, defs []*saga.Definition) {
	t.Helper()
	reg := factory()

	const goroutines = 500
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			if len(defs) == 0 {
				// No defs: just exercise the miss path.
				reg.Lookup(idutil.SafeID("saga-miss"))
				return
			}
			def := defs[n%len(defs)]
			got, ok := reg.Lookup(def.ID)
			if !ok || got == nil {
				t.Errorf("concurrent Lookup(%q): expected hit, got ok=%v def=%v", def.ID, ok, got)
			}
		}(i)
	}
	wg.Wait()
}

// conformLookupNilDefs asserts that a Resolver constructed with no definitions
// returns (nil, false) for any Lookup call.
func conformLookupNilDefs(t *testing.T, _ func() saga.Resolver, emptyFactory func() saga.Resolver, _ []*saga.Definition) {
	t.Helper()
	reg := emptyFactory()
	got, ok := reg.Lookup(idutil.SafeID("saga-any"))
	if ok {
		t.Error("empty Resolver Lookup: expected ok=false, got true")
	}
	if got != nil {
		t.Errorf("empty Resolver Lookup: expected nil, got %v", got)
	}
}
