package crypto_test

import (
	"testing"

	configcrypto "github.com/ghbvf/gocell/cells/config-core/internal/crypto"
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
			cellID:    "config-core",
			configKey: "api_key",
			want:      "cell:config-core/key:api_key",
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
