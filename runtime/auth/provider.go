package auth

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// EnvKeyProviderOption configures EnvKeyProvider behavior.
type EnvKeyProviderOption func(*EnvKeyProvider)

// WithEnvKeyProviderLogger sets the logger for EnvKeyProvider.
func WithEnvKeyProviderLogger(l *slog.Logger) EnvKeyProviderOption {
	return func(p *EnvKeyProvider) {
		if l != nil {
			p.logger = l
		}
	}
}

// KeyProvider abstracts the source of cryptographic key material.
// It is consumed at the composition root (main.go) to build JWTIssuer,
// JWTVerifier, and service token infrastructure.
//
// Implementations may load from environment variables, files, Vault, or JWKS
// endpoints. The provider caches loaded keys; consumers call RSAKeySet() or
// HMACKeyRing() and handle domain-specific errors.
//
// ref: lestrrat-go/jwx — jwk.Set interface for key set abstraction
// ref: coreos/go-oidc — RemoteKeySet cache-first pattern (future)
// Adopted: separate RSA / HMAC domains (ISP); cache at construction.
// Deviated: no remote refresh (FR-017 static config only).
type KeyProvider interface {
	// RSAKeySet returns the RSA KeySet for JWT signing and verification.
	// Returns an error if no RSA keys are configured.
	RSAKeySet() (*KeySet, error)

	// HMACKeyRing returns the HMACKeyRing for service token operations.
	// Returns an error if no HMAC secrets are configured.
	HMACKeyRing() (*HMACKeyRing, error)
}

// EnvKeyProvider loads key material from environment variables at construction.
// It succeeds even if some key domains are not configured; individual domain
// errors surface when RSAKeySet() or HMACKeyRing() is called.
//
// This provider loads keys once (fail-fast per domain). For hot-reload support,
// see future WM-34 integration.
type EnvKeyProvider struct {
	rsaKeySet *KeySet
	rsaErr    error
	hmacRing  *HMACKeyRing
	hmacErr   error
	logger    *slog.Logger
}

// NewEnvKeyProvider loads all available key material from environment variables.
// It returns a provider even if some key types are not configured — call
// RSAKeySet() or HMACKeyRing() to check individual domain availability.
func NewEnvKeyProvider(opts ...EnvKeyProviderOption) *EnvKeyProvider {
	p := &EnvKeyProvider{logger: slog.Default()}
	for _, o := range opts {
		o(p)
	}

	ks, rsaErr := LoadKeySetFromEnv()
	ring, hmacErr := LoadHMACKeyRingFromEnv()
	if rsaErr != nil {
		p.logger.Info("RSA key set not loaded from environment", "error", rsaErr.Error())
	}
	if hmacErr != nil {
		p.logger.Info("HMAC key ring not loaded from environment", "error", hmacErr.Error())
	}
	p.rsaKeySet = ks
	p.rsaErr = rsaErr
	p.hmacRing = ring
	p.hmacErr = hmacErr
	return p
}

// RSAKeySet returns the cached RSA KeySet or the error from loading.
func (p *EnvKeyProvider) RSAKeySet() (*KeySet, error) {
	return p.rsaKeySet, p.rsaErr
}

// HMACKeyRing returns the cached HMACKeyRing or the error from loading.
func (p *EnvKeyProvider) HMACKeyRing() (*HMACKeyRing, error) {
	return p.hmacRing, p.hmacErr
}

// StaticKeyProvider holds pre-constructed key material. Useful for tests and
// composition roots that load keys before building the provider.
type StaticKeyProvider struct {
	rsaKeySet *KeySet
	hmacRing  *HMACKeyRing
}

// NewStaticKeyProvider creates a KeyProvider from pre-existing key material.
// Either parameter may be nil; the corresponding getter returns an error.
func NewStaticKeyProvider(ks *KeySet, ring *HMACKeyRing) *StaticKeyProvider {
	return &StaticKeyProvider{rsaKeySet: ks, hmacRing: ring}
}

// RSAKeySet returns the pre-loaded KeySet or an error if nil.
func (p *StaticKeyProvider) RSAKeySet() (*KeySet, error) {
	if p.rsaKeySet == nil {
		return nil, errcode.New(errcode.ErrAuthKeyMissing, "RSA key set not configured")
	}
	return p.rsaKeySet, nil
}

// HMACKeyRing returns the pre-loaded HMACKeyRing or an error if nil.
func (p *StaticKeyProvider) HMACKeyRing() (*HMACKeyRing, error) {
	if p.hmacRing == nil {
		return nil, errcode.New(errcode.ErrAuthKeyMissing, "HMAC key ring not configured")
	}
	return p.hmacRing, nil
}

// MustNewTestKeyProvider creates a KeyProvider with ephemeral RSA and HMAC keys
// for testing. It panics on error, following the Go test helper convention.
func MustNewTestKeyProvider() KeyProvider {
	ks, _, _ := MustNewTestKeySet()
	ring, err := NewHMACKeyRing([]byte("test-hmac-secret-at-least-32-bytes!!"), nil)
	if err != nil {
		panic("auth: failed to create test HMAC key ring: " + err.Error())
	}
	return NewStaticKeyProvider(ks, ring)
}
