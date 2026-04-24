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
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
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

// TestEncryptDecrypt_DataKeyRoundTrip is the smoke-level happy-path round
// trip for the datakey envelope. Decrypt-side wire-format assertions
// (transit/decrypt path, ciphertext prefix, no AAD field on the wire) live
// in TestVaultTransitHandle_DecryptRoundTrip (TC-2) in transit_provider_test.go;
// keep them there so this file stays focused on the encrypt-path concern.
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

func TestEncrypt_MalformedDatakeyResponse(t *testing.T) {
	for _, tc := range malformedDatakeyResponseCases() {
		t.Run(tc.name, func(t *testing.T) {
			fake, h := newMalformedDatakeyHandle(t, tc.response)
			_, _, _, _, err := h.Encrypt(context.Background(), []byte("payload"), []byte("aad"))
			assertMalformedDatakeyError(t, err)
			assertDatakeyRequestOnly(t, fake)
		})
	}
}

type malformedDatakeyResponseCase struct {
	name     string
	response map[string]any
}

func malformedDatakeyResponseCases() []malformedDatakeyResponseCase {
	validDEK := []byte("0123456789abcdef0123456789abcdef")
	validPlaintext := base64.StdEncoding.EncodeToString(validDEK)
	validCiphertext := "vault:v3:" + base64.StdEncoding.EncodeToString(validDEK)

	return []malformedDatakeyResponseCase{
		{
			name:     "missing plaintext",
			response: map[string]any{"ciphertext": validCiphertext},
		},
		{
			name:     "non-string plaintext",
			response: map[string]any{"plaintext": 42, "ciphertext": validCiphertext},
		},
		{
			name:     "invalid plaintext base64",
			response: map[string]any{"plaintext": "not-base64!", "ciphertext": validCiphertext},
		},
		{
			name:     "missing ciphertext",
			response: map[string]any{"plaintext": validPlaintext},
		},
		{
			name:     "non-string ciphertext",
			response: map[string]any{"plaintext": validPlaintext, "ciphertext": 42},
		},
	}
}

func newMalformedDatakeyHandle(t *testing.T, response map[string]any) (*fakeVaultClientWithWriteOverride, *vaultTransitHandle) {
	t.Helper()
	fake := &fakeVaultClientWithWriteOverride{
		fakeVaultClient: fakeVaultClient{latestVersion: 3},
	}
	fake.writeFn = func(_ context.Context, path string, data map[string]any) (map[string]any, error) {
		fake.datakeyCalls.Add(1)
		fake.lastWritePath = path
		fake.lastWriteData = data
		return response, nil
	}
	p, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"))
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}
	return fake, mustCurrent(t, p)
}

func assertMalformedDatakeyError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Encrypt expected malformed datakey response error, got nil")
	}
	if !errChainHasCode(err, errcode.ErrKeyProviderEncryptFailed) {
		t.Errorf("error chain must contain ErrKeyProviderEncryptFailed; err=%v", err)
	}
	if errcode.IsTransient(err) {
		t.Errorf("malformed datakey response must be permanent, got transient err=%v", err)
	}
}

func assertDatakeyRequestOnly(t *testing.T, fake *fakeVaultClientWithWriteOverride) {
	t.Helper()
	if got := fake.datakeyCalls.Load(); got != 1 {
		t.Errorf("datakey call count = %d, want 1", got)
	}
	if fake.lastWritePath != "transit/datakey/plaintext/gocell-config" {
		t.Errorf("lastWritePath = %q, want transit/datakey/plaintext/gocell-config", fake.lastWritePath)
	}
	if bits := fake.lastWriteData["bits"]; bits != 256 {
		t.Errorf("datakey body bits = %v, want 256", bits)
	}
	if got := fake.encryptCalls.Load(); got != 0 {
		t.Errorf("legacy /encrypt path must not be called, got %d hits", got)
	}
}
