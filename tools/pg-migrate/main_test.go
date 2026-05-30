package main

import (
	"testing"
)

func TestParseRebuildPermits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantLen     int
		wantErr     bool
		wantNums    []int64
		wantReasons []string
	}{
		{
			name:    "empty string returns nil permits and no error",
			input:   "",
			wantLen: 0,
			wantErr: false,
		},
		{
			name:    "missing colon returns error",
			input:   "44reason",
			wantErr: true,
		},
		{
			name:    "non-numeric migration number returns error",
			input:   "abc:reason",
			wantErr: true,
		},
		{
			name:    "empty reason returns AllowForwardRebuild validation error",
			input:   "44:",
			wantErr: true,
		},
		{
			name:        "multiple permits parsed correctly",
			input:       "43:r1,44:r2",
			wantLen:     2,
			wantErr:     false,
			wantNums:    []int64{43, 44},
			wantReasons: []string{"r1", "r2"},
		},
		{
			name:        "whitespace trimmed from number and reason",
			input:       " 44 : reason ",
			wantLen:     1,
			wantErr:     false,
			wantNums:    []int64{44},
			wantReasons: []string{"reason"},
		},
		{
			name:        "reason containing internal spaces is preserved",
			input:       "44:drain verified",
			wantLen:     1,
			wantErr:     false,
			wantNums:    []int64{44},
			wantReasons: []string{"drain verified"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			permits, err := parseRebuildPermits(tc.input)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRebuildPermits(%q) = nil error, want error", tc.input)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseRebuildPermits(%q) unexpected error: %v", tc.input, err)
			}

			if len(permits) != tc.wantLen {
				t.Fatalf("parseRebuildPermits(%q) returned %d permits, want %d",
					tc.input, len(permits), tc.wantLen)
			}

			for i, p := range permits {
				if got := p.MigrationNumber(); got != tc.wantNums[i] {
					t.Errorf("permit[%d].MigrationNumber() = %d, want %d",
						i, got, tc.wantNums[i])
				}
				if got := p.Reason(); got != tc.wantReasons[i] {
					t.Errorf("permit[%d].Reason() = %q, want %q",
						i, got, tc.wantReasons[i])
				}
			}
		})
	}
}
