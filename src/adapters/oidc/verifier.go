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
	"strings"
	"sync"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// jwtHeader is the decoded JOSE header of a JWT.
type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

// jwtClaims is the decoded claims set of a JWT.
type jwtClaims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"`
	Audience audience `json:"aud"`
	Expiry   int64    `json:"exp"`
	IssuedAt int64    `json:"iat"`
	Nonce    string   `json:"nonce"`
	Email    string   `json:"email"`
}

// audience handles the fact that OIDC "aud" can be a string or []string.
type audience []string

// UnmarshalJSON implements json.Unmarshaler for audience.
func (a *audience) UnmarshalJSON(data []byte) error {
	// Try single string first.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = audience{s}
		return nil
	}
	// Then try array of strings.
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("oidc: aud must be string or []string: %w", err)
	}
	*a = audience(arr)
	return nil
}

// jwksKey is a single JSON Web Key from the JWKS endpoint.
type jwksKey struct {
	KeyType   string `json:"kty"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	N         string `json:"n"` // RSA modulus (base64url)
	E         string `json:"e"` // RSA exponent (base64url)
}

// jwksResponse is the JWKS endpoint response.
type jwksResponse struct {
	Keys []jwksKey `json:"keys"`
}

// Verifier verifies OIDC ID tokens using JWKS public keys.
// It implements runtime/auth.TokenVerifier.
type Verifier struct {
	provider *Provider

	mu   sync.RWMutex
	keys map[string]*rsa.PublicKey // kid -> public key
}

// NewVerifier creates a Verifier backed by the given Provider.
// It eagerly fetches the JWKS key set.
func NewVerifier(ctx context.Context, provider *Provider) (*Verifier, error) {
	v := &Verifier{
		provider: provider,
		keys:     make(map[string]*rsa.PublicKey),
	}
	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

// Verify validates the raw ID token string: decodes the JWT, checks the RS256
// signature against JWKS keys, and validates exp, iss, and aud claims.
// It satisfies runtime/auth.TokenVerifier.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (auth.Claims, error) {
	parts := strings.SplitN(rawToken, ".", 3)
	if len(parts) != 3 {
		return auth.Claims{}, errcode.New(ErrTokenVerify, "oidc: token must have 3 parts")
	}

	// Decode header.
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return auth.Claims{}, errcode.Wrap(ErrTokenVerify, "oidc: failed to decode token header", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return auth.Claims{}, errcode.Wrap(ErrTokenVerify, "oidc: failed to parse token header", err)
	}
	if header.Algorithm != "RS256" {
		return auth.Claims{}, errcode.New(ErrTokenVerify,
			fmt.Sprintf("oidc: unsupported signing algorithm %q, expected RS256", header.Algorithm))
	}

	// Look up signing key by kid.
	pubKey, err := v.keyForKID(ctx, header.KeyID)
	if err != nil {
		return auth.Claims{}, err
	}

	// Verify signature.
	if err := verifyRS256(parts[0]+"."+parts[1], parts[2], pubKey); err != nil {
		return auth.Claims{}, errcode.Wrap(ErrTokenVerify, "oidc: signature verification failed", err)
	}

	// Decode claims.
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return auth.Claims{}, errcode.Wrap(ErrTokenVerify, "oidc: failed to decode token claims", err)
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return auth.Claims{}, errcode.Wrap(ErrTokenVerify, "oidc: failed to parse token claims", err)
	}

	// Validate expiry.
	if time.Now().Unix() > claims.Expiry {
		return auth.Claims{}, errcode.New(ErrTokenVerify, "oidc: token has expired")
	}

	// Validate issuer.
	if claims.Issuer != v.provider.cfg.IssuerURL {
		return auth.Claims{}, errcode.New(ErrTokenVerify,
			fmt.Sprintf("oidc: issuer mismatch: got %q, expected %q", claims.Issuer, v.provider.cfg.IssuerURL))
	}

	// Validate audience.
	if !containsAudience(claims.Audience, v.provider.cfg.ClientID) {
		return auth.Claims{}, errcode.New(ErrTokenVerify,
			fmt.Sprintf("oidc: client ID %q not found in audience %v", v.provider.cfg.ClientID, []string(claims.Audience)))
	}

	return auth.Claims{
		Subject:   claims.Subject,
		Issuer:    claims.Issuer,
		Audience:  []string(claims.Audience),
		ExpiresAt: time.Unix(claims.Expiry, 0),
		IssuedAt:  time.Unix(claims.IssuedAt, 0),
		Extra: map[string]any{
			"email": claims.Email,
			"nonce": claims.Nonce,
		},
	}, nil
}

// keyForKID returns the RSA public key matching the given kid.
// On miss it refreshes the JWKS and retries once (kid rotation support).
func (v *Verifier) keyForKID(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	v.mu.RUnlock()
	if ok {
		return key, nil
	}

	slog.Info("oidc: kid not found in cache, refreshing JWKS",
		slog.String("kid", kid),
	)

	if err := v.refreshKeys(ctx); err != nil {
		return nil, err
	}

	v.mu.RLock()
	key, ok = v.keys[kid]
	v.mu.RUnlock()
	if !ok {
		return nil, errcode.New(ErrTokenVerify,
			fmt.Sprintf("oidc: signing key %q not found in JWKS", kid))
	}
	return key, nil
}

// refreshKeys fetches the JWKS endpoint and replaces the cached key set.
func (v *Verifier) refreshKeys(ctx context.Context) error {
	md, err := v.provider.Metadata(ctx)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, md.JWKSURI, nil)
	if err != nil {
		return errcode.Wrap(ErrTokenVerify, "oidc: failed to build JWKS request", err)
	}

	resp, err := v.provider.client.Do(req)
	if err != nil {
		return errcode.Wrap(ErrTokenVerify, "oidc: JWKS request failed", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return errcode.New(ErrTokenVerify,
			fmt.Sprintf("oidc: JWKS endpoint returned HTTP %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return errcode.Wrap(ErrTokenVerify, "oidc: failed to read JWKS response", err)
	}

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return errcode.Wrap(ErrTokenVerify, "oidc: failed to decode JWKS response", err)
	}

	newKeys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.KeyType != "RSA" || k.Use != "sig" {
			continue
		}
		pub, err := parseRSAPublicKey(k)
		if err != nil {
			slog.Warn("oidc: skipping invalid JWKS key",
				slog.String("kid", k.KeyID),
				slog.String("error", err.Error()),
			)
			continue
		}
		newKeys[k.KeyID] = pub
	}

	v.mu.Lock()
	v.keys = newKeys
	v.mu.Unlock()

	slog.Info("oidc: JWKS refreshed",
		slog.Int("keyCount", len(newKeys)),
	)
	return nil
}

// parseRSAPublicKey converts a JWKS key into an *rsa.PublicKey.
func parseRSAPublicKey(k jwksKey) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("oidc: failed to decode modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("oidc: failed to decode exponent: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() {
		return nil, fmt.Errorf("oidc: exponent too large")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// containsAudience checks whether the target audience is present in the list.
func containsAudience(auds []string, target string) bool {
	for _, a := range auds {
		if a == target {
			return true
		}
	}
	return false
}
