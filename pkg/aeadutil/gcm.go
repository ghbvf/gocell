package aeadutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// EncryptGCM encrypts plaintext with key and aad using AES-GCM.
// Returns (ciphertext, nonce, error). The nonce is NOT prepended to the
// ciphertext — it is returned separately so callers can store it in a
// dedicated column (value_nonce). This matches the split-storage convention
// used by AWS S3 crypto and kubernetes/kubernetes kmsv2/envelope.go.
//
// The nonce is generated internally via crypto/rand; its length is derived
// from gcm.NonceSize() (standard AES-GCM = 12 bytes).
//
// ref: google/tink-go aead/subtle/aes_gcm.go — AEAD function signature
// ref: aws/aws-sdk-go s3crypto — split nonce/ciphertext storage
func EncryptGCM(key, plaintext, aad []byte) (ciphertext, nonce []byte, err error) {
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

// DecryptGCM decrypts rawCiphertext (not nonce-prefixed) using key, nonce, and aad.
// The aad must match exactly what was used in EncryptGCM; any mismatch causes
// AES-GCM authentication failure. Errors are sanitised — the message never
// contains key material or plaintext.
//
// ref: google/tink-go aead/subtle/aes_gcm.go
// ref: kubernetes/kubernetes kmsv2/envelope.go — Transformer.TransformFromStorage
func DecryptGCM(key, ciphertext, nonce, aad []byte) (plaintext []byte, err error) {
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

// EncryptGCMSelfContained encrypts plaintext and returns a nonce-prefixed
// self-contained blob: nonce || ciphertext. Suitable for storing a single
// opaque blob (e.g. wrapped DEK in value_edk) where a separate nonce column
// is inconvenient.
//
// ref: google/tink-go aead/subtle/aes_gcm.go — NewAESGCM prepends nonce
// ref: kubernetes/kubernetes kmsv2/envelope.go — self-contained wrapped DEK
func EncryptGCMSelfContained(key, plaintext, aad []byte) (blob []byte, err error) {
	ct, nonce, err := EncryptGCM(key, plaintext, aad)
	if err != nil {
		return nil, err
	}
	// Prepend nonce so the blob is self-contained: nonce || ciphertext.
	blob = make([]byte, len(nonce)+len(ct))
	copy(blob, nonce)
	copy(blob[len(nonce):], ct)
	return blob, nil
}

// DecryptGCMSelfContained decrypts a nonce-prefixed self-contained blob
// produced by EncryptGCMSelfContained.
//
// Returns an error containing "blob too short" if the blob is smaller than
// the AES-GCM nonce size.
//
// ref: google/tink-go aead/subtle/aes_gcm.go
func DecryptGCMSelfContained(key, blob, aad []byte) (plaintext []byte, err error) {
	// Use a temporary cipher to determine the nonce size before splitting.
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
		return nil, fmt.Errorf("aes-gcm: blob too short (len=%d, need>=%d)", len(blob), nonceSize)
	}
	nonce := blob[:nonceSize]
	ct := blob[nonceSize:]
	plaintext, err = gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm: open self-contained: %w", err)
	}
	return plaintext, nil
}
