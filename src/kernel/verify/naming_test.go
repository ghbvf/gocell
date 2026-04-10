package verify

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKebabToCamelCase(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"session-revoke", "SessionRevoke"},
		{"startup", "Startup"},
		{"oidc-redirect", "OidcRedirect"},
		{"", ""},
		{"a", "A"},
		{"multi-word-string", "MultiWordString"},
		{"already-Capitalized", "AlreadyCapitalized"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, kebabToCamelCase(tt.in))
		})
	}
}
