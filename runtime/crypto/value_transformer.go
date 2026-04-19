package crypto

import (
	"context"
	"fmt"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
)

// ValueTransformer is a type alias for kernel/crypto.ValueTransformer.
// The caller-facing encryption interface for sensitive config values.
type ValueTransformer = kcrypto.ValueTransformer

// CurrentKeyIDProvider is a type alias for kernel/crypto.CurrentKeyIDProvider.
// Optional extension interface for ValueTransformer implementations that can
// report their current key ID.
type CurrentKeyIDProvider = kcrypto.CurrentKeyIDProvider

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

// CurrentKeyID returns the ID of the currently active key. Used by the config
// repository to compute the staleness signal (Stale=true when stored keyID
// differs from current).
func (t *keyProviderTransformer) CurrentKeyID(ctx context.Context) (string, error) {
	h, err := t.provider.Current(ctx)
	if err != nil {
		return "", err
	}
	return h.ID(), nil
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
