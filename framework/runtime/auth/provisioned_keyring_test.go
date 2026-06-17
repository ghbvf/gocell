package auth

// provisioned_keyring_test.go — table-driven negative-path tests for the
// env-parsing helpers in provisioned_keyring.go (#2153).
//
// parseVerifyKeys and decodeHexKeyPair are package-private helpers; this file is
// in package auth (white-box) so they are directly accessible.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVerifyKeys_NegativePaths(t *testing.T) {
	good32hex := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20" // 32 bytes

	tests := []struct {
		name    string
		input   string
		wantErr bool
		wantLen int // expected number of entries when no error
	}{
		{
			name:    "empty_string_yields_empty_map",
			input:   "",
			wantErr: false,
			wantLen: 0,
		},
		{
			name:    "whitespace_only_yields_empty_map",
			input:   "   ",
			wantErr: false,
			wantLen: 0,
		},
		{
			name:    "malformed_no_colon",
			input:   "accesscore",
			wantErr: true,
		},
		{
			name:    "malformed_empty_key_after_colon",
			input:   "accesscore:",
			wantErr: true,
		},
		{
			name:    "malformed_empty_caller_before_colon",
			input:   ":deadbeef",
			wantErr: true,
		},
		{
			name:    "bad_hex_value",
			input:   "accesscore:NOTHEX",
			wantErr: true,
		},
		{
			name:    "bad_hex_odd_length",
			input:   "accesscore:abc",
			wantErr: true,
		},
		{
			name:    "valid_single_entry",
			input:   "accesscore:" + good32hex,
			wantErr: false,
			wantLen: 1,
		},
		{
			name:    "valid_two_entries",
			input:   "accesscore:" + good32hex + ",auditcore:" + good32hex,
			wantErr: false,
			wantLen: 2,
		},
		{
			name:    "mixed_valid_and_invalid",
			input:   "accesscore:" + good32hex + ",badentrynocolon",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVerifyKeys(tc.input)
			if tc.wantErr {
				require.Error(t, err, "expected an error for input %q", tc.input)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tc.wantLen,
				"unexpected number of parsed entries for input %q", tc.input)
		})
	}
}

func TestDecodeHexKeyPair_NegativePaths(t *testing.T) {
	good32hex := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20" // 32 bytes

	tests := []struct {
		name    string
		cur     string
		prev    string
		wantErr bool
		wantLen int // expected slice length when no error
	}{
		{
			name:    "empty_current_key",
			cur:     "",
			prev:    "",
			wantErr: true,
		},
		{
			name:    "bad_hex_current",
			cur:     "NOTHEX",
			prev:    "",
			wantErr: true,
		},
		{
			name:    "bad_hex_previous",
			cur:     good32hex,
			prev:    "NOTHEX",
			wantErr: true,
		},
		{
			name:    "valid_current_only",
			cur:     good32hex,
			prev:    "",
			wantErr: false,
			wantLen: 1,
		},
		{
			name:    "valid_current_and_previous",
			cur:     good32hex,
			prev:    good32hex,
			wantErr: false,
			wantLen: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeHexKeyPair(tc.cur, tc.prev)
			if tc.wantErr {
				require.Error(t, err, "expected an error for cur=%q prev=%q", tc.cur, tc.prev)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tc.wantLen,
				"unexpected slice length for cur=%q prev=%q", tc.cur, tc.prev)
		})
	}
}
