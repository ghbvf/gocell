package crypto

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ParseKeyID parses a key version identifier of the form "{provider}-v{N}" or
// "{provider}:v{N}" and returns the provider name and version number.
//
// Both dash-separated (e.g. "local-aes-v1") and colon-separated (e.g.
// "vault-transit:v3") formats are supported. The provider is everything before
// the last "-vN" or ":vN" segment.
//
// Returns ErrKeyProviderDecryptFailed on any parse error.
func ParseKeyID(keyID string) (provider string, version int, err error) {
	if keyID == "" {
		return "", 0, errcode.New(errcode.ErrKeyProviderDecryptFailed,
			"invalid keyID: empty string")
	}

	// Try colon separator first: "{provider}:v{N}"
	if idx := strings.LastIndex(keyID, ":v"); idx >= 0 {
		providerPart := keyID[:idx]
		versionStr := keyID[idx+2:] // skip ":v"
		if providerPart == "" {
			return "", 0, errcode.New(errcode.ErrKeyProviderDecryptFailed,
				fmt.Sprintf("invalid keyID %q: empty provider before ':v'", keyID))
		}
		v, parseErr := strconv.Atoi(versionStr)
		if parseErr != nil {
			return "", 0, errcode.New(errcode.ErrKeyProviderDecryptFailed,
				fmt.Sprintf("invalid keyID %q: non-numeric version %q", keyID, versionStr))
		}
		return providerPart, v, nil
	}

	// Try dash separator: "{provider}-v{N}"
	// The version segment is the last "-v{N}" suffix.
	if idx := strings.LastIndex(keyID, "-v"); idx >= 0 {
		providerPart := keyID[:idx]
		versionStr := keyID[idx+2:] // skip "-v"
		if providerPart == "" {
			return "", 0, errcode.New(errcode.ErrKeyProviderDecryptFailed,
				fmt.Sprintf("invalid keyID %q: empty provider before '-v'", keyID))
		}
		v, parseErr := strconv.Atoi(versionStr)
		if parseErr != nil {
			return "", 0, errcode.New(errcode.ErrKeyProviderDecryptFailed,
				fmt.Sprintf("invalid keyID %q: non-numeric version %q", keyID, versionStr))
		}
		return providerPart, v, nil
	}

	return "", 0, errcode.New(errcode.ErrKeyProviderDecryptFailed,
		fmt.Sprintf("invalid keyID %q: must end with '-v{N}' or ':v{N}'", keyID))
}

// MatchKeyID verifies that handleID and edkKeyID refer to the same provider
// and key version. Returns nil when they match, or a descriptive
// ErrKeyProviderDecryptFailed error when they do not.
//
// Callers SHOULD call MatchKeyID before decryption to prevent silent data
// corruption from misrouted key versions.
func MatchKeyID(handleID, edkKeyID string) error {
	hp, hv, err := ParseKeyID(handleID)
	if err != nil {
		return err
	}
	ep, ev, err := ParseKeyID(edkKeyID)
	if err != nil {
		return err
	}

	if hp != ep || hv != ev {
		return errcode.New(errcode.ErrKeyProviderDecryptFailed,
			fmt.Sprintf("keyID mismatch: handle %q does not match edk %q", handleID, edkKeyID))
	}
	return nil
}
