package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// JWK represents a single JSON Web Key.
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// JWKS represents a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

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

// Verifier verifies OIDC ID tokens using JWKS from the provider.
type Verifier struct {
	provider *Provider

	mu       sync.RWMutex
	jwks     *JWKS
	keyCache map[string]*rsa.PublicKey
	fetchAt  time.Time
}

// NewVerifier creates a Verifier backed by the given Provider.
func NewVerifier(provider *Provider) *Verifier {
	return &Verifier{
		provider: provider,
		keyCache: make(map[string]*rsa.PublicKey),
	}
}

// Verify parses and validates an ID token string. It checks the signature
// using JWKS, validates the issuer and audience claims.
func (v *Verifier) Verify(ctx context.Context, rawIDToken string) (*IDTokenClaims, error) {
	// Parse without verification first to get the kid from the header.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(rawIDToken, jwt.MapClaims{})
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCVerify,
			"oidc: failed to parse token header", err)
	}

	kid, _ := unverified.Header["kid"].(string)
	if kid == "" {
		return nil, errcode.New(ErrAdapterOIDCVerify,
			"oidc: token header missing kid")
	}

	pubKey, err := v.getKey(ctx, kid)
	if err != nil {
		return nil, err
	}

	// Parse and verify with the RSA public key.
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(rawIDToken, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "RS256" {
			return nil, errcode.New(ErrAdapterOIDCVerify,
				fmt.Sprintf("oidc: unexpected signing algorithm %s", t.Method.Alg()))
		}
		return pubKey, nil
	})
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCVerify,
			"oidc: token verification failed", err)
	}
	if !token.Valid {
		return nil, errcode.New(ErrAdapterOIDCVerify, "oidc: token is invalid")
	}

	// Validate issuer.
	iss, _ := claims["iss"].(string)
	if iss != v.provider.config.IssuerURL {
		return nil, errcode.New(ErrAdapterOIDCVerify,
			fmt.Sprintf("oidc: issuer mismatch: got %s, want %s", iss, v.provider.config.IssuerURL))
	}

	// Validate audience.
	if !v.audienceMatch(claims) {
		return nil, errcode.New(ErrAdapterOIDCVerify,
			"oidc: audience mismatch")
	}

	result := &IDTokenClaims{
		Issuer:  iss,
		Subject: claimString(claims, "sub"),
		Nonce:   claimString(claims, "nonce"),
		Email:   claimString(claims, "email"),
		Name:    claimString(claims, "name"),
	}

	if aud := claimString(claims, "aud"); aud != "" {
		result.Audience = aud
	}
	if exp, ok := claims["exp"].(float64); ok {
		result.ExpiresAt = int64(exp)
	}
	if iat, ok := claims["iat"].(float64); ok {
		result.IssuedAt = int64(iat)
	}

	return result, nil
}

// audienceMatch checks if the token audience contains the configured client ID.
func (v *Verifier) audienceMatch(claims jwt.MapClaims) bool {
	clientID := v.provider.config.ClientID

	switch aud := claims["aud"].(type) {
	case string:
		return aud == clientID
	case []any:
		for _, a := range aud {
			if s, ok := a.(string); ok && s == clientID {
				return true
			}
		}
	}
	return false
}

// getKey retrieves the RSA public key for the given kid, fetching JWKS if needed.
func (v *Verifier) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	if key, ok := v.keyCache[kid]; ok {
		v.mu.RUnlock()
		return key, nil
	}
	v.mu.RUnlock()

	// Fetch fresh JWKS.
	if err := v.fetchJWKS(ctx); err != nil {
		return nil, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()
	if key, ok := v.keyCache[kid]; ok {
		return key, nil
	}

	return nil, errcode.New(ErrAdapterOIDCJWKS,
		fmt.Sprintf("oidc: key with kid %q not found in JWKS", kid))
}

// fetchJWKS retrieves the JWKS from the provider and populates the key cache.
func (v *Verifier) fetchJWKS(ctx context.Context) error {
	doc, err := v.provider.Discover(ctx)
	if err != nil {
		return fmt.Errorf("oidc jwks: %w", err)
	}

	if doc.JWKSURI == "" {
		return errcode.New(ErrAdapterOIDCJWKS,
			"oidc: JWKS URI not found in discovery document")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.JWKSURI, nil)
	if err != nil {
		return errcode.Wrap(ErrAdapterOIDCJWKS,
			"oidc: failed to create JWKS request", err)
	}

	resp, err := v.provider.client.Do(req)
	if err != nil {
		return errcode.Wrap(ErrAdapterOIDCJWKS,
			"oidc: JWKS request failed", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("oidc: failed to close JWKS response body",
				slog.Any("error", closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return errcode.New(ErrAdapterOIDCJWKS,
			fmt.Sprintf("oidc: JWKS endpoint returned status %d", resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errcode.Wrap(ErrAdapterOIDCJWKS,
			"oidc: failed to read JWKS response", err)
	}

	var jwks JWKS
	if err := json.Unmarshal(body, &jwks); err != nil {
		return errcode.Wrap(ErrAdapterOIDCJWKS,
			"oidc: failed to parse JWKS", err)
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.jwks = &jwks
	v.keyCache = make(map[string]*rsa.PublicKey, len(jwks.Keys))
	v.fetchAt = time.Now()

	for _, key := range jwks.Keys {
		if key.Kty != "RSA" || key.Use != "sig" {
			continue
		}
		pubKey, err := parseRSAPublicKey(key)
		if err != nil {
			slog.Warn("oidc: failed to parse JWKS key",
				slog.String("kid", key.Kid),
				slog.Any("error", err),
			)
			continue
		}
		v.keyCache[key.Kid] = pubKey
	}

	slog.Info("oidc: JWKS fetched",
		slog.Int("key_count", len(v.keyCache)),
	)

	return nil
}

// parseRSAPublicKey converts a JWK to an *rsa.PublicKey.
func parseRSAPublicKey(key JWK) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, fmt.Errorf("oidc: decode modulus: %w", err)
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, fmt.Errorf("oidc: decode exponent: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}

	return &rsa.PublicKey{N: n, E: e}, nil
}

// claimString extracts a string claim from MapClaims.
func claimString(claims jwt.MapClaims, key string) string {
	s, _ := claims[key].(string)
	return s
}
