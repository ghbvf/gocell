package session_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// memFactory is the storetest.Factory for the in-memory Store implementation.
// Each call yields a fresh MemStore + FakeClock anchored at the deterministic
// epoch the suite uses for its case timestamps.
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
// suite against MemStore. Wave 1 RED — bodies are skipped; Wave 2 GREEN turns
// every t.Skip into a real assertion.
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
