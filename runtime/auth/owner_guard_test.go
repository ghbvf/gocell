package auth_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

type fakeResource struct {
	OwnerID string
}

func TestCheckOwner_MatchReturnsNil(t *testing.T) {
	res := &fakeResource{OwnerID: "user-123"}
	err := auth.CheckOwner(res, func(r *fakeResource) string { return r.OwnerID },
		"user-123", errcode.ErrSessionNotFound, "session not found")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckOwner_MismatchReturnsKindNotFound(t *testing.T) {
	res := &fakeResource{OwnerID: "user-victim"}
	err := auth.CheckOwner(res, func(r *fakeResource) string { return r.OwnerID },
		"user-attacker", errcode.ErrSessionNotFound, "session not found")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindNotFound {
		t.Errorf("Kind: got %v, want KindNotFound (must never be PermissionDenied to avoid existence leak)", ec.Kind)
	}
	if ec.Code != errcode.ErrSessionNotFound {
		t.Errorf("Code: got %v, want ErrSessionNotFound", ec.Code)
	}
	if ec.Message != "session not found" {
		t.Errorf("Message: got %q, want %q", ec.Message, "session not found")
	}
}

func TestCheckOwner_NilPointerWithNilSafeAccessor(t *testing.T) {
	var res *fakeResource // nil
	accessor := func(r *fakeResource) string {
		if r == nil {
			return ""
		}
		return r.OwnerID
	}
	err := auth.CheckOwner(res, accessor, "user-123", errcode.ErrSessionNotFound, "session not found")
	if err == nil {
		t.Fatal("expected KindNotFound for nil resource vs non-empty caller, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindNotFound {
		t.Errorf("expected KindNotFound for nil sess + non-empty caller (IDOR collapse), got %v", err)
	}
}

func TestCheckOwner_BothEmptyStringsReturnsNil(t *testing.T) {
	// Edge case: if owner and caller are both "", they match → nil. Callers
	// must pre-validate caller is non-empty (sessionlogout does this at the
	// handler entry per service.go:99-104, raising KindInvalid for empty
	// callerUserID). The funnel relies on that pre-condition for IDOR safety.
	err := auth.CheckOwner((*fakeResource)(nil),
		func(r *fakeResource) string { return "" },
		"", errcode.ErrSessionNotFound, "session not found")
	if err != nil {
		t.Fatalf("expected nil (both empty), got %v", err)
	}
}

func TestCheckOwner_ValueTypeT(t *testing.T) {
	type valueResource struct{ Owner string }
	res := valueResource{Owner: "u1"}
	if err := auth.CheckOwner(res, func(r valueResource) string { return r.Owner },
		"u1", errcode.ErrSessionNotFound, "x"); err != nil {
		t.Fatalf("value-T match: expected nil, got %v", err)
	}
	if err := auth.CheckOwner(res, func(r valueResource) string { return r.Owner },
		"u2", errcode.ErrSessionNotFound, "x"); err == nil {
		t.Fatal("value-T mismatch: expected error, got nil")
	}
}

func TestCheckOwner_StringTAccessor(t *testing.T) {
	// Demonstrate T can be a primitive when owner ID *is* the resource.
	if err := auth.CheckOwner("owner-x", func(s string) string { return s },
		"owner-x", errcode.ErrSessionNotFound, "x"); err != nil {
		t.Fatalf("string-T match: expected nil, got %v", err)
	}
	if err := auth.CheckOwner("owner-x", func(s string) string { return s },
		"owner-y", errcode.ErrSessionNotFound, "x"); err == nil {
		t.Fatal("string-T mismatch: expected error, got nil")
	}
}
