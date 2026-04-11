package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// MinRSAKeyBits is the minimum RSA key size accepted by the auth package.
// Keys smaller than 2048 bits (e.g. 512 or 1024) are considered insecure and
// will be rejected during parsing and verifier/issuer construction.
const MinRSAKeyBits = 2048

// validateRSAKeySize checks that the RSA key modulus is at least MinRSAKeyBits.
func validateRSAKeySize(n int, keyKind string) error {
	if n < MinRSAKeyBits {
		return errcode.New(errcode.ErrAuthKeyInvalid,
			fmt.Sprintf("RSA %s key size %d bits is below the minimum %d bits", keyKind, n, MinRSAKeyBits))
	}
	return nil
}

// Thumbprint computes the RFC 7638 JSON Web Key (JWK) Thumbprint of an RSA
// public key using SHA-256. The result is a base64url-encoded (no padding)
// hash that serves as a deterministic key identifier (kid).
//
// ref: RFC 7638 §3.2 — required members for RSA in lexicographic order: "e", "kty", "n"
func Thumbprint(pub *rsa.PublicKey) string {
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	canonical := fmt.Sprintf(`{"e":"%s","kty":"RSA","n":"%s"}`, e, n)
	hash := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// VerificationKey is a previously active signing key that has been demoted.
// It retains the public key for token validation during the grace period.
type VerificationKey struct {
	PublicKey *rsa.PublicKey
	KeyID     string
	ExpiresAt time.Time
}

// KeySet holds a set of cryptographic keys for JWT operations: one active
// signing key and zero or more verification-only keys. It provides O(1)
// key lookup by kid (key identifier).
//
// ref: dexidp/dex server/rotation.go — 3-state model (Active → Verification-only → Pruned)
type KeySet struct {
	signingKey       *rsa.PrivateKey
	signingPub       *rsa.PublicKey
	signingKeyID     string
	verificationKeys []VerificationKey
	keyIndex         map[string]*rsa.PublicKey // kid → public key
}

// NewKeySet creates a KeySet with a single active signing key pair.
// The kid is derived deterministically from the public key using RFC 7638.
func NewKeySet(priv *rsa.PrivateKey, pub *rsa.PublicKey) (*KeySet, error) {
	if priv == nil || pub == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "signing key pair must not be nil")
	}
	if err := validateRSAKeySize(pub.N.BitLen(), "public"); err != nil {
		return nil, err
	}

	kid := Thumbprint(pub)
	ks := &KeySet{
		signingKey:   priv,
		signingPub:   pub,
		signingKeyID: kid,
		keyIndex:     map[string]*rsa.PublicKey{kid: pub},
	}

	slog.Info("key activated",
		slog.String("kid", kid),
		slog.String("transition", "activated"),
	)

	return ks, nil
}

// NewKeySetWithVerificationKeys creates a KeySet with an active signing key
// and one or more verification-only keys. Keys that are already expired at
// construction time are pruned immediately.
func NewKeySetWithVerificationKeys(priv *rsa.PrivateKey, pub *rsa.PublicKey, vkeys []VerificationKey) (*KeySet, error) {
	ks, err := NewKeySet(priv, pub)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	for _, vk := range vkeys {
		if !now.Before(vk.ExpiresAt) {
			slog.Info("key pruned",
				slog.String("kid", vk.KeyID),
				slog.String("transition", "pruned"),
				slog.String("reason", "already expired at load time"),
			)
			continue
		}
		ks.verificationKeys = append(ks.verificationKeys, vk)
		ks.keyIndex[vk.KeyID] = vk.PublicKey
		slog.Info("key demoted to verification-only",
			slog.String("kid", vk.KeyID),
			slog.String("transition", "verification-only"),
			slog.Time("expiresAt", vk.ExpiresAt),
		)
	}

	return ks, nil
}

// SigningKeyID returns the kid of the active signing key.
func (ks *KeySet) SigningKeyID() string {
	return ks.signingKeyID
}

// SigningKey returns the active private key for signing.
func (ks *KeySet) SigningKey() *rsa.PrivateKey {
	return ks.signingKey
}

// PublicKeyByKID looks up a public key by its kid. It prunes expired
// verification keys before lookup. Returns an error if the kid is unknown.
func (ks *KeySet) PublicKeyByKID(kid string) (*rsa.PublicKey, error) {
	ks.PruneExpired()
	pub, ok := ks.keyIndex[kid]
	if !ok {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, fmt.Sprintf("unknown kid: %s", kid))
	}
	return pub, nil
}

// PruneExpired removes verification-only keys whose expiry time has passed.
func (ks *KeySet) PruneExpired() {
	now := time.Now()
	remaining := ks.verificationKeys[:0]
	for _, vk := range ks.verificationKeys {
		if now.Before(vk.ExpiresAt) {
			remaining = append(remaining, vk)
		} else {
			delete(ks.keyIndex, vk.KeyID)
			slog.Info("key pruned",
				slog.String("kid", vk.KeyID),
				slog.String("transition", "pruned"),
			)
		}
	}
	ks.verificationKeys = remaining
}

// MustGenerateTestKeyPair generates a 2048-bit RSA key pair for testing.
// It panics on error, following the Go test helper convention (e.g., template.Must).
// Do NOT use in production code.
func MustGenerateTestKeyPair() (*rsa.PrivateKey, *rsa.PublicKey) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Sprintf("auth: failed to generate test RSA key pair: %v", err))
	}
	return priv, &priv.PublicKey
}

