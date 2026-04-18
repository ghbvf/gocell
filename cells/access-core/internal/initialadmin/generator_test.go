package initialadmin_test

import (
	"io"
	"testing"
	"testing/iotest"

	"github.com/ghbvf/gocell/cells/access-core/internal/initialadmin"
)

func TestGeneratePassword_LengthAndCharset(t *testing.T) {
	t.Parallel()

	password, err := initialadmin.GeneratePassword(nil) // nil uses crypto/rand.Reader internally
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 32 bytes base64url no-pad → 43 chars (ceil(32*8/6))
	if len(password) < 43 {
		t.Errorf("password too short: got %d chars, want ≥ 43", len(password))
	}

	// Must only contain base64url chars: [A-Za-z0-9_-], no padding
	for i, c := range password {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			t.Errorf("invalid char %q at position %d in password", c, i)
		}
	}

	// No standard base64 padding or non-URL chars
	for _, forbidden := range []byte{'=', '+', '/'} {
		for _, c := range []byte(password) {
			if c == forbidden {
				t.Errorf("password contains forbidden char %q", forbidden)
			}
		}
	}
}

func TestGeneratePassword_Uniqueness(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 1000)
	for i := range 1000 {
		pw, err := initialadmin.GeneratePassword(nil)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if _, dup := seen[pw]; dup {
			t.Fatalf("duplicate password at iteration %d: %q", i, pw)
		}
		seen[pw] = struct{}{}
	}
}

func TestGeneratePassword_RandReaderError(t *testing.T) {
	t.Parallel()

	errReader := iotest.ErrReader(io.ErrUnexpectedEOF)
	_, err := initialadmin.GeneratePassword(errReader)
	if err == nil {
		t.Fatal("expected error from failing reader, got nil")
	}
}
