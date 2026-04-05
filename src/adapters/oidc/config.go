package oidc

import "github.com/ghbvf/gocell/pkg/errcode"

// Config holds the OIDC client configuration.
type Config struct {
	// IssuerURL is the OIDC provider's issuer URL (e.g., "https://accounts.google.com").
	// Used for Discovery and issuer claim validation.
	IssuerURL string
	// ClientID is the OAuth 2.0 client identifier.
	ClientID string
	// ClientSecret is the OAuth 2.0 client secret.
	ClientSecret string
	// RedirectURL is the callback URL registered with the OIDC provider.
	RedirectURL string
}

// Validate checks that all required fields are populated.
func (c *Config) Validate() error {
	if c.IssuerURL == "" {
		return errcode.New(ErrDiscovery, "oidc: issuer URL is required")
	}
	if c.ClientID == "" {
		return errcode.New(ErrDiscovery, "oidc: client ID is required")
	}
	if c.ClientSecret == "" {
		return errcode.New(ErrDiscovery, "oidc: client secret is required")
	}
	if c.RedirectURL == "" {
		return errcode.New(ErrDiscovery, "oidc: redirect URL is required")
	}
	return nil
}
