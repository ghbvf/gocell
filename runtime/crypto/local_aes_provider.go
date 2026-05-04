package crypto

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"sync"

	"github.com/ghbvf/gocell/pkg/aeadutil"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Key ID constants for the LocalAES two-version keyring.
const (
	// LocalAESCurrentKeyID is the version label for the current master key.
	LocalAESCurrentKeyID = "local-aes-v1"
	// LocalAESPreviousKeyID is the version label for the previous master key.
	LocalAESPreviousKeyID = "local-aes-v0"
)

// localAESHandle implements KeyHandle using AES-GCM-256 envelope encryption.
// Envelope model:
//   - A per-row 32-byte DEK is generated from crypto/rand.
//   - The DEK encrypts the plaintext (AES-GCM) → (ciphertext, nonce).
//   - The master KEK encrypts the DEK (AES-GCM) → edk.
//
// ref: hashicorp/vault vault/barrier_aes_gcm.go@main:L1199-L1233
type localAESHandle struct {
	id  string
	kek []byte // 32-byte master Key Encryption Key
}

// ID returns the key version identifier.
func (h *localAESHandle) ID() string { return h.id }

// Encrypt performs AES-GCM envelope encryption.
//
// Returns:
//   - ciphertext: AES-GCM(DEK, plaintext, aad) — raw GCM output (no nonce prefix)
//   - nonce:      random 12-byte IV for the data encryption; stored in value_nonce
//   - edk:        nonce-prefixed AES-GCM(KEK, DEK) — self-contained blob stored in value_edk
//   - keyID:      actual KEK version used (for LocalAES always equals h.ID(), but the
//     interface requires callers to persist the Encrypt-returned keyID to eliminate the
//     race between Current() and a key rotation in backends like VaultTransit)
//
// Note: DEK uses defer clear() to zeroize on function exit; defense-in-depth over Go GC.
//
// ref: kubernetes/kubernetes staging/.../kmsv2/envelope.go EncryptResponse.KeyID semantics
func (h *localAESHandle) Encrypt(_ context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, keyID string, err error) {
	// 1. Generate a fresh 32-byte DEK.
	dek := make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, dek); err != nil {
		return nil, nil, nil, "", errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
			"local-aes: generate DEK", err)
	}
	defer clear(dek) // zeroize DEK on function exit; defense-in-depth over Go GC

	// 2. Encrypt plaintext with DEK. Returns raw ciphertext + nonce separately.
	ciphertext, nonce, err = aeadutil.EncryptGCM(dek, plaintext, aad)
	if err != nil {
		return nil, nil, nil, "", errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
			"local-aes: encrypt value", err)
	}

	// 3. Encrypt DEK with KEK (no AAD). edk is a self-contained nonce-prefixed blob.
	edk, err = aeadutil.EncryptGCMSelfContained(h.kek, dek, nil)
	if err != nil {
		return nil, nil, nil, "", errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
			"local-aes: wrap DEK", err)
	}

	return ciphertext, nonce, edk, h.id, nil
}

// Decrypt reverses AES-GCM envelope encryption.
func (h *localAESHandle) Decrypt(_ context.Context, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error) {
	if len(nonce) == 0 || len(edk) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed, "local-aes: missing nonce or edk")
	}

	// 1. Unwrap DEK using KEK. edk is a self-contained nonce-prefixed blob.
	dek, err := aeadutil.DecryptGCMSelfContained(h.kek, edk, nil)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed, "local-aes: unwrap DEK", err)
	}
	defer clear(dek) // zeroize DEK on function exit; defense-in-depth over Go GC

	// 2. Decrypt value using DEK + stored nonce.
	plaintext, err = aeadutil.DecryptGCM(dek, ciphertext, nonce, aad)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed,
			"local-aes: decrypt value (AAD mismatch or tampered)", err)
	}

	return plaintext, nil
}

// ---------------------------------------------------------------------------
// LocalAESKeyProvider
// ---------------------------------------------------------------------------

