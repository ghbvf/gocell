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
		"user-123", errcode.ErrSessionNotFound)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckOwner_MismatchReturnsKindNotFound(t *testing.T) {
	res := &fakeResource{OwnerID: "user-victim"}
	err := auth.CheckOwner(res, func(r *fakeResource) string { return r.OwnerID },
		"user-attacker", errcode.ErrSessionNotFound)
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
	if ec.Message != "not found" {
		t.Errorf("Message: got %q, want %q (funnel uses const literal, resource type carried by Code)",
			ec.Message, "not found")
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
	err := auth.CheckOwner(res, accessor, "user-123", errcode.ErrSessionNotFound)
	if err == nil {
		t.Fatal("expected KindNotFound for nil resource vs non-empty caller, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindNotFound {
		t.Errorf("expected KindNotFound for nil sess + non-empty caller (IDOR collapse), got %v", err)
	}
}

func TestCheckOwner_EmptyCallerIDFailsClosed(t *testing.T) {
	// Defense-in-depth: empty callerID must NOT grant access even if owner
	// accessor returns "". Upstream callers (e.g. sessionlogout.Service.Logout's
	// empty-callerUserID guard) should reject empty subjects with KindInvalid;
	// CheckOwner's fail-closed check is the backstop so an upstream auth bug
	// cannot silently permit access by lining up two empty strings.
	cases := []struct {
		name      string
		accessor  func(*fakeResource) string
		resource  *fakeResource
		wantOwner string
	}{
		{
			name:     "both empty",
			accessor: func(r *fakeResource) string { return "" },
			resource: nil,
		},
		{
			name:     "accessor empty resource non-nil",
			accessor: func(r *fakeResource) string { return "" },
			resource: &fakeResource{OwnerID: "u1"},
		},
		{
			name:     "accessor returns real ownerID",
			accessor: func(r *fakeResource) string { return r.OwnerID },
			resource: &fakeResource{OwnerID: "u1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := auth.CheckOwner(tc.resource, tc.accessor,
				"", errcode.ErrSessionNotFound)
			if err == nil {
				t.Fatal("expected fail-closed KindNotFound for empty callerID, got nil")
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) || ec.Kind != errcode.KindNotFound {
				t.Errorf("expected KindNotFound (fail-closed), got %v", err)
			}
		})
	}
}

func TestCheckOwner_ValueTypeT(t *testing.T) {
	type valueResource struct{ Owner string }
	res := valueResource{Owner: "u1"}
	if err := auth.CheckOwner(res, func(r valueResource) string { return r.Owner },
		"u1", errcode.ErrSessionNotFound); err != nil {
		t.Fatalf("value-T match: expected nil, got %v", err)
	}
	if err := auth.CheckOwner(res, func(r valueResource) string { return r.Owner },
		"u2", errcode.ErrSessionNotFound); err == nil {
		t.Fatal("value-T mismatch: expected error, got nil")
	}
}

func TestCheckOwner_StringTAccessor(t *testing.T) {
	// Demonstrate T can be a primitive when owner ID *is* the resource.
	if err := auth.CheckOwner("owner-x", func(s string) string { return s },
		"owner-x", errcode.ErrSessionNotFound); err != nil {
		t.Fatalf("string-T match: expected nil, got %v", err)
	}
	if err := auth.CheckOwner("owner-x", func(s string) string { return s },
		"owner-y", errcode.ErrSessionNotFound); err == nil {
		t.Fatal("string-T mismatch: expected error, got nil")
	}
}
