package oidc

import (
	"context"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// IDTokenClaims represents the validated claims from an ID token.
type IDTokenClaims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  string `json:"aud"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	Nonce     string `json:"nonce,omitempty"`
	Email     string `json:"email,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Verifier verifies OIDC ID tokens using the go-oidc library.
type Verifier struct {
	provider *Provider
}

// NewVerifier creates a Verifier backed by the given Provider.
func NewVerifier(provider *Provider) *Verifier {
	return &Verifier{
		provider: provider,
	}
}

// Verify parses and validates an ID token string. It checks the signature
// using JWKS, validates the issuer and audience claims.
func (v *Verifier) Verify(ctx context.Context, rawIDToken string) (*IDTokenClaims, error) {
	oidcProvider, err := v.provider.ensureProvider(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCVerify,
			"oidc: failed to initialize provider for verification", err)
	}

	verifierCfg := &gooidc.Config{
		ClientID: v.provider.config.ClientID,
	}

	oidcVerifier := oidcProvider.VerifierContext(v.provider.oidcContext(ctx), verifierCfg)

	idToken, err := oidcVerifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCVerify,
			"oidc: token verification failed", err)
	}

	// Extract additional claims (email, name, nonce) from the token payload.
	var extra struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Nonce string `json:"nonce"`
	}
	if claimErr := idToken.Claims(&extra); claimErr != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCVerify,
			"oidc: failed to extract claims from verified token", claimErr)
	}

	result := &IDTokenClaims{
		Issuer:    idToken.Issuer,
		Subject:   idToken.Subject,
		ExpiresAt: idToken.Expiry.Unix(),
		IssuedAt:  idToken.IssuedAt.Unix(),
		Nonce:     idToken.Nonce,
		Email:     extra.Email,
		Name:      extra.Name,
	}

	// Set audience from the first audience entry.
	if len(idToken.Audience) > 0 {
		result.Audience = idToken.Audience[0]
	}

	return result, nil
}
