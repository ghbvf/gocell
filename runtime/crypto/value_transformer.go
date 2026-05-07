package crypto

import (
	"context"
	"fmt"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
)

// ValueTransformer is a type alias for the kernel ValueTransformer interface.
// The authoritative definition lives in kernel/crypto.
//
// This is not a migration shim — the alias exists so runtime/crypto
// implementations (keyProviderTransformer, NoopTransformer)
// type-check against the kernel contract without importing kernel/crypto
// from every local impl file.
//
// Guidance for new consumers: code in cells/ or cmd/ referencing only
// interfaces SHOULD import kernel/crypto directly and reference
// kcrypto.ValueTransformer (the kernel contract).
type ValueTransformer = kcrypto.ValueTransformer

// CurrentKeyIDProvider is a type alias for the kernel CurrentKeyIDProvider
// interface. The authoritative definition lives in kernel/crypto.
//
// See ValueTransformer alias comment for guidance on import choices.
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
func (t *keyProviderTransformer) Encrypt(ctx context.Context, plaintext, aad []byte) (EncryptResult, error) {
	handle, err := t.provider.Current(ctx)
	if err != nil {
		return EncryptResult{}, fmt.Errorf("value-transformer: get current key: %w", err)
	}

	result, err := handle.Encrypt(ctx, plaintext, aad)
	if err != nil {
		return EncryptResult{}, fmt.Errorf("value-transformer: encrypt: %w", err)
	}

	return result, nil
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

	// Defense-in-depth: verify that the provider returned a handle whose ID
	// matches the requested keyID. A mismatch indicates a buggy KeyProvider
	// implementation that routed the lookup to the wrong key — permanent error.
	if err := kcrypto.MatchKeyID(handle.ID(), keyID); err != nil {
		return nil, fmt.Errorf("value-transformer: provider returned handle id %q for requested keyID %q: %w",
			handle.ID(), keyID, err)
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
func (NoopTransformer) Encrypt(_ context.Context, plaintext, _ []byte) (EncryptResult, error) {
	return EncryptResult{Ciphertext: plaintext}, nil
}

// Decrypt returns ciphertext as-is.
func (NoopTransformer) Decrypt(_ context.Context, ciphertext []byte, _ string, _, _, _ []byte) ([]byte, error) {
	return ciphertext, nil
}
