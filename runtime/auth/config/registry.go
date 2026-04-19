// Package config provides the JWT configuration Registry — a single source of
// truth for JWT issuer, audiences, key material, and clock.
//
// ref: Hydra internal/driver/config.DefaultProvider — single Registry pattern
// ref: Kratos middleware/auth/jwt WithParserOptions — one-time injection
// plan: docs/plans/202604191515-auth-federated-whistle.md §F1
package config

import (
	"os"
	"strings"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Registry is the single source of truth for JWT configuration.
// All JWT consumers (JWTIssuer, JWTVerifier, middleware) obtain their
// configuration from Registry instead of carrying per-instance copies.
//
// Registry is safe for concurrent read access after construction.
type Registry struct {
	issuer    string
	audiences []string
	keyProv   auth.SigningKeyProvider
	keyStore  auth.VerificationKeyStore
	clock     func() time.Time
	realMode  bool
}

// Config carries the inputs for constructing a Registry.
type Config struct {
	// Issuer is the JWT issuer claim (iss). Required when RealMode is true.
	// Corresponds to GOCELL_JWT_ISSUER.
	Issuer string

	// Audiences is the list of accepted JWT audience values. Required when
	// RealMode is true. Corresponds to GOCELL_JWT_AUDIENCE (single value
	// stored as []string{"value"}). Future: GOCELL_JWT_AUDIENCES comma-separated.
	Audiences []string

	// KeyProv supplies the active RSA signing key. May be nil in non-real mode.
	KeyProv auth.SigningKeyProvider

	// KeyStore provides public keys for JWT verification. May be nil in non-real mode.
	KeyStore auth.VerificationKeyStore

	// Clock overrides the time source. Nil defaults to time.Now.
	Clock func() time.Time

	// RealMode enforces non-empty Issuer and Audiences at construction time.
	// Set to true in production; leave false for dev/test.
	RealMode bool
}

// New constructs a Registry from the given Config. Returns an error when
// RealMode is true and Issuer or Audiences are missing.
//
// Configuration errors use errcode.ErrAuthVerifierConfig so operators can
// distinguish startup misconfigurations from runtime key errors.
func New(cfg Config) (*Registry, error) {
	if cfg.RealMode {
		if cfg.Issuer == "" {
			return nil, errcode.New(errcode.ErrAuthVerifierConfig,
				"JWT registry: Issuer is required in real mode (set GOCELL_JWT_ISSUER)")
		}
		if len(cfg.Audiences) == 0 {
			return nil, errcode.New(errcode.ErrAuthVerifierConfig,
				"JWT registry: Audiences must not be empty in real mode (set GOCELL_JWT_AUDIENCE)")
		}
	}

	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}

	auds := make([]string, len(cfg.Audiences))
	copy(auds, cfg.Audiences)

	return &Registry{
		issuer:    cfg.Issuer,
		audiences: auds,
		keyProv:   cfg.KeyProv,
		keyStore:  cfg.KeyStore,
		clock:     clock,
		realMode:  cfg.RealMode,
	}, nil
}

// Issuer returns the JWT issuer string (iss claim).
func (r *Registry) Issuer() string { return r.issuer }

// Audiences returns a defensive copy of the configured audience allowlist.
// Mutating the returned slice does not affect the Registry.
func (r *Registry) Audiences() []string {
	if len(r.audiences) == 0 {
		return nil
	}
	out := make([]string, len(r.audiences))
	copy(out, r.audiences)
	return out
}

// SigningKeyProvider returns the configured key provider for JWT signing.
// May be nil when the Registry was constructed in non-real mode without keys.
func (r *Registry) SigningKeyProvider() auth.SigningKeyProvider { return r.keyProv }

// VerificationKeyStore returns the configured key store for JWT verification.
// May be nil when the Registry was constructed in non-real mode without keys.
func (r *Registry) VerificationKeyStore() auth.VerificationKeyStore { return r.keyStore }

// Clock returns the time function used for token timestamps.
// Always non-nil — defaults to time.Now when not configured.
func (r *Registry) Clock() func() time.Time { return r.clock }

// ---- FromEnv ----

// EnvOption configures FromEnv behavior.
type EnvOption func(*envConfig)

type envConfig struct {
	realMode bool
	keyProv  auth.SigningKeyProvider
	keyStore auth.VerificationKeyStore
	clock    func() time.Time
}

// WithRealMode enables real-mode validation (fail-fast on missing env vars).
func WithRealMode(v bool) EnvOption {
	return func(c *envConfig) { c.realMode = v }
}

