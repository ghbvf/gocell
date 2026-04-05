package oidc

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// verifyRS256 verifies an RS256 (RSASSA-PKCS1-v1_5 with SHA-256) signature.
// signingInput is "base64url(header).base64url(payload)".
// signatureB64 is the base64url-encoded signature segment of the JWT.
func verifyRS256(signingInput, signatureB64 string, pub *rsa.PublicKey) error {
	sig, err := base64.RawURLEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("oidc: failed to decode signature: %w", err)
	}

	hash := sha256.Sum256([]byte(signingInput))

	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], sig); err != nil {
		return fmt.Errorf("oidc: RSA signature mismatch: %w", err)
	}
	return nil
}
