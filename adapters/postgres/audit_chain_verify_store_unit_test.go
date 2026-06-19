package postgres

// audit_chain_verify_store_unit_test.go — white-box unit tests for
// AuditChainVerifyStore that do NOT require a live database.
// Uses same-package access to build an AuditChainVerifyStore directly via
// struct literal (same package = no exported constructor required).

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// newTestProtocol builds a minimal Protocol for unit tests (real HMAC key).
func newTestProtocol(t *testing.T, ns string) *ledger.Protocol {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	nsid, err := ledger.ParseNamespaceID(ns)
	if err != nil {
		t.Fatalf("ParseNamespaceID(%q): %v", ns, err)
	}
	p, err := ledger.NewProtocol(nsid, key,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}))
	if err != nil {
		t.Fatalf("NewProtocol(%q): %v", ns, err)
	}
	return p
}

// TestVerifyChain_UnknownNamespace_Unit constructs an AuditChainVerifyStore in
// the same package (white-box) with a non-nil protocols map that is missing the
// requested namespace, and asserts that VerifyChain fails closed (non-nil error,
// valid=false) — no live DB required.
func TestVerifyChain_UnknownNamespace_Unit(t *testing.T) {
	t.Parallel()

	relayProto := newTestProtocol(t, "auditcore")
	// Build the store directly — same package access, no constructor preflight.
	s := &AuditChainVerifyStore{
		db:        nil, // VerifyChain fails before reaching the DB for unknown namespace
		protocols: map[string]*ledger.Protocol{"auditcore": relayProto},
	}

	// "bootstrap" is NOT in the protocols map — must fail closed.
	valid, _, err := s.VerifyChain(context.Background(), "bootstrap", "", 1, 1)
	if err == nil {
		t.Fatal("VerifyChain: expected error for unknown namespace, got nil")
	}
	if valid {
		t.Error("VerifyChain: unknown namespace must not report valid=true")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Errorf("expected *errcode.Error for unknown namespace, got %T: %v", err, err)
	}
}
