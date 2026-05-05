package crypto_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func assertErrorContains(t *testing.T, context string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error containing %q, got nil", context, want)
	}
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) {
		full := ecErr.Message + " " + ecErr.InternalMessage
		if !strings.Contains(full, want) {
			t.Fatalf("%s: error message+internal %q does not contain %q", context, full, want)
		}
		return
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: error %q does not contain %q", context, err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// ParseKeyID tests
// ---------------------------------------------------------------------------

func TestParseKeyID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		keyID           string
		wantProvider    string
		wantVersion     int
		wantErrContains string
	}{
		{
			name:         "local-aes dash separator v1",
			keyID:        "local-aes-v1",
			wantProvider: "local-aes",
			wantVersion:  1,
		},
		{
			name:         "local-aes dash separator v0",
			keyID:        "local-aes-v0",
			wantProvider: "local-aes",
			wantVersion:  0,
		},
		{
			name:         "vault-transit colon separator v3",
			keyID:        "vault-transit:v3",
			wantProvider: "vault-transit",
			wantVersion:  3,
		},
		{
			name:         "vault-transit colon separator v1",
			keyID:        "vault-transit:v1",
			wantProvider: "vault-transit",
			wantVersion:  1,
		},
		{
			name:            "empty string",
			keyID:           "",
			wantErrContains: "invalid keyID",
		},
		{
			name:            "no version suffix",
			keyID:           "no-version",
			wantErrContains: "invalid keyID",
		},
		{
			name:            "non-numeric version",
			keyID:           "bad:vXYZ",
			wantErrContains: "invalid keyID",
		},
		{
			name:            "negative version dash separator",
			keyID:           "local-aes-v-1",
			wantErrContains: "negative version",
		},
		{
			name:            "negative version colon separator",
			keyID:           "vault-transit:v-5",
			wantErrContains: "negative version",
		},
		{
			name:         "colon separator takes precedence over dash for unambiguous input",
			keyID:        "provider-x:v2",
			wantProvider: "provider-x",
			wantVersion:  2, // colon separator tried first; dash in provider name is fine
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider, version, err := kcrypto.ParseKeyID(tc.keyID)

			if tc.wantErrContains != "" {
				assertErrorContains(t, fmt.Sprintf("ParseKeyID(%q)", tc.keyID), err, tc.wantErrContains)
				return
			}

			if err != nil {
				t.Fatalf("ParseKeyID(%q): unexpected error: %v", tc.keyID, err)
			}
			if provider != tc.wantProvider {
				t.Errorf("ParseKeyID(%q): provider = %q, want %q", tc.keyID, provider, tc.wantProvider)
			}
			if version != tc.wantVersion {
				t.Errorf("ParseKeyID(%q): version = %d, want %d", tc.keyID, version, tc.wantVersion)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MatchKeyID tests
// ---------------------------------------------------------------------------

func TestMatchKeyID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		handleID        string
		edkKeyID        string
		wantErrContains string
	}{
		{
			name:     "same provider and version - local-aes v1",
			handleID: "local-aes-v1",
			edkKeyID: "local-aes-v1",
		},
		{
			name:     "same provider and version - vault-transit v3",
			handleID: "vault-transit:v3",
			edkKeyID: "vault-transit:v3",
		},
		{
			name:            "different version - vault-transit",
			handleID:        "vault-transit:v2",
			edkKeyID:        "vault-transit:v3",
			wantErrContains: "vault-transit:v2",
		},
		{
			name:            "different version includes both IDs in message",
			handleID:        "local-aes-v1",
			edkKeyID:        "local-aes-v2",
			wantErrContains: "local-aes-v2",
		},
		{
			name:            "different provider",
			handleID:        "local-aes-v1",
			edkKeyID:        "vault-transit:v1",
			wantErrContains: "local-aes",
		},
		{
			name:            "malformed handleID",
			handleID:        "bad-handle",
			edkKeyID:        "vault-transit:v1",
			wantErrContains: "invalid keyID",
		},
		{
			name:            "malformed edkKeyID",
			handleID:        "vault-transit:v1",
			edkKeyID:        "bad-edk",
			wantErrContains: "invalid keyID",
		},
		{
			name:            "negative version in handleID",
			handleID:        "local-aes-v-1",
			edkKeyID:        "local-aes-v1",
			wantErrContains: "negative version",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := kcrypto.MatchKeyID(tc.handleID, tc.edkKeyID)

			if tc.wantErrContains != "" {
				assertErrorContains(t,
					fmt.Sprintf("MatchKeyID(%q, %q)", tc.handleID, tc.edkKeyID),
					err,
					tc.wantErrContains)
				return
			}

			if err != nil {
				t.Fatalf("MatchKeyID(%q, %q): unexpected error: %v", tc.handleID, tc.edkKeyID, err)
			}
		})
	}
}
