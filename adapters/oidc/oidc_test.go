package oidc

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	require.NoError(t, Config{IssuerURL: "https://issuer", ClientID: "id"}.Validate())

	for _, tc := range []struct {
		name   string
		config Config
	}{
		{"missing issuer", Config{ClientID: "id"}},
		{"missing client ID", Config{IssuerURL: "https://issuer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, ErrAdapterOIDCConfig, ec.Code)
		})
	}
}

func TestNew_ValidConfig(t *testing.T) {
	a, err := New(Config{IssuerURL: "https://issuer", ClientID: "id"})
	require.NoError(t, err)
	require.NotNil(t, a)
}

func TestNew_InvalidConfig(t *testing.T) {
	_, err := New(Config{})
	require.Error(t, err)
}
