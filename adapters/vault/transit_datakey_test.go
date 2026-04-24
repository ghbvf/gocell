package vault

// Envelope encrypt path tests — production Encrypt routes through
// /transit/datakey/plaintext (server-side DEK). The legacy /transit/encrypt
// path must not be invoked from production code.
//
// Reuses the shared fakeVaultClient from transit_provider_test.go (same
// package, same _test build tag); the fake's separate datakeyCalls /
// encryptCalls counters give the regression guard.

import (
	"context"
	"strings"
	"testing"
)

func TestEncrypt_UsesDataKeyPath(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 3}
	p := newTestProvider(t, fake)
	h := mustCurrent(t, p)

	_, _, edk, keyID, err := h.Encrypt(context.Background(), []byte("payload"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if got := fake.datakeyCalls.Load(); got != 1 {
		t.Errorf("datakey call count = %d, want 1", got)
	}
	if got := fake.encryptCalls.Load(); got != 0 {
		t.Errorf("legacy /encrypt path must not be called, got %d hits", got)
	}
	if fake.lastWritePath != "transit/datakey/plaintext/gocell-config" {
		t.Errorf("lastWritePath = %q, want transit/datakey/plaintext/gocell-config", fake.lastWritePath)
	}
	if bits := fake.lastWriteData["bits"]; bits != 256 {
		t.Errorf("datakey body bits = %v, want 256", bits)
	}
	if keyID != "vault-transit:v3" {
		t.Errorf("keyID = %q, want vault-transit:v3", keyID)
	}
	if !strings.HasPrefix(string(edk), "vault:v3:") {
		t.Errorf("edk prefix = %q, want vault:v3:", string(edk))
	}
}

func TestEncryptDecrypt_DataKeyRoundTrip(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 5}
	p := newTestProvider(t, fake)
	h := mustCurrent(t, p)

	plaintext := []byte("hello world")
	aad := []byte("row:42")

	ct, nonce, edk, _, err := h.Encrypt(context.Background(), plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := h.Decrypt(context.Background(), ct, nonce, edk, aad)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", string(got), string(plaintext))
	}
}