// LocalAESKeyProvider implements KeyProvider using local AES-GCM-256 keys.
// Suitable for dev/CI; not recommended for production
// (use `adapters/vault.TransitKeyProvider` instead).
//
// Keys are supplied by the caller as hex/base64-encoded 32-byte strings.
// Use cmd/corebundle.LoadConfigCoreKeyProvider to read per-cell env variables
// and pass the result to NewLocalAESKeyProviderFromKeys.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 — keyring with
// current + historical versions.
type LocalAESKeyProvider struct {
	mu      sync.RWMutex
	keyring map[string]*localAESHandle
	current string // ID of the active key
}

// NewLocalAESKeyProviderFromKeys constructs a LocalAESKeyProvider from the
// supplied hex/base64 encoded 32-byte keys. prevKey may be empty (no previous
// key in keyring). Both keys must decode to exactly 32 bytes.
//
// currentKey is the active master KEK (required; must be non-empty).
// prevKey enables decryption of values encrypted with a prior key version
// (supports rotation); may be empty for single-key mode.
func NewLocalAESKeyProviderFromKeys(currentKey, prevKey string) (*LocalAESKeyProvider, error) {
	if currentKey == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing,
			"local-aes: master key is required for encryption (set a 32-byte hex/base64 key)")
	}

	curBytes, err := decodeKey(currentKey)
	if err != nil {
		return nil, fmt.Errorf("local-aes: parse master key: %w", err)
	}

	p := &LocalAESKeyProvider{
		keyring: make(map[string]*localAESHandle),
		current: LocalAESCurrentKeyID,
	}
	p.keyring[LocalAESCurrentKeyID] = &localAESHandle{id: LocalAESCurrentKeyID, kek: curBytes}

	if prevKey != "" {
		prevBytes, err := decodeKey(prevKey)
		if err != nil {
			return nil, fmt.Errorf("local-aes: parse previous master key: %w", err)
		}
		p.keyring[LocalAESPreviousKeyID] = &localAESHandle{id: LocalAESPreviousKeyID, kek: prevBytes}
	}

	return p, nil
}

// Current returns the active KeyHandle.
func (p *LocalAESKeyProvider) Current(_ context.Context) (KeyHandle, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	h, ok := p.keyring[p.current]
	if !ok {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound, "local-aes: current key not found in keyring")
	}
	return h, nil
}

// ByID returns the KeyHandle for the given key ID. Returns ErrKeyNotFound
// when the ID is absent from the keyring.
func (p *LocalAESKeyProvider) ByID(_ context.Context, keyID string) (KeyHandle, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	h, ok := p.keyring[keyID]
	if !ok {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound, "local-aes: key not found: "+keyID)
	}
	return h, nil
}

// Rotate is not supported for LocalAESKeyProvider in production use.
//
// LocalAES key rotation is not persistent: a new in-memory key is lost on
// restart, causing all previously encrypted values to become unreadable.
// This method returns ErrNotImplemented so that callers receive an explicit
// error rather than silently losing data.
//
// Production rotation strategy (S14a):
//   - Use adapters/vault.TransitKeyProvider.Rotate() which delegates key generation to Vault.
//   - Vault persists key versions server-side; historical ciphertext remains
//     decryptable via the ciphertext version prefix.
//
// Testing rotation scenarios: use adapters/vault.TransitKeyProvider with a fake VaultClient
// (see adapters/vault/transit_provider_test.go).
func (p *LocalAESKeyProvider) Rotate(_ context.Context) (string, error) {
	return "", errcode.New(errcode.KindNotImplemented, errcode.ErrNotImplemented,
		"LocalAES rotation is not persistent; use adapters/vault.TransitKeyProvider for production key rotation (S14a)")
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// Key decoding
// ---------------------------------------------------------------------------

// decodeKey decodes a hex or base64-encoded 32-byte AES key.
func decodeKey(s string) ([]byte, error) {
	// Try hex first (64 hex chars = 32 bytes).
	if len(s) == 64 {
		b, err := hex.DecodeString(s)
		if err == nil && len(b) == 32 {
			return b, nil
		}
	}
	// Try base64 (standard or URL-safe).
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.URLEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, fmt.Errorf("local-aes: key must be 64-char hex or base64 encoding of 32 bytes")
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("local-aes: decoded key length %d, want 32 bytes", len(b))
	}
	return b, nil
}
