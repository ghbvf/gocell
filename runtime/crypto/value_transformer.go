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

// keyProviderTransformer is the production ValueTransformer backed by a KeyProvider.
type keyProviderTransformer struct {
	provider KeyProvider
}

// NewValueTransformer returns a ValueTransformer backed by the given KeyProvider.
func NewValueTransformer(p KeyProvider) ValueTransformer {
	return &keyProviderTransformer{provider: p}
}

// Encrypt encrypts plaintext using the current KeyHandle.
func (t *keyProviderTransformer) Encrypt(ctx context.Context, plaintext, aad []byte) ([]byte, string, []byte, []byte, error) {
	handle, err := t.provider.Current(ctx)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("value-transformer: get current key: %w", err)
	}

	ct, nonce, edk, err := handle.Encrypt(ctx, plaintext, aad)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("value-transformer: encrypt: %w", err)
	}

	return ct, handle.ID(), nonce, edk, nil
}

// Decrypt decrypts ciphertext using the KeyHandle identified by keyID.
func (t *keyProviderTransformer) Decrypt(ctx context.Context, ciphertext []byte, keyID string, nonce, edk, aad []byte) ([]byte, error) {
	handle, err := t.provider.ByID(ctx, keyID)
	if err != nil {
		return nil, fmt.Errorf("value-transformer: resolve key %q: %w", keyID, err)
	}

	plaintext, err := handle.Decrypt(ctx, ciphertext, nonce, edk, aad)
	if err != nil {
		return nil, err // already wrapped with ErrKeyProviderDecryptFailed by handle
	}

	return plaintext, nil
}

// ---------------------------------------------------------------------------
// NoopTransformer — pass-through for sensitive=false values
// ---------------------------------------------------------------------------

// NoopTransformer implements ValueTransformer with identity (no-op) semantics.
// Used for sensitive=false config values that do not require encryption.
// Encrypt returns the plaintext unchanged; Decrypt returns ciphertext unchanged.
type NoopTransformer struct{}

// Encrypt returns plaintext as-is with empty keyID, nonce, and edk.
func (NoopTransformer) Encrypt(_ context.Context, plaintext, _ []byte) ([]byte, string, []byte, []byte, error) {
	return plaintext, "", nil, nil, nil
}

// Decrypt returns ciphertext as-is.
func (NoopTransformer) Decrypt(_ context.Context, ciphertext []byte, _ string, _, _, _ []byte) ([]byte, error) {
	return ciphertext, nil
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
