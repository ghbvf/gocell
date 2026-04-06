package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

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
)

// ErrKeyMissing indicates a required JWT key environment variable is not set.
var ErrKeyMissing = errcode.Code("ERR_AUTH_KEY_MISSING")

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
