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
		session.WithRevokeOn(session.CredentialEventPasswordReset),
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
		session.WithRevokeOn(session.CredentialEventPasswordReset),
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
// preserves first occurrence.
func TestNewProtocol_WithRevokeOn_Dedup(t *testing.T) {
	t.Parallel()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(
			session.CredentialEventPasswordReset,
			session.CredentialEventLock,
			session.CredentialEventPasswordReset, // duplicate
			session.CredentialEventDelete,
			session.CredentialEventLock, // duplicate
		),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := p.RevokeOn()
	want := []session.CredentialEvent{
		session.CredentialEventPasswordReset,
		session.CredentialEventLock,
		session.CredentialEventDelete,
	}
	if len(got) != len(want) {
		t.Fatalf("dedup length mismatch: got %d, want %d (events=%v)", len(got), len(want), got)
	}
	for i, ev := range want {
		if got[i] != ev {
			t.Errorf("dedup order mismatch at %d: got %v, want %v", i, got[i], ev)
		}
	}
}

// TestNewProtocol_WithRevokeOn_Accumulates: multiple WithRevokeOn calls
// accumulate (builder semantics — runtime-api.md §Option 范式分层 builder type).
func TestNewProtocol_WithRevokeOn_Accumulates(t *testing.T) {
	t.Parallel()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(session.CredentialEventPasswordReset),
		session.WithRevokeOn(session.CredentialEventLock),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.RevokeOn(); len(got) != 2 {
		t.Errorf("accumulate: got %d events, want 2 (events=%v)", len(got), got)
	}
}

// TestMustNewProtocol_OK: composition-root convenience wrapper succeeds with
// valid options.
func TestMustNewProtocol_OK(t *testing.T) {
	t.Parallel()
	p := session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(session.CredentialEventPasswordReset),
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
		session.WithRevokeOn(session.CredentialEventPasswordReset),
	)
}

// TestProtocol_AccessorsImmutable: accessors return defensive copies of
// internal slice fields so callers cannot mutate protocol state post-construction.
func TestProtocol_AccessorsImmutable(t *testing.T) {
	t.Parallel()
	p := session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOn(
			session.CredentialEventPasswordReset,
			session.CredentialEventLock,
		),
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
