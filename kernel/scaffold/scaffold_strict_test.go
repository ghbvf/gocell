package scaffold

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestCreateSlice_KebabNameRejected verifies that scaffold rejects slice IDs
// containing a hyphen (kebab-case). Slice directories must use no-dash naming
// to satisfy the governance FMT-16 strict rule.
func TestCreateSlice_KebabNameRejected(t *testing.T) {
	tests := []struct {
		name    string
		sliceID string
	}{
		{"single hyphen", "config-publish"},
		{"multiple hyphens", "session-login-v2"},
		{"trailing hyphen", "myslice-"},
		{"leading hyphen", "-myslice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			s := New(root)

			// Create a cell first so the missing-cell check doesn't interfere.
			if err := s.CreateCell(CellOpts{ID: "access-core", OwnerTeam: "platform"}); err != nil {
				t.Fatalf("CreateCell: %v", err)
			}

			err := s.CreateSlice(SliceOpts{ID: tt.sliceID, CellID: "access-core"})
			if err == nil {
				t.Fatalf("CreateSlice(%q) should have returned an error", tt.sliceID)
			}
			var ecErr *errcode.Error
			if !errors.As(err, &ecErr) || ecErr.Code != ErrScaffoldInvalidOpts {
				t.Errorf("CreateSlice(%q) error = %v, want code %v", tt.sliceID, err, ErrScaffoldInvalidOpts)
			}
		})
	}
}

// TestCreateSlice_NoDashNameAccepted verifies that no-dash names still pass.
func TestCreateSlice_NoDashNameAccepted(t *testing.T) {
	tests := []struct {
		name    string
		sliceID string
	}{
		{"single word", "login"},
		{"camelish", "sessionLogin"},
		{"nodash joined", "configpublish"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			s := New(root)

			if err := s.CreateCell(CellOpts{ID: "access-core", OwnerTeam: "platform"}); err != nil {
				t.Fatalf("CreateCell: %v", err)
			}

			err := s.CreateSlice(SliceOpts{ID: tt.sliceID, CellID: "access-core"})
			if err != nil {
				t.Errorf("CreateSlice(%q) should succeed, got: %v", tt.sliceID, err)
			}
		})
	}
}
