package vault

// aead.go — local AES-GCM helpers for envelope encryption.
//
// These helpers are intentionally copied from runtime/crypto rather than
// imported, because adapters/vault must NOT depend on runtime/ (layer rule).
// The implementation is identical; any future changes must be kept in sync or
// extracted to pkg/aeadutil.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/aes/aes.go@master
// ref: hashicorp/vault vault/barrier_aes_gcm.go@main:L1199-L1233

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// aeadEncryptSplit encrypts plaintext with key and aad using AES-GCM-256.
// Returns (rawCiphertext, nonce, error). The nonce is NOT prepended to the
// ciphertext — it is returned separately so callers can store it in a
// dedicated column (value_nonce).
//
// ref: kubernetes/kubernetes aes/aes.go@master — Transformer.TransformToStorage
func aeadEncryptSplit(key, plaintext, aad []byte) (ciphertext, nonce []byte, err error) {
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

// aeadDecrypt decrypts rawCiphertext (not nonce-prefixed) using key, nonce, and aad.
// The aad must match exactly what was used in aeadEncryptSplit; any mismatch
// causes AES-GCM Open to return an authentication error.
//
// ref: kubernetes/kubernetes aes/aes.go@master — Transformer.TransformFromStorage
func aeadDecrypt(key, ciphertext, nonce, aad []byte) (plaintext []byte, err error) {
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
