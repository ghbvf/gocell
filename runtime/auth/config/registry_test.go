// Package config_test exercises the JWT config.Registry single-source-of-truth.
//
// ref: Hydra internal/driver/config.DefaultProvider — single Registry pattern
// ref: Kratos middleware/auth/jwt WithParserOptions — one-time injection
// plan: docs/plans/202604191515-auth-federated-whistle.md §F1
package config_test

import (
	"context"
	"crypto/rsa"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/config"
)

// stubKeySet is a minimal in-memory key provider/store for tests.
// It satisfies both auth.SigningKeyProvider and auth.VerificationKeyStore.
type stubKeySet struct{}

func (s *stubKeySet) SigningKey() *rsa.PrivateKey                     { return nil }
func (s *stubKeySet) SigningKeyID() string                            { return "stub-kid" }
func (s *stubKeySet) PublicKeyByKID(_ string) (*rsa.PublicKey, error) { return nil, nil }

// TestNew_RealMode_IssuerRequired verifies that RealMode=true requires a non-empty Issuer.
func TestNew_RealMode_IssuerRequired(t *testing.T) {
	_, err := config.New(config.Config{
		Issuer:    "",
		Audiences: []string{"gocell"},
		RealMode:  true,
	})
	require.Error(t, err, "RealMode + empty Issuer must return error")
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must be errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrAuthVerifierConfig, ecErr.Code,
		"config error must use ErrAuthVerifierConfig")
}

// TestNew_RealMode_AudiencesRequired verifies that RealMode=true requires non-nil Audiences.
func TestNew_RealMode_AudiencesRequired(t *testing.T) {
	_, err := config.New(config.Config{
		Issuer:    "https://gocell.example",
		Audiences: nil,
		RealMode:  true,
	})
	require.Error(t, err, "RealMode + nil Audiences must return error")
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must be errcode.Error")
	assert.Equal(t, errcode.ErrAuthVerifierConfig, ecErr.Code)
}

// TestNew_RealMode_EmptyAudiencesRequired verifies that RealMode=true rejects empty Audiences slice.
func TestNew_RealMode_EmptyAudiencesRequired(t *testing.T) {
	_, err := config.New(config.Config{
		Issuer:    "https://gocell.example",
		Audiences: []string{},
		RealMode:  true,
	})
	require.Error(t, err, "RealMode + empty Audiences slice must return error")
}

// TestNew_NonRealMode_AllowsEmpty verifies that dev/test mode allows empty config (no hard error).
func TestNew_NonRealMode_AllowsEmpty(t *testing.T) {
	reg, err := config.New(config.Config{
		Issuer:    "",
		Audiences: nil,
		RealMode:  false,
	})
	require.NoError(t, err, "non-real mode must allow empty config")
	require.NotNil(t, reg)
	assert.Equal(t, "", reg.Issuer())
	assert.Empty(t, reg.Audiences())
}

// TestRegistry_Audiences_ReturnsCopy verifies that mutating the returned slice
// does not affect subsequent calls (defensive copy).
func TestRegistry_Audiences_ReturnsCopy(t *testing.T) {
	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: []string{"gocell", "api-gateway"},
		RealMode:  true,
	})
	require.NoError(t, err)

	first := reg.Audiences()
	first[0] = "mutated"

	second := reg.Audiences()
	assert.Equal(t, "gocell", second[0],
		"mutating returned slice must not affect registry state")
}

// TestRegistry_Clock_DefaultsToTimeNow verifies that a nil Clock falls back to time.Now.
func TestRegistry_Clock_DefaultsToTimeNow(t *testing.T) {
	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: []string{"gocell"},
		Clock:     nil,
		RealMode:  true,
	})
	require.NoError(t, err)

	clockFn := reg.Clock()
	require.NotNil(t, clockFn, "Clock() must not return nil")

	now := time.Now()
	clocked := clockFn()
	delta := clocked.Sub(now)
	if delta < 0 {
		delta = -delta
	}
	assert.Less(t, delta, time.Second,
		"default clock must return time close to time.Now()")
}

