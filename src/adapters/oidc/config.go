package oidc

import (
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Config holds the OIDC provider configuration.
type Config struct {
	// IssuerURL is the OIDC issuer URL (e.g., https://accounts.google.com).
	IssuerURL string
	// ClientID is the OAuth2 client identifier.
	ClientID string
	// ClientSecret is the OAuth2 client secret.
	ClientSecret string
	// RedirectURL is the callback URL for authorization code exchange.
	RedirectURL string
	// Scopes are the requested OIDC scopes. Defaults to ["openid", "profile", "email"].
	Scopes []string
	// JWKSCacheTTL is how long to cache the JWKS keys. Default: 1 hour.
	JWKSCacheTTL time.Duration
	// DiscoveryCacheTTL is how long to cache the discovery document. Default: 24 hours.
	DiscoveryCacheTTL time.Duration
	// HTTPTimeout is the timeout for HTTP requests to the OIDC provider. Default: 10 seconds.
	HTTPTimeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults applied over the
// provided issuer, client ID, and secret.
func DefaultConfig(issuerURL, clientID, clientSecret string) Config {
	return Config{
		IssuerURL:         issuerURL,
		ClientID:          clientID,
		ClientSecret:      clientSecret,
		Scopes:            []string{"openid", "profile", "email"},
		JWKSCacheTTL:      1 * time.Hour,
		DiscoveryCacheTTL: 24 * time.Hour,
		HTTPTimeout:       10 * time.Second,
	}
}

// Validate checks that required Config fields are populated.
func (c Config) Validate() error {
	if c.IssuerURL == "" {
		return errcode.New(ErrAdapterOIDCConfig, "oidc: issuer URL is required")
	}
	if c.ClientID == "" {
		return errcode.New(ErrAdapterOIDCConfig, "oidc: client ID is required")
	}
	return nil
}
