package session_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Compile-time verification that package-internal types implement the sealed
// marker interfaces. The marker methods (fingerprintModeOK / orderingModelOK)
// are unexported, so package-external types cannot satisfy these interfaces —
// any attempt to add an external implementer would fail to compile here.
var (
	_ session.FingerprintMode = session.FingerprintJTIRef{}
	_ session.OrderingModel   = session.OrderingAuthzEpoch{}
)

// TestNewProtocol_NoOptions_Error: NewProtocol with zero options must fail
// because Fingerprint and Ordering are required (D1 / D2).
func TestNewProtocol_NoOptions_Error(t *testing.T) {
	t.Parallel()
	p, err := session.NewProtocol()
	if err == nil {
		t.Fatalf("expected error for missing required options, got nil; protocol=%+v", p)
	}
	if p != nil {
		t.Fatalf("expected nil protocol on error, got %+v", p)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %s", coded.Code)
	}
	if !strings.Contains(coded.Message, "fingerprint") {
		t.Errorf("expected message to mention fingerprint, got %q", coded.Message)
	}
}

// TestNewProtocol_WithFingerprintJTIRef_OK: the recommended path produces a
// usable protocol when all required options are supplied.
func TestNewProtocol_WithFingerprintJTIRef_OK(t *testing.T) {
	t.Parallel()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(
			session.CredentialEventPasswordReset,
			session.CredentialEventLock,
			session.CredentialEventDelete,
			session.CredentialEventRoleRevoke,
		),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil protocol")
	}
	if _, ok := p.Fingerprint().(session.FingerprintJTIRef); !ok {
		t.Errorf("expected FingerprintJTIRef, got %T", p.Fingerprint())
	}
	if _, ok := p.Ordering().(session.OrderingAuthzEpoch); !ok {
		t.Errorf("expected OrderingAuthzEpoch, got %T", p.Ordering())
	}
	if got, want := len(p.RevokeOn()), 4; got != want {
		t.Errorf("RevokeOn length: got %d, want %d", got, want)
	}
}

// TestNewProtocol_WithFingerprintNil_Rejected: typed-nil interface must be
// rejected (defense-in-depth on top of the nil-interface case).
func TestNewProtocol_WithFingerprintNil_Rejected(t *testing.T) {
	t.Parallel()
	var fp session.FingerprintMode // typed nil
	_, err := session.NewProtocol(
		session.WithFingerprint(fp),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err == nil {
		t.Fatal("expected error for nil FingerprintMode")
	}
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("expected error to mention fingerprint, got %q", err.Error())
	}
}

// TestNewProtocol_WithOrderingNil_Rejected: typed-nil OrderingModel rejected
// (D2 — ordering required).
func TestNewProtocol_WithOrderingNil_Rejected(t *testing.T) {
	t.Parallel()
	var om session.OrderingModel // typed nil
	_, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(om),
		session.WithRevokeOnAll(),
	)
	if err == nil {
		t.Fatal("expected error for nil OrderingModel")
	}
	if !strings.Contains(err.Error(), "ordering") {
		t.Errorf("expected error to mention ordering, got %q", err.Error())
	}
}

// TestNewProtocol_WithRevokeOn_Empty_Error: RevokeOn must declare ≥1 event
// (D3 — fail-closed; empty means caller forgot).
func TestNewProtocol_WithRevokeOn_Empty_Error(t *testing.T) {
	t.Parallel()
	_, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(),
	)
	if err == nil {
		t.Fatal("expected error for empty RevokeOn")
	}
	if !strings.Contains(err.Error(), "event") {
		t.Errorf("expected error to mention event, got %q", err.Error())
	}
}

// TestNewProtocol_NoRevokeOn_Error: missing RevokeOn entirely also fails
// (≥1 required event is the minimum protocol shape).
func TestNewProtocol_NoRevokeOn_Error(t *testing.T) {
	t.Parallel()
	_, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
	)
	if err == nil {
		t.Fatal("expected error when WithRevokeOn omitted")
	}
}

// TestNewProtocol_WithRevokeOn_Dedup: duplicate events deduplicated; order
// preserves first occurrence. Declares all 4 events via WithRevokeOnAll, then
// adds each event again via a second WithRevokeOn call to verify cross-call dedup.
func TestNewProtocol_WithRevokeOn_Dedup(t *testing.T) {
	t.Parallel()
	// WithRevokeOnAll provides the required complete set; the second call re-adds
	// all 4 events as duplicates to verify the dedup-across-accumulated-calls path.
	extra := session.WithRevokeOn(
		session.CredentialEventPasswordReset,
		session.CredentialEventLock,
		session.CredentialEventDelete,
		session.CredentialEventRoleRevoke,
	)
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
		extra, // all 4 are duplicates — dedup must keep length at 4
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := p.RevokeOn()
	if len(got) != 4 {
		t.Fatalf("dedup length mismatch: got %d, want 4 (events=%v)", len(got), got)
	}
}

