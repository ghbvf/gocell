package crypto_test

import (
	"bytes"
	"testing"

	configcrypto "github.com/ghbvf/gocell/cells/configcore/internal/crypto"
)

func TestAADForConfig_Format(t *testing.T) {
	tests := []struct {
		name      string
		cellID    string
		configKey string
		want      string
	}{
		{
			name:      "basic",
			cellID:    "configcore",
			configKey: "api_key",
			want:      "cell:configcore/key:api_key",
		},
		{
			name:      "empty strings",
			cellID:    "",
			configKey: "",
			want:      "cell:/key:",
		},
		{
			name:      "special chars",
			cellID:    "my-cell",
			configKey: "some/complex:key",
			want:      "cell:my-cell/key:some/complex:key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(configcrypto.AADForConfig(tc.cellID, tc.configKey))
			if got != tc.want {
				t.Fatalf("AADForConfig(%q, %q) = %q; want %q", tc.cellID, tc.configKey, got, tc.want)
			}
		})
	}
}

func TestAADForVersion_Format(t *testing.T) {
	tests := []struct {
		name     string
		cellID   string
		configID string
		want     string
	}{
		{
			name:     "basic uuid",
			cellID:   "configcore",
			configID: "550e8400-e29b-41d4-a716-446655440000",
			want:     "cell:configcore/version:550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:     "empty strings",
			cellID:   "",
			configID: "",
			want:     "cell:/version:",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(configcrypto.AADForVersion(tc.cellID, tc.configID))
			if got != tc.want {
				t.Fatalf("AADForVersion(%q, %q) = %q; want %q", tc.cellID, tc.configID, got, tc.want)
			}
		})
	}
}

// TestAADForConfig_vs_AADForVersion_NoCollision verifies that AADForConfig and
// AADForVersion produce distinct byte sequences even when their identity inputs
// look similar. This prevents cross-field AAD domain collisions.
func TestAADForConfig_vs_AADForVersion_NoCollision(t *testing.T) {
	cellID := "configcore"
	configID := "abc123"

	configAAD := configcrypto.AADForConfig(cellID, configID)
	versionAAD := configcrypto.AADForVersion(cellID, configID)

	if bytes.Equal(configAAD, versionAAD) {
		t.Fatalf("AADForConfig and AADForVersion must not be equal for the same inputs: both returned %q", configAAD)
	}
}
