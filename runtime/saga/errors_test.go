package saga

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

func TestErrDefinitionNotRegistered(t *testing.T) {
	id := idutil.SafeID("def-123")
	err := errDefinitionNotRegistered(id)

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}

	if ec.Kind != errcode.KindInvalid {
		t.Errorf("Kind = %v, want KindInvalid", ec.Kind)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("Code = %v, want ErrValidationFailed", ec.Code)
	}

	// Verify definitionId detail is present.
	found := false
	for _, attr := range ec.Details {
		if attr.Key == "definitionId" && attr.Value.Kind() == slog.KindString {
			found = true
			if attr.Value.String() != string(id) {
				t.Errorf("definitionId detail = %q, want %q", attr.Value.String(), string(id))
			}
		}
	}
	if !found {
		t.Error("expected Details to contain definitionId attr")
	}
}

func TestErrFoldEventMismatch(t *testing.T) {
	id := idutil.SafeID("inst-456")
	reason := "unexpected-status"
	err := errFoldEventMismatch(id, reason)

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}

	if ec.Kind != errcode.KindInternal {
		t.Errorf("Kind = %v, want KindInternal", ec.Kind)
	}
	if ec.Code != errcode.ErrInternal {
		t.Errorf("Code = %v, want ErrInternal", ec.Code)
	}

	// Verify instanceId is present in Details.
	// reason is stored in WithInternal (server-side only, not in Details) per
	// the KindInternal three-layer rule (error-handling.md).
	var gotInstanceID bool
	for _, attr := range ec.Details {
		if attr.Key == "instanceId" && attr.Value.Kind() == slog.KindString {
			gotInstanceID = true
			if attr.Value.String() != string(id) {
				t.Errorf("instanceId detail = %q, want %q", attr.Value.String(), string(id))
			}
		}
		if attr.Key == "reason" {
			t.Errorf("reason must not be in Details (must be in WithInternal); got attr %v", attr)
		}
	}
	if !gotInstanceID {
		t.Error("expected Details to contain instanceId attr")
	}
}
