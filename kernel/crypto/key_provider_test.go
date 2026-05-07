package crypto_test

import (
	"context"
	"testing"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
)

// ---------------------------------------------------------------------------
// Compile-time contract assertions
// ---------------------------------------------------------------------------

// fakeProvider is a minimal KeyProvider for compile-time assertions only.
type fakeProvider struct{}

func (fakeProvider) Current(_ context.Context) (kcrypto.KeyHandle, error) { return fakeHandle{}, nil }
func (fakeProvider) ByID(_ context.Context, _ string) (kcrypto.KeyHandle, error) {
	return fakeHandle{}, nil
}
func (fakeProvider) Rotate(_ context.Context) (string, error) { return "", nil }

// fakeHandle is a minimal KeyHandle for compile-time assertions only.
type fakeHandle struct{}

func (fakeHandle) ID() string { return "fake-v1" }
func (fakeHandle) Encrypt(_ context.Context, _, _ []byte) (kcrypto.EncryptResult, error) {
	return kcrypto.EncryptResult{
		Ciphertext: []byte("ciphertext"),
		Nonce:      []byte("nonce"),
		EDK:        []byte("edk"),
		KeyID:      "fake-v1",
	}, nil
}
func (fakeHandle) Decrypt(_ context.Context, _, _, _, _ []byte) ([]byte, error) { return nil, nil }

// Compile-time contract checks — these fail at build time if the interfaces
// are not satisfied, mirroring the kernel/lifecycle and kernel/worker pattern.
var (
	_ kcrypto.KeyProvider = fakeProvider{}
	_ kcrypto.KeyHandle   = fakeHandle{}
)

// ---------------------------------------------------------------------------
// Method-call tests (verifying interface shape at runtime)
// ---------------------------------------------------------------------------

func TestKeyProvider_InterfaceMethods(t *testing.T) {
	ctx := context.Background()
	var p kcrypto.KeyProvider = fakeProvider{}

	_, err := p.Current(ctx)
	if err != nil {
		t.Fatalf("Current: unexpected error: %v", err)
	}

	_, err = p.ByID(ctx, "some-key")
	if err != nil {
		t.Fatalf("ByID: unexpected error: %v", err)
	}

	id, err := p.Rotate(ctx)
	if err != nil {
		t.Fatalf("Rotate: unexpected error: %v", err)
	}
	_ = id
}

func TestKeyHandle_InterfaceMethods(t *testing.T) {
	ctx := context.Background()
	var h kcrypto.KeyHandle = fakeHandle{}

	if h.ID() != "fake-v1" {
		t.Fatalf("ID: expected fake-v1, got %s", h.ID())
	}

	result, err := h.Encrypt(ctx, []byte("plain"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt: unexpected error: %v", err)
	}
	if result.KeyID == "" {
		t.Fatal("Encrypt: keyID must not be empty")
	}
	if len(result.Ciphertext) == 0 {
		t.Fatal("Encrypt: ciphertext must not be empty")
	}

	_, err = h.Decrypt(ctx, nil, nil, nil, []byte("aad"))
	if err != nil {
		t.Fatalf("Decrypt: unexpected error: %v", err)
	}
}
