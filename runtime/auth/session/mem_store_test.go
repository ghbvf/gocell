package session_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// memFactory is the storetest.Factory for the in-memory Store implementation.
// Each call yields a fresh MemStore + FakeClock anchored at the deterministic
// epoch the suite uses for its case timestamps.
//
// This factory is intentionally duplicated with the one in
// runtime/auth/session/storetest/suite_test.go: per-package coverage attributes
// executed lines to the package whose tests ran them, so we need session's
// own tests to exercise mem_store.go (and storetest's own tests to exercise
// suite.go). Without the duplication the Sonar per-package coverage gate
// would see one of the packages at 0%.
func memFactory(t *testing.T) (session.Store, *clockmock.FakeClock, func()) {
	t.Helper()
	fc := clockmock.New(storetest.EpochAnchor())
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), fc)
	if err != nil {
		t.Fatalf("memFactory: NewMemStore failed: %v", err)
	}
	return store, fc, func() {}
}

// TestMemStore_ConformsToStoretest exercises the full Protocol-driven contract
// suite against MemStore — paired with TestSuite_AgainstMemStore in storetest's
// own _test.go to keep both packages self-covered under per-package Sonar.
func TestMemStore_ConformsToStoretest(t *testing.T) {
	t.Parallel()
	storetest.Run(t, memFactory, storetest.NewTestProtocol(t))
}

// TestNewMemStore_NilProtocol_Rejected — body fail-fast on bare-nil *Protocol.
// Covered as Hard via typed (*MemStore, error) signature; the unit test
// pins the body-level message so refactors cannot silently relax the rule.
func TestNewMemStore_NilProtocol_Rejected(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(storetest.EpochAnchor())
	store, err := session.NewMemStore(nil, fc)
	if err == nil {
		t.Fatal("expected error for nil Protocol, got nil")
	}
	if store != nil {
		t.Fatalf("expected nil store on error, got %+v", store)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %s", coded.Code)
	}
	if !strings.Contains(coded.Message, "Protocol") {
		t.Errorf("expected message to mention Protocol, got %q", coded.Message)
	}
}

// TestNewMemStore_TypedNilProtocol_Rejected — pins the (*Protocol)(nil)
// branch alongside bare-nil. *Protocol is a concrete pointer type, so this
// case is functionally identical to bare-nil; the test guards against a
// future refactor that swaps the bare-nil check for an interface check on
// a typed-nil-incompatible helper, which would silently regress this path.
func TestNewMemStore_TypedNilProtocol_Rejected(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(storetest.EpochAnchor())
	var p *session.Protocol // typed-nil pointer
	store, err := session.NewMemStore(p, fc)
	if err == nil {
		t.Fatal("expected error for typed-nil *Protocol, got nil")
	}
	if store != nil {
		t.Fatalf("expected nil store on error, got %+v", store)
	}
	if !strings.Contains(err.Error(), "Protocol") {
		t.Errorf("expected error to mention Protocol, got %q", err.Error())
	}
}

// TestNewMemStore_NilClock_Rejected — body fail-fast on nil Clock.
func TestNewMemStore_NilClock_Rejected(t *testing.T) {
	t.Parallel()
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), nil)
	if err == nil {
		t.Fatal("expected error for nil Clock, got nil")
	}
	if store != nil {
		t.Fatalf("expected nil store on error, got %+v", store)
	}
	if !strings.Contains(err.Error(), "Clock") {
		t.Errorf("expected error to mention Clock, got %q", err.Error())
	}
}

// TestNewMemStore_TypedNilClock_Rejected — typed-nil clock.Clock interface
// must be rejected (defense-in-depth atop bare-nil; mirrors PR-MODE-1 typed-
// nil reject pattern locked by ERROR-FIRST-TYPED-NIL-01 archtest).
func TestNewMemStore_TypedNilClock_Rejected(t *testing.T) {
	t.Parallel()
	var typedNilClock clock.Clock // typed nil
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), typedNilClock)
	if err == nil {
		t.Fatal("expected error for typed-nil Clock, got nil")
	}
	if store != nil {
		t.Fatalf("expected nil store on error, got %+v", store)
	}
	if !strings.Contains(err.Error(), "Clock") {
		t.Errorf("expected error to mention Clock, got %q", err.Error())
	}
}

// TestNewMemStore_OK — happy path: valid protocol + clock returns *MemStore.
func TestNewMemStore_OK(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(storetest.EpochAnchor())
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), fc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

// TestMemStore_RepoReadinessConformance wires MemStore through the shared
// RepoHealthProber conformance harness. broken=nil because MemStore has no
// differentiated failure domain (in-memory always ready).
func TestMemStore_RepoReadinessConformance(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(storetest.EpochAnchor())
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), fc)
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	celltest.RunRepoReadinessConformance(t, "session-mem", store, nil)
}