// TestFromEnv_ReadsIssuerAndAudience verifies that FromEnv reads GOCELL_JWT_ISSUER
// and GOCELL_JWT_AUDIENCE from the environment.
func TestFromEnv_ReadsIssuerAndAudience(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "https://auth.example")
	t.Setenv("GOCELL_JWT_AUDIENCE", "my-service")

	reg, err := config.FromEnv()
	require.NoError(t, err)
	assert.Equal(t, "https://auth.example", reg.Issuer())
	assert.Equal(t, []string{"my-service"}, reg.Audiences())
}

// TestFromEnv_MissingIssuer_RealMode_Error verifies that FromEnv in real mode
// fails fast when GOCELL_JWT_ISSUER is unset.
func TestFromEnv_MissingIssuer_RealMode_Error(t *testing.T) {
	// Ensure env is clear.
	t.Setenv("GOCELL_JWT_ISSUER", "")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	_, err := config.FromEnv(config.WithRealMode(true))
	require.Error(t, err, "missing GOCELL_JWT_ISSUER in real mode must return error")
}

// TestFromEnv_MissingAudience_RealMode_Error verifies fail-fast on missing GOCELL_JWT_AUDIENCE.
func TestFromEnv_MissingAudience_RealMode_Error(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "https://auth.example")
	t.Setenv("GOCELL_JWT_AUDIENCE", "")

	_, err := config.FromEnv(config.WithRealMode(true))
	require.Error(t, err, "missing GOCELL_JWT_AUDIENCE in real mode must return error")
}

// TestRegistry_KeyProviders_PassThrough verifies that key provider/store supplied
// via Config are returned unchanged by the accessor methods.
func TestRegistry_KeyProviders_PassThrough(t *testing.T) {
	_ = auth.SigningKeyProvider((*stubKeySet)(nil))   // compile-time interface check
	_ = auth.VerificationKeyStore((*stubKeySet)(nil)) // compile-time interface check

	prov := &stubKeySet{}
	store := &stubKeySet{}

	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: []string{"gocell"},
		KeyProv:   prov,
		KeyStore:  store,
		RealMode:  true,
	})
	require.NoError(t, err)
	assert.Same(t, prov, reg.SigningKeyProvider(),
		"SigningKeyProvider() must return the same object that was passed in")
	assert.Same(t, store, reg.VerificationKeyStore(),
		"VerificationKeyStore() must return the same object that was passed in")
}

// TestFromEnv_IgnoresUnsetVars verifies FromEnv returns empty values for unset env (non-real mode).
func TestFromEnv_IgnoresUnsetVars(t *testing.T) {
	os.Unsetenv("GOCELL_JWT_ISSUER")   //nolint:errcheck
	os.Unsetenv("GOCELL_JWT_AUDIENCE") //nolint:errcheck

	reg, err := config.FromEnv()
	require.NoError(t, err)
	assert.Equal(t, "", reg.Issuer())
	assert.Empty(t, reg.Audiences())
}

// TestFromEnv_WithKeys verifies that WithKeys sets both key provider and store
// when the same object implements both interfaces.
func TestFromEnv_WithKeys(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	ks, _, _ := auth.MustNewTestKeySet()
	reg, err := config.FromEnv(config.WithKeys(ks))
	require.NoError(t, err)
	assert.NotNil(t, reg.SigningKeyProvider(), "SigningKeyProvider must be set via WithKeys")
	assert.NotNil(t, reg.VerificationKeyStore(), "VerificationKeyStore must be set via WithKeys")
}

// TestFromEnv_WithKeySeparate verifies WithKeySeparate sets providers independently.
func TestFromEnv_WithKeySeparate(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	prov := &stubKeySet{}
	store := &stubKeySet{}

	reg, err := config.FromEnv(config.WithKeySeparate(prov, store))
	require.NoError(t, err)
	assert.Same(t, prov, reg.SigningKeyProvider())
	assert.Same(t, store, reg.VerificationKeyStore())
}

// TestFromEnv_WithEnvClock verifies WithEnvClock overrides the clock.
func TestFromEnv_WithEnvClock(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clockFn := func() time.Time { return fixed }

	reg, err := config.FromEnv(config.WithEnvClock(clockFn))
	require.NoError(t, err)
	assert.Equal(t, fixed, reg.Clock()(), "custom clock must be used")
}

