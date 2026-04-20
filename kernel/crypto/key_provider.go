package crypto

import (
	"context"
)

// KeyProvider abstracts a KMS backend. Implementations must be safe for
// concurrent use.
//
// ref: kubernetes/kubernetes staging/.../storage/value/transformer.go@master
type KeyProvider interface {
	// Current returns the active KeyHandle for encrypting new values.
	// The returned handle's ID() is stored alongside the ciphertext so that
	// the correct key can be looked up during decryption.
	Current(ctx context.Context) (KeyHandle, error)

	// ByID returns the KeyHandle identified by keyID. Callers use this to
	// decrypt values encrypted by a previous key version.
	// Returns ErrKeyNotFound when the key is absent from the keyring.
	ByID(ctx context.Context, keyID string) (KeyHandle, error)

	// Rotate generates or activates a new key version. The previous key
	// remains in the keyring so that existing ciphertexts can still be
	// decrypted. Returns the new key's ID.
	Rotate(ctx context.Context) (newKeyID string, err error)
}

// KeyHandle is a thin handle for a specific key version. It provides the
// cryptographic primitives needed by ValueTransformer.
//
// Contract:
//   - Encrypt MUST generate a fresh nonce on every call (nonce uniqueness).
//   - Decrypt MUST validate the aad; mismatched aad MUST return ErrDecryptFailed.
//   - nonce and edk semantics are provider-specific. VaultTransit may return
//     nil/empty slices because key material is managed server-side.
//   - KeyID MUST reflect the KEK version actually used to wrap the data/DEK;
//     for LocalAES this equals handle.ID(); for VaultTransit this is parsed
//     from the wrapped DEK prefix "vault:vN:".
//   - keyID is "verifiable metadata": callers SHOULD use MatchKeyID() to verify
//     that a stored keyID matches the handle used for decryption. This prevents
//     silent data corruption from misrouted key versions.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (keyID version prefix)
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go@master (EncryptResponse.KeyID)
type KeyHandle interface {
	// ID returns the key version identifier (e.g. "local-aes-v1", "vault-transit:v3").
	ID() string

	// Encrypt encrypts plaintext under this key using the provided aad as
	// Additional Authenticated Data.  Returns:
	//   - ciphertext: the encrypted payload (opaque bytes; may embed nonce for
	//     some backends like VaultTransit).
	//   - nonce:      random IV used for AES-GCM (nil for backends that embed it).
	//   - edk:        encrypted DEK for envelope encryption (nil for backends
	//     like VaultTransit that manage keys server-side).
	//   - keyID:      the KEK version identifier actually used at encrypt-time.
	//     Callers MUST persist this value alongside the ciphertext so that the
	//     correct key can be resolved during decryption.  Returning keyID from
	//     Encrypt (rather than reading handle.ID() after the call) eliminates
	//     the race between a Current() call and a key rotation in VaultTransit.
	//     Mirrors k8s KMS v2 EncryptResponse.KeyID semantics.
	//   - err:        non-nil on any encryption failure (fail-closed).
	Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, keyID string, err error)

	// Decrypt decrypts ciphertext encrypted by this key. The aad must match
	// exactly what was provided to Encrypt; mismatched aad returns ErrDecryptFailed.
	//
	// nonce and edk may be nil for backends that embed these within ciphertext.
	Decrypt(ctx context.Context, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error)
}
