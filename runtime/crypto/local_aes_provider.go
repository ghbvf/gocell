package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"

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
func (h *localAESHandle) Encrypt(_ context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, err error) {
	// 1. Generate a fresh 32-byte DEK.
	dek := make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, dek); err != nil {
		return nil, nil, nil, fmt.Errorf("local-aes: generate DEK: %w", err)
	}

	// 2. Encrypt plaintext with DEK. Returns raw ciphertext + nonce separately.
	ciphertext, nonce, err = aesGCMEncryptSplit(dek, plaintext, aad)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("local-aes: encrypt value: %w", err)
	}

	// 3. Encrypt DEK with KEK (no AAD). edk is a self-contained nonce-prefixed blob.
	edk, err = aesGCMEncryptSelfContained(h.kek, dek, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("local-aes: wrap DEK: %w", err)
	}

	return ciphertext, nonce, edk, nil
}

// Decrypt reverses AES-GCM envelope encryption.
func (h *localAESHandle) Decrypt(_ context.Context, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error) {
	if len(nonce) == 0 || len(edk) == 0 {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed, "local-aes: missing nonce or edk")
	}

	// 1. Unwrap DEK using KEK. edk is a self-contained nonce-prefixed blob.
	dek, err := aesGCMDecryptSelfContained(h.kek, edk, nil)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed, "local-aes: unwrap DEK", err)
	}

	// 2. Decrypt value using DEK + stored nonce.
	plaintext, err = aesGCMDecrypt(dek, ciphertext, nonce, aad)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed, "local-aes: decrypt value (AAD mismatch or tampered)", err)
	}

	return plaintext, nil
}

// ---------------------------------------------------------------------------
// LocalAESKeyProvider
// ---------------------------------------------------------------------------

// LocalAESKeyProvider implements KeyProvider using local AES-GCM-256 keys
// loaded from environment variables. Suitable for dev/CI; not recommended for
// production (use VaultTransitKeyProvider instead).
//
// Master KEK loading:
//   - GOCELL_MASTER_KEY:          required in postgres mode (32B hex or base64).
//   - GOCELL_MASTER_KEY_PREVIOUS: optional; enables decryption of values
//     encrypted with the prior key version (supports rotation).
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 — keyring with
// current + historical versions.
type LocalAESKeyProvider struct {
	mu      sync.RWMutex
	keyring map[string]*localAESHandle
	current string // ID of the active key
	rotSeq  int    // rotation counter used to generate unique post-Rotate IDs
}

// NewLocalAESKeyProviderFromEnv constructs a LocalAESKeyProvider from the
// standard environment variables GOCELL_MASTER_KEY and optionally
// GOCELL_MASTER_KEY_PREVIOUS. Returns ErrConfigKeyMissing if the primary key
// is absent.
func NewLocalAESKeyProviderFromEnv() (*LocalAESKeyProvider, error) {
	return NewLocalAESKeyProviderFromKeys(
		os.Getenv("GOCELL_MASTER_KEY"),
		os.Getenv("GOCELL_MASTER_KEY_PREVIOUS"),
	)
}

// NewLocalAESKeyProviderFromKeys constructs a LocalAESKeyProvider from the
// supplied hex/base64 encoded 32-byte keys. prevKey may be empty (no previous
// key in keyring). Both keys must decode to exactly 32 bytes.
//
// This constructor is the canonical entry point for tests; production code
// calls NewLocalAESKeyProviderFromEnv.
func NewLocalAESKeyProviderFromKeys(currentKey, prevKey string) (*LocalAESKeyProvider, error) {
	if currentKey == "" {
		return nil, errcode.New(errcode.ErrConfigKeyMissing,
			"local-aes: GOCELL_MASTER_KEY is required for encryption (set a 32-byte hex/base64 key)")
	}

	curBytes, err := decodeKey(currentKey)
	if err != nil {
		return nil, fmt.Errorf("local-aes: parse GOCELL_MASTER_KEY: %w", err)
	}

	p := &LocalAESKeyProvider{
		keyring: make(map[string]*localAESHandle),
		current: LocalAESCurrentKeyID,
	}
	p.keyring[LocalAESCurrentKeyID] = &localAESHandle{id: LocalAESCurrentKeyID, kek: curBytes}

	if prevKey != "" {
		prevBytes, err := decodeKey(prevKey)
		if err != nil {
			return nil, fmt.Errorf("local-aes: parse GOCELL_MASTER_KEY_PREVIOUS: %w", err)
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
		return nil, errcode.New(errcode.ErrKeyProviderKeyNotFound, "local-aes: current key not found in keyring")
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
		return nil, errcode.New(errcode.ErrKeyProviderKeyNotFound, "local-aes: key not found: "+keyID)
	}
	return h, nil
}

// Rotate generates a new random 32-byte KEK, adds it to the keyring, and
// makes it current. The previous current key is retained in the keyring for
// backward-compatible decryption.
//
// Note: for production rotation, prefer VaultTransitKeyProvider.Rotate() which
// delegates key generation to Vault.
func (p *LocalAESKeyProvider) Rotate(_ context.Context) (string, error) {
	newKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, newKey); err != nil {
		return "", fmt.Errorf("local-aes: generate new key: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.rotSeq++
	newID := fmt.Sprintf("local-aes-rotated-v%d", p.rotSeq)
	p.keyring[newID] = &localAESHandle{id: newID, kek: newKey}
	p.current = newID
	return newID, nil
}

// ---------------------------------------------------------------------------
// AES-GCM helpers
// ---------------------------------------------------------------------------

// aesGCMEncryptSplit encrypts plaintext with key+aad using AES-GCM-256.
// Returns (rawCiphertext, nonce, error). rawCiphertext does NOT include the
// nonce — the nonce is returned separately and should be stored in value_nonce.
func aesGCMEncryptSplit(key, plaintext, aad []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("aes-gcm: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("aes-gcm: new GCM: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("aes-gcm: generate nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, aad)
	return ciphertext, nonce, nil
}

// aesGCMDecrypt decrypts rawCiphertext (not nonce-prefixed) with key+nonce+aad.
func aesGCMDecrypt(key, ciphertext, nonce, aad []byte) (plaintext []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: new GCM: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("aes-gcm: nonce length %d, want %d", len(nonce), gcm.NonceSize())
	}
	plaintext, err = gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: open: %w", err)
	}
	return plaintext, nil
}

// aesGCMEncryptSelfContained encrypts plaintext and returns a nonce-prefixed
// self-contained blob: nonce || ciphertext. Used for edk (wrapped DEK) so
// the blob is self-sufficient when stored in value_edk.
func aesGCMEncryptSelfContained(key, plaintext, aad []byte) ([]byte, error) {
	ct, nonce, err := aesGCMEncryptSplit(key, plaintext, aad)
	if err != nil {
		return nil, err
	}
	// Prepend nonce so the blob is self-contained.
	blob := make([]byte, len(nonce)+len(ct))
	copy(blob, nonce)
	copy(blob[len(nonce):], ct)
	return blob, nil
}

// aesGCMDecryptSelfContained decrypts a nonce-prefixed self-contained blob
// produced by aesGCMEncryptSelfContained.
func aesGCMDecryptSelfContained(key, blob, aad []byte) ([]byte, error) {
	// We need the nonce size to split the blob. Use a temporary cipher to get it.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: new GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(blob) < nonceSize {
		return nil, fmt.Errorf("aes-gcm: self-contained blob too short (len=%d)", len(blob))
	}
	nonce := blob[:nonceSize]
	ct := blob[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: open self-contained: %w", err)
	}
	return plaintext, nil
}

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
