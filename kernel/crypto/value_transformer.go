package crypto

import (
	"context"
	"fmt"
)

// ValueTransformer is the caller-facing encryption interface for sensitive
// config values. It is a thin wrapper over KeyProvider that:
//   - Uses the current key for Encrypt.
//   - Uses ByID(keyID) for Decrypt so historical keys remain accessible.
//   - Accepts pre-computed AAD so callers can bind ciphertext to the row's
//     identity (preventing ciphertext transplant attacks).
//
// Optional extension interface: implementations MAY also satisfy
// CurrentKeyIDProvider to expose the current key ID; this is used by the
// config repository to compute the Stale staleness signal for rotation-driven
// lazy re-encryption. NoopTransformer deliberately does not implement it
// (nothing to be stale against).
//
// ref: kubernetes/kubernetes staging/.../storage/value/transformer.go@master
type ValueTransformer interface {
	// Encrypt encrypts plaintext under the current key.
	// Returns (ciphertext, keyID, nonce, edk, error).
	// keyID identifies the key version; store alongside the ciphertext so that
	// Decrypt can resolve the correct historical key on read.
	Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext []byte, keyID string, nonce, edk []byte, err error)

	// Decrypt decrypts ciphertext using the key identified by keyID.
	// Fail-closed: returns an error on any decryption failure; never returns
	// the raw ciphertext or an empty slice as a fallback.
	Decrypt(ctx context.Context, ciphertext []byte, keyID string, nonce, edk, aad []byte) (plaintext []byte, err error)
}

// CurrentKeyIDProvider is the optional extension interface for ValueTransformer
// implementations that can report their current key ID. Discovered at runtime
// via type assertion (`if c, ok := tr.(CurrentKeyIDProvider); ok { ... }`).
// A nil error with an empty string is reserved for "no key" (e.g. Noop); a
// non-empty string represents the active key version label used for staleness
// comparison against per-row stored key IDs.
type CurrentKeyIDProvider interface {
	CurrentKeyID(ctx context.Context) (string, error)
}

// ---------------------------------------------------------------------------
// AAD helpers
// ---------------------------------------------------------------------------

// AADForConfig computes the Additional Authenticated Data for a config entry.
// Format: "cell:{cellID}/key:{configKey}"
//
// Using a composite key prevents a ciphertext encrypted for one config entry
// from being transplanted into a different entry (cross-row replay attack).
func AADForConfig(cellID, configKey string) []byte {
	return []byte(fmt.Sprintf("cell:%s/key:%s", cellID, configKey))
}