// TestNewJWTIssuerFromRegistry_Success verifies that a Registry with valid keys
// produces a working JWTIssuer.
func TestNewJWTIssuerFromRegistry_Success(t *testing.T) {
	ks, _, _ := auth.MustNewTestKeySet()
	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: []string{"gocell"},
		KeyProv:   ks,
		KeyStore:  ks,
		RealMode:  true,
	})
	require.NoError(t, err)

	issuer, err := config.NewJWTIssuerFromRegistry(reg, 15*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, issuer)

	// Issue a token to verify it works end-to-end.
	tok, err := issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, tok)
}

// TestNewJWTIssuerFromRegistry_NilRegistry returns an error.
func TestNewJWTIssuerFromRegistry_NilRegistry(t *testing.T) {
	_, err := config.NewJWTIssuerFromRegistry(nil, 15*time.Minute)
	require.Error(t, err)
}

// TestNewJWTIssuerFromRegistry_NilKeyProv returns an error when KeyProv is nil.
func TestNewJWTIssuerFromRegistry_NilKeyProv(t *testing.T) {
	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: []string{"gocell"},
		KeyProv:   nil, // nil
		RealMode:  false,
	})
	require.NoError(t, err)

	_, err = config.NewJWTIssuerFromRegistry(reg, 15*time.Minute)
	require.Error(t, err, "nil KeyProv must return error")
}

// TestNewJWTVerifierFromRegistry_Success verifies that a Registry with valid keys
// produces a working JWTVerifier.
func TestNewJWTVerifierFromRegistry_Success(t *testing.T) {
	ks, _, _ := auth.MustNewTestKeySet()
	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: []string{"gocell"},
		KeyProv:   ks,
		KeyStore:  ks,
		RealMode:  true,
	})
	require.NoError(t, err)

	verifier, err := config.NewJWTVerifierFromRegistry(reg)
	require.NoError(t, err)
	require.NotNil(t, verifier)
}

// TestNewJWTVerifierFromRegistry_NilRegistry returns an error.
func TestNewJWTVerifierFromRegistry_NilRegistry(t *testing.T) {
	_, err := config.NewJWTVerifierFromRegistry(nil)
	require.Error(t, err)
}

// TestNewJWTVerifierFromRegistry_NilKeyStore returns an error when KeyStore is nil.
func TestNewJWTVerifierFromRegistry_NilKeyStore(t *testing.T) {
	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: []string{"gocell"},
		KeyStore:  nil, // nil
		RealMode:  false,
	})
	require.NoError(t, err)

	_, err = config.NewJWTVerifierFromRegistry(reg)
	require.Error(t, err, "nil KeyStore must return error")
}

// TestNewJWTVerifierFromRegistry_EmptyAudiences returns an error when Audiences is empty.
func TestNewJWTVerifierFromRegistry_EmptyAudiences(t *testing.T) {
	ks, _, _ := auth.MustNewTestKeySet()
	reg, err := config.New(config.Config{
		Issuer:    "gocell",
		Audiences: nil, // empty
		KeyStore:  ks,
		RealMode:  false,
	})
	require.NoError(t, err)

	_, err = config.NewJWTVerifierFromRegistry(reg)
	require.Error(t, err, "empty Audiences must return error for verifier construction")
}

// TestNewJWTIssuerVerifierFromRegistry_EndToEnd verifies the full round-trip:
// issue a token via Registry-constructed issuer, verify with Registry-constructed verifier.
func TestNewJWTIssuerVerifierFromRegistry_EndToEnd(t *testing.T) {
	ks, _, _ := auth.MustNewTestKeySet()
	reg, err := config.New(config.Config{
		Issuer:    "gocell-test",
		Audiences: []string{"gocell"},
		KeyProv:   ks,
		KeyStore:  ks,
		RealMode:  true,
	})
	require.NoError(t, err)

	issuer, err := config.NewJWTIssuerFromRegistry(reg, 15*time.Minute)
	require.NoError(t, err)

	verifier, err := config.NewJWTVerifierFromRegistry(reg)
	require.NoError(t, err)

	tok, err := issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{})
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.Equal(t, "gocell-test", claims.Issuer)
	assert.Contains(t, claims.Audience, "gocell")
}
