package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNullableTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   time.Time
		wantNil bool
	}{
		{
			name:    "zero time maps to nil",
			input:   time.Time{},
			wantNil: true,
		},
		{
			name:    "non-zero time maps to itself",
			input:   time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
			wantNil: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nullableTime(tc.input)
			if tc.wantNil {
				require.Nil(t, got, "zero time.Time should map to nil")
			} else {
				require.Equal(t, tc.input, got, "non-zero time should map to itself")
			}
		})
	}
}
