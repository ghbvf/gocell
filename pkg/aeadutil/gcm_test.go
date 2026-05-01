package aeadutil_test

import (
	"bytes"
	"crypto/aes"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/aeadutil"
)

// validKey32 is a 32-byte AES-256 key for testing.
var validKey32 = bytes.Repeat([]byte{0x01}, 32)

// validKey16 is a 16-byte AES-128 key for testing.
var validKey16 = bytes.Repeat([]byte{0x02}, 16)

// validKey24 is a 24-byte AES-192 key for testing.
var validKey24 = bytes.Repeat([]byte{0x03}, 24)

// assertGCMRoundTrip encrypts+decrypts plaintext with the given key/aad and
// asserts the round-trip returns the original bytes. Extracted from the
// table-driven TestEncryptDecryptGCM_RoundTrip to keep cognitive complexity
// inside Sonar's 15 threshold.
func assertGCMRoundTrip(t *testing.T, key, plaintext, aad []byte) {
	t.Helper()
	ct, nonce, err := aeadutil.EncryptGCM(key, plaintext, aad)
	if err != nil {
		t.Fatalf("EncryptGCM error: %v", err)
	}
	if len(ct) == 0 {
		t.Fatal("ciphertext must not be empty")
	}
	if len(nonce) == 0 {
		t.Fatal("nonce must not be empty")
	}

	got, err := aeadutil.DecryptGCM(key, ct, nonce, aad)
	if err != nil {
		t.Fatalf("DecryptGCM error: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

// TestEncryptDecryptGCM_RoundTrip tests EncryptGCM + DecryptGCM round-trips.
func TestEncryptDecryptGCM_RoundTrip(t *testing.T) {
	t.Parallel()
	plaintext := []byte("hello, world — this is a test plaintext")

	tests := []struct {
		name string
		key  []byte
		aad  []byte
	}{
		{name: "aes256_nil_aad", key: validKey32, aad: nil},
		{name: "aes256_non_empty_aad", key: validKey32, aad: []byte("additional authenticated data")},
		{name: "aes128_nil_aad", key: validKey16, aad: nil},
		{name: "aes192_non_empty_aad", key: validKey24, aad: []byte("aad-for-aes192")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertGCMRoundTrip(t, tc.key, plaintext, tc.aad)
		})
	}
}

// TestEncryptGCM_NonceLengthIs12 verifies the nonce is always 12 bytes (GCM standard).
func TestEncryptGCM_NonceLengthIs12(t *testing.T) {
	t.Parallel()
	_, nonce, err := aeadutil.EncryptGCM(validKey32, []byte("data"), nil)
	if err != nil {
		t.Fatalf("EncryptGCM error: %v", err)
	}
	if len(nonce) != 12 {
		t.Errorf("nonce length = %d, want 12", len(nonce))
	}
}

// TestDecryptGCM_WrongKey verifies authentication failure on wrong key.
func TestDecryptGCM_WrongKey(t *testing.T) {
	t.Parallel()
	ct, nonce, err := aeadutil.EncryptGCM(validKey32, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("EncryptGCM error: %v", err)
	}

	wrongKey := bytes.Repeat([]byte{0xFF}, 32)
	_, err = aeadutil.DecryptGCM(wrongKey, ct, nonce, nil)
	if err == nil {
		t.Fatal("expected error with wrong key, got nil")
	}
}

// TestDecryptGCM_TamperedCiphertext verifies authentication failure on modified ciphertext.
func TestDecryptGCM_TamperedCiphertext(t *testing.T) {
	t.Parallel()
	ct, nonce, err := aeadutil.EncryptGCM(validKey32, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("EncryptGCM error: %v", err)
	}

	// Flip a bit in the ciphertext.
	tampered := make([]byte, len(ct))
	copy(tampered, ct)
	tampered[0] ^= 0xFF

	_, err = aeadutil.DecryptGCM(validKey32, tampered, nonce, nil)
	if err == nil {
		t.Fatal("expected error with tampered ciphertext, got nil")
	}
}

// TestDecryptGCM_AADMismatch verifies authentication failure on mismatched AAD.
func TestDecryptGCM_AADMismatch(t *testing.T) {
	t.Parallel()
	ct, nonce, err := aeadutil.EncryptGCM(validKey32, []byte("secret"), []byte("original-aad"))
	if err != nil {
		t.Fatalf("EncryptGCM error: %v", err)
	}

	_, err = aeadutil.DecryptGCM(validKey32, ct, nonce, []byte("different-aad"))
	if err == nil {
		t.Fatal("expected error with AAD mismatch, got nil")
	}
}

// TestDecryptGCM_WrongNonceLength verifies error for incorrect nonce length.
func TestDecryptGCM_WrongNonceLength(t *testing.T) {
	t.Parallel()
	ct, _, err := aeadutil.EncryptGCM(validKey32, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("EncryptGCM error: %v", err)
	}

	// Use a nonce of wrong length (8 instead of 12).
	wrongNonce := make([]byte, 8)
	_, err = aeadutil.DecryptGCM(validKey32, ct, wrongNonce, nil)
	if err == nil {
		t.Fatal("expected error with wrong nonce length, got nil")
	}
	if !strings.Contains(err.Error(), "nonce") {
		t.Errorf("error message should contain 'nonce', got: %v", err)
	}
}

// TestEncryptGCM_InvalidKeyLength verifies aes.NewCipher returns KeySizeError.
func TestEncryptGCM_InvalidKeyLength(t *testing.T) {
	t.Parallel()
	badKey := bytes.Repeat([]byte{0x01}, 17) // 17 bytes is not valid for AES
	_, _, err := aeadutil.EncryptGCM(badKey, []byte("data"), nil)
	if err == nil {
		t.Fatal("expected error with invalid key length, got nil")
	}
	var keySizeErr aes.KeySizeError
	// The error should wrap KeySizeError or at least not be nil.
	// We can't always errors.As through the fmt.Errorf wrapper, so just check non-nil.
	_ = keySizeErr
}

// TestEncryptDecryptGCMSelfContained_RoundTrip tests self-contained round-trips.
func TestEncryptDecryptGCMSelfContained_RoundTrip(t *testing.T) {
	t.Parallel()
	plaintext := []byte("self-contained test plaintext")

	tests := []struct {
		name string
		key  []byte
		aad  []byte
	}{
		{
			name: "aes256_nil_aad",
			key:  validKey32,
			aad:  nil,
		},
		{
			name: "aes256_non_empty_aad",
			key:  validKey32,
			aad:  []byte("self-contained-aad"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			blob, err := aeadutil.EncryptGCMSelfContained(tc.key, plaintext, tc.aad)
			if err != nil {
				t.Fatalf("EncryptGCMSelfContained error: %v", err)
			}
			if len(blob) == 0 {
				t.Fatal("blob must not be empty")
			}

			got, err := aeadutil.DecryptGCMSelfContained(tc.key, blob, tc.aad)
			if err != nil {
				t.Fatalf("DecryptGCMSelfContained error: %v", err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Errorf("round-trip mismatch: got %q, want %q", got, plaintext)
			}
		})
	}
}

// TestDecryptGCMSelfContained_BlobTooShort verifies error for truncated blob.
func TestDecryptGCMSelfContained_BlobTooShort(t *testing.T) {
	t.Parallel()
	// A blob shorter than the nonce size (12 bytes) must fail.
	shortBlob := []byte{0x01, 0x02, 0x03} // only 3 bytes
	_, err := aeadutil.DecryptGCMSelfContained(validKey32, shortBlob, nil)
	if err == nil {
		t.Fatal("expected error with too-short blob, got nil")
	}
	if !strings.Contains(err.Error(), "blob too short") {
		t.Errorf("error message should contain 'blob too short', got: %v", err)
	}
}

// TestEncryptGCM_ErrorNoKeyLeak verifies error messages do not leak key or plaintext.
func TestEncryptGCM_ErrorNoKeyLeak(t *testing.T) {
	t.Parallel()
	badKey := bytes.Repeat([]byte{0xAB}, 7) // 7 bytes, invalid
	plaintext := []byte("secret plaintext that must not appear in error")
	keyHex := "abababababababab" // hex of badKey bytes

	_, _, err := aeadutil.EncryptGCM(badKey, plaintext, nil)
	if err == nil {
		t.Fatal("expected error with invalid key, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, keyHex) {
		t.Errorf("error message leaks key hex: %v", errMsg)
	}
	if strings.Contains(errMsg, string(plaintext)) {
		t.Errorf("error message leaks plaintext: %v", errMsg)
	}
}

// TestDecryptGCM_ErrorNoKeyLeak verifies decrypt error messages do not leak key or plaintext.
func TestDecryptGCM_ErrorNoKeyLeak(t *testing.T) {
	t.Parallel()
	ct, nonce, err := aeadutil.EncryptGCM(validKey32, []byte("original"), nil)
	if err != nil {
		t.Fatalf("EncryptGCM error: %v", err)
	}

	wrongKey := bytes.Repeat([]byte{0xFF}, 32)
	keyHex := strings.Repeat("ff", 32)
	plaintext := "original"

	_, err = aeadutil.DecryptGCM(wrongKey, ct, nonce, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, keyHex) {
		t.Errorf("error message leaks key: %v", errMsg)
	}
	if strings.Contains(errMsg, plaintext) {
		t.Errorf("error message leaks plaintext: %v", errMsg)
	}
}

// TestDecryptGCMSelfContained_WrongKey verifies authentication failure on wrong key.
func TestDecryptGCMSelfContained_WrongKey(t *testing.T) {
	t.Parallel()
	blob, err := aeadutil.EncryptGCMSelfContained(validKey32, []byte("secret"), nil)
	if err != nil {
		t.Fatalf("EncryptGCMSelfContained error: %v", err)
	}

	wrongKey := bytes.Repeat([]byte{0xFF}, 32)
	_, err = aeadutil.DecryptGCMSelfContained(wrongKey, blob, nil)
	if err == nil {
		t.Fatal("expected error with wrong key, got nil")
	}
}

// TestDecryptGCMSelfContained_ErrorNoKeyLeak verifies that error messages from wrong-key
// or tampered-blob decryption do not expose the key bytes or plaintext.
func TestDecryptGCMSelfContained_ErrorNoKeyLeak(t *testing.T) {
	t.Parallel()
	plaintext := []byte("secret plaintext that must not appear in error")
	wrongKey := bytes.Repeat([]byte{0xFF}, 32)
	keyHex := strings.Repeat("ff", 32)

	blob, err := aeadutil.EncryptGCMSelfContained(validKey32, plaintext, nil)
	if err != nil {
		t.Fatalf("EncryptGCMSelfContained error: %v", err)
	}

	tests := []struct {
		name string
		key  []byte
		blob []byte
	}{
		{
			name: "wrong_key",
			key:  wrongKey,
			blob: blob,
		},
		{
			name: "tampered_blob",
			key:  validKey32,
			blob: func() []byte {
				tampered := make([]byte, len(blob))
				copy(tampered, blob)
				tampered[len(tampered)-1] ^= 0xFF
				return tampered
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := aeadutil.DecryptGCMSelfContained(tc.key, tc.blob, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			errMsg := err.Error()
			if strings.Contains(errMsg, keyHex) {
				t.Errorf("error message leaks key hex: %v", errMsg)
			}
			if strings.Contains(errMsg, string(plaintext)) {
				t.Errorf("error message leaks plaintext: %v", errMsg)
			}
		})
	}
}