// WithKeys sets the key provider and key store for the registry built by FromEnv.
func WithKeys(prov auth.SigningKeyProvider) EnvOption {
	return func(c *envConfig) {
		c.keyProv = prov
		if ks, ok := prov.(auth.VerificationKeyStore); ok {
			c.keyStore = ks
		}
	}
}

// WithKeySeparate sets key provider and key store independently.
func WithKeySeparate(prov auth.SigningKeyProvider, store auth.VerificationKeyStore) EnvOption {
	return func(c *envConfig) {
		c.keyProv = prov
		c.keyStore = store
	}
}

// WithEnvClock overrides the time source for a FromEnv-built Registry.
func WithEnvClock(fn func() time.Time) EnvOption {
	return func(c *envConfig) { c.clock = fn }
}

// envVarIssuer is the environment variable for the JWT issuer.
// Defined as constant to satisfy ≥3-use string rule.
const envVarIssuer = "GOCELL_JWT_ISSUER"

// envVarAudience is the environment variable for the JWT audience.
// Future: GOCELL_JWT_AUDIENCES (comma-separated multi-value) — see
// docs/ops/env-vars.md for migration notes.
const envVarAudience = "GOCELL_JWT_AUDIENCE"

// FromEnv constructs a Registry by reading GOCELL_JWT_ISSUER and
// GOCELL_JWT_AUDIENCE from the environment.
//
// The audience env var stores a single value stored as []string{value}.
// When the value is empty and RealMode is false, Audiences will be nil.
//
// Returns an error in real mode when required env vars are missing or empty.
func FromEnv(opts ...EnvOption) (*Registry, error) {
	ec := &envConfig{}
	for _, o := range opts {
		o(ec)
	}

	issuer := strings.TrimSpace(os.Getenv(envVarIssuer))
	audience := strings.TrimSpace(os.Getenv(envVarAudience))

	var audiences []string
	if audience != "" {
		audiences = []string{audience}
	}

	return New(Config{
		Issuer:    issuer,
		Audiences: audiences,
		KeyProv:   ec.keyProv,
		KeyStore:  ec.keyStore,
		Clock:     ec.clock,
		RealMode:  ec.realMode,
	})
}

// ---- Factory functions ----

// NewJWTIssuerFromRegistry constructs a *auth.JWTIssuer whose issuer string,
// default audiences, signing key, and clock are all sourced from reg.
//
// This is the single authorised entry point for creating a JWTIssuer in
// production code; the raw NewJWTIssuer constructor is retained only for
// test helpers that build issuers independently of Registry.
//
// ref: Hydra internal/driver/config.DefaultProvider — configuration through registry
func NewJWTIssuerFromRegistry(reg *Registry, ttl time.Duration, opts ...auth.JWTIssuerOption) (*auth.JWTIssuer, error) {
	if reg == nil {
		return nil, errcode.New(errcode.ErrAuthVerifierConfig, "JWT registry must not be nil")
	}
	if reg.keyProv == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "JWT registry: SigningKeyProvider is nil")
	}

	// Merge registry-derived settings first, then apply caller opts so opts
	// can override (e.g. WithRefreshTTL in tests).
	baseOpts := []auth.JWTIssuerOption{
		auth.WithIssuerAudiencesFromSlice(reg.Audiences()),
		auth.WithIssuerClock(reg.Clock()),
	}
	return auth.NewJWTIssuer(reg.keyProv, reg.issuer, ttl, append(baseOpts, opts...)...)
}

// NewJWTVerifierFromRegistry constructs a *auth.JWTVerifier whose expected
// audiences, expected issuer, verification key store, and clock are all
// sourced from reg.
//
// ref: Hydra internal/driver/config.DefaultProvider — configuration through registry
func NewJWTVerifierFromRegistry(reg *Registry, opts ...auth.JWTVerifierOption) (*auth.JWTVerifier, error) {
	if reg == nil {
		return nil, errcode.New(errcode.ErrAuthVerifierConfig, "JWT registry must not be nil")
	}
	if reg.keyStore == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "JWT registry: VerificationKeyStore is nil")
	}
	auds := reg.Audiences()
	if len(auds) == 0 {
		return nil, errcode.New(errcode.ErrAuthVerifierConfig,
			"JWT registry: Audiences must not be empty for verifier construction")
	}

	baseOpts := []auth.JWTVerifierOption{
		auth.WithExpectedAudiences(auds[0], auds[1:]...),
		auth.WithExpectedIssuer(reg.issuer),
		auth.WithVerifierClock(reg.Clock()),
	}
	return auth.NewJWTVerifier(reg.keyStore, append(baseOpts, opts...)...)
}
