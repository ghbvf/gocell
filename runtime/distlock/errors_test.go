package distlock_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/distlock"
)

// TestErrorCodes_StableValues guards against accidental rename of the four
// distlock error-code string constants. These values are consumed by
// client-side error taxonomies and must remain stable across releases.
func TestErrorCodes_StableValues(t *testing.T) {
	tests := []struct {
		name     string
		got      errcode.Code
		expected errcode.Code
	}{
		{
			name:     "ErrLockAcquire",
			got:      distlock.ErrLockAcquire,
			expected: "ERR_DISTLOCK_ACQUIRE",
		},
		{
			name:     "ErrLockRelease",
			got:      distlock.ErrLockRelease,
			expected: "ERR_DISTLOCK_RELEASE",
		},
		{
			name:     "ErrLockTimeout",
			got:      distlock.ErrLockTimeout,
			expected: "ERR_DISTLOCK_TIMEOUT",
		},
		{
			name:     "ErrLockLost",
			got:      distlock.ErrLockLost,
			expected: "ERR_DISTLOCK_LOST",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.expected {
				t.Errorf("errcode constant %s: got %q, want %q", tc.name, tc.got, tc.expected)
			}
		})
	}
}