// TestNewProtocol_WithRevokeOn_Accumulates: multiple WithRevokeOn calls
// accumulate (builder semantics — runtime-api.md §Option 范式分层 builder type).
// Uses split WithRevokeOn calls covering all 4 events to satisfy the complete-set check.
func TestNewProtocol_WithRevokeOn_Accumulates(t *testing.T) {
	t.Parallel()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(session.CredentialEventPasswordReset, session.CredentialEventLock),
		session.WithRevokeOn(session.CredentialEventDelete, session.CredentialEventRoleRevoke),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.RevokeOn(); len(got) != 4 {
		t.Errorf("accumulate: got %d events, want 4 (events=%v)", len(got), got)
	}
}

// TestMustNewProtocol_OK: composition-root convenience wrapper succeeds with
// valid options.
func TestMustNewProtocol_OK(t *testing.T) {
	t.Parallel()
	p := session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if p == nil {
		t.Fatal("expected non-nil protocol from MustNewProtocol")
	}
}

// TestMustNewProtocol_Panic_OnError: composition-root wrapper panics on
// validation failure (typed-nil fingerprint).
func TestMustNewProtocol_Panic_OnError(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from MustNewProtocol when fingerprint missing")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("expected panic value to be error, got %T: %v", r, r)
		}
		if !strings.Contains(err.Error(), "fingerprint") {
			t.Errorf("expected panic error to mention fingerprint, got %q", err.Error())
		}
	}()
	_ = session.MustNewProtocol(
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
}

// TestProtocol_AccessorsImmutable: accessors return defensive copies of
// internal slice fields so callers cannot mutate protocol state post-construction.
func TestProtocol_AccessorsImmutable(t *testing.T) {
	t.Parallel()
	p := session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	got := p.RevokeOn()
	if len(got) == 0 {
		t.Fatal("expected non-empty RevokeOn")
	}
	got[0] = session.CredentialEventDelete // mutate caller copy
	again := p.RevokeOn()
	if again[0] == session.CredentialEventDelete {
		t.Error("RevokeOn() must return a defensive copy; caller mutation leaked")
	}
}

// TestNewProtocol_WithRevokeOn_UnknownEvent_Rejected: CredentialEvent(99) is
// not a declared constant and must be rejected at option evaluation time.
func TestNewProtocol_WithRevokeOn_UnknownEvent_Rejected(t *testing.T) {
	t.Parallel()
	_, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(session.CredentialEvent(99)),
	)
	if err == nil {
		t.Fatal("expected error for unknown CredentialEvent(99)")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected error to mention unknown, got %q", err.Error())
	}
}

// TestNewProtocol_WithRevokeOn_PartialSet_Rejected: declaring only a subset of
// the 4 required CredentialEvent values must be rejected by NewProtocol.
func TestNewProtocol_WithRevokeOn_PartialSet_Rejected(t *testing.T) {
	t.Parallel()
	_, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(session.CredentialEventPasswordReset),
	)
	if err == nil {
		t.Fatal("expected error for partial CredentialEvent set")
	}
	if !strings.Contains(err.Error(), "all 4") && !strings.Contains(err.Error(), "complete set") {
		t.Errorf("expected error to mention complete set requirement, got %q", err.Error())
	}
}

// TestNewProtocol_WithRevokeOnAll_OK: WithRevokeOnAll() declares all 4 events
// and must produce a valid protocol with RevokeOn returning 4 events.
func TestNewProtocol_WithRevokeOnAll_OK(t *testing.T) {
	t.Parallel()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		t.Fatalf("unexpected error with WithRevokeOnAll: %v", err)
	}
	got := p.RevokeOn()
	if len(got) != 4 {
		t.Errorf("WithRevokeOnAll: expected 4 events, got %d (events=%v)", len(got), got)
	}
}

// TestWithRevokeOn_MixedValid_Invalid: a call mixing valid and invalid
// CredentialEvent values must be rejected (the invalid value is detected before
// any accumulation occurs).
func TestWithRevokeOn_MixedValid_Invalid(t *testing.T) {
	t.Parallel()
	_, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(session.CredentialEventPasswordReset, session.CredentialEvent(99)),
	)
	if err == nil {
		t.Fatal("expected error when mixing valid and invalid CredentialEvent values")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected error to mention unknown, got %q", err.Error())
	}
}

// TestCredentialEvent_Stringer: typed enum has a stable String() representation
// for diagnostics (storetest will key cases on it in S2).
func TestCredentialEvent_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ev   session.CredentialEvent
		want string
	}{
		{session.CredentialEventPasswordReset, "PasswordReset"},
		{session.CredentialEventLock, "Lock"},
		{session.CredentialEventDelete, "Delete"},
		{session.CredentialEventRoleRevoke, "RoleRevoke"},
		{session.CredentialEvent(99), "Unknown"},
	}
	for _, c := range cases {
		if got := c.ev.String(); got != c.want {
			t.Errorf("CredentialEvent(%d).String() = %q, want %q", c.ev, got, c.want)
		}
	}
}