// LoadRSAKeyPairFromPEM parses PEM-encoded RSA private and public keys.
func LoadRSAKeyPairFromPEM(privPEM, pubPEM []byte) (*rsa.PrivateKey, *rsa.PublicKey, error) {
	priv, err := parseRSAPrivateKey(privPEM)
	if err != nil {
		return nil, nil, err
	}
	pub, err := parseRSAPublicKey(pubPEM)
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

const (
	// EnvJWTPrivateKey is the environment variable for the PEM-encoded RSA private key.
	EnvJWTPrivateKey = "GOCELL_JWT_PRIVATE_KEY"
	// EnvJWTPublicKey is the environment variable for the PEM-encoded RSA public key.
	EnvJWTPublicKey = "GOCELL_JWT_PUBLIC_KEY"
	// EnvJWTPrevPublicKey is the environment variable for the previous (verification-only) public key.
	EnvJWTPrevPublicKey = "GOCELL_JWT_PREV_PUBLIC_KEY"
	// EnvJWTPrevKeyExpires is the environment variable for the expiry of the previous key (RFC 3339).
	EnvJWTPrevKeyExpires = "GOCELL_JWT_PREV_KEY_EXPIRES"
)

// ErrKeyMissing indicates a required JWT key environment variable is not set.
var ErrKeyMissing = errcode.ErrAuthKeyMissing

// LoadKeysFromEnv reads PEM-encoded RSA keys from environment variables
// GOCELL_JWT_PRIVATE_KEY and GOCELL_JWT_PUBLIC_KEY. It returns an errcode
// error if either variable is missing or contains invalid PEM/key data.
func LoadKeysFromEnv() (privateKey *rsa.PrivateKey, publicKey *rsa.PublicKey, err error) {
	privPEM := os.Getenv(EnvJWTPrivateKey)
	if privPEM == "" {
		return nil, nil, errcode.New(ErrKeyMissing,
			fmt.Sprintf("environment variable %s is not set", EnvJWTPrivateKey))
	}

	pubPEM := os.Getenv(EnvJWTPublicKey)
	if pubPEM == "" {
		return nil, nil, errcode.New(ErrKeyMissing,
			fmt.Sprintf("environment variable %s is not set", EnvJWTPublicKey))
	}

	privateKey, err = parseRSAPrivateKey([]byte(privPEM))
	if err != nil {
		return nil, nil, errcode.Wrap(ErrKeyMissing,
			fmt.Sprintf("failed to parse %s", EnvJWTPrivateKey), err)
	}

	publicKey, err = parseRSAPublicKey([]byte(pubPEM))
	if err != nil {
		return nil, nil, errcode.Wrap(ErrKeyMissing,
			fmt.Sprintf("failed to parse %s", EnvJWTPublicKey), err)
	}

	return privateKey, publicKey, nil
}

// LoadKeySetFromEnv loads a KeySet from environment variables. It reads the
// active key pair from GOCELL_JWT_PRIVATE_KEY / GOCELL_JWT_PUBLIC_KEY, and
// optionally loads a verification-only key from GOCELL_JWT_PREV_PUBLIC_KEY
// with expiry from GOCELL_JWT_PREV_KEY_EXPIRES (RFC 3339).
func LoadKeySetFromEnv() (*KeySet, error) {
	priv, pub, err := LoadKeysFromEnv()
	if err != nil {
		return nil, err
	}

	prevPubPEM := os.Getenv(EnvJWTPrevPublicKey)
	if prevPubPEM == "" {
		return NewKeySet(priv, pub)
	}

	prevPub, err := parseRSAPublicKey([]byte(prevPubPEM))
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrAuthKeyInvalid,
			fmt.Sprintf("failed to parse %s", EnvJWTPrevPublicKey), err)
	}

	expiresStr := os.Getenv(EnvJWTPrevKeyExpires)
	if expiresStr == "" {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid,
			fmt.Sprintf("%s is set but %s is missing", EnvJWTPrevPublicKey, EnvJWTPrevKeyExpires))
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrAuthKeyInvalid,
			fmt.Sprintf("failed to parse %s as RFC 3339", EnvJWTPrevKeyExpires), err)
	}

	vk := VerificationKey{
		PublicKey: prevPub,
		KeyID:     Thumbprint(prevPub),
		ExpiresAt: expiresAt,
	}

	return NewKeySetWithVerificationKeys(priv, pub, []VerificationKey{vk})
}

func parseRSAPrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "no PEM block found in private key data")
	}

	// Try PKCS#8 first, then PKCS#1.
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errcode.New(errcode.ErrAuthKeyInvalid, "PKCS#8 key is not RSA")
		}
		if err := validateRSAKeySize(rsaKey.N.BitLen(), "private"); err != nil {
			return nil, err
		}
		return rsaKey, nil
	}

	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrAuthKeyInvalid, "failed to parse RSA private key", err)
	}
	if err := validateRSAKeySize(rsaKey.N.BitLen(), "private"); err != nil {
		return nil, err
	}
	return rsaKey, nil
}

func parseRSAPublicKey(pemData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errcode.New(errcode.ErrAuthKeyInvalid, "no PEM block found in public key data")
	}

	// Try PKIX first, then PKCS#1.
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err == nil {
		rsaKey, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, errcode.New(errcode.ErrAuthKeyInvalid, "PKIX key is not RSA")
		}
		if err := validateRSAKeySize(rsaKey.N.BitLen(), "public"); err != nil {
			return nil, err
		}
		return rsaKey, nil
	}

	rsaKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrAuthKeyInvalid, "failed to parse RSA public key", err)
	}
	if err := validateRSAKeySize(rsaKey.N.BitLen(), "public"); err != nil {
		return nil, err
	}
	return rsaKey, nil
}
