package crypto_test

import (
	"bytes"
	"context"
	"testing"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
)

// ---------------------------------------------------------------------------
// Compile-time contract assertions
// ---------------------------------------------------------------------------

// fakeTransformer satisfies ValueTransformer.
type fakeTransformer struct{}

func (fakeTransformer) Encrypt(_ context.Context, plaintext, _ []byte) (kcrypto.EncryptResult, error) {
	return kcrypto.EncryptResult{
		Ciphertext: plaintext,
		Nonce:      []byte("nonce"),
		EDK:        []byte("edk"),
		KeyID:      "fake-key-v1",
	}, nil
}

func (fakeTransformer) Decrypt(_ context.Context, ciphertext []byte, _ string, _, _, _ []byte) ([]byte, error) {
	return ciphertext, nil
}

// fakeCurrentKeyIDProvider satisfies CurrentKeyIDProvider.
type fakeCurrentKeyIDProvider struct{}

func (fakeCurrentKeyIDProvider) CurrentKeyID(_ context.Context) (string, error) {
	return "fake-key-v1", nil
}

var (
	_ kcrypto.ValueTransformer     = fakeTransformer{}
	_ kcrypto.CurrentKeyIDProvider = fakeCurrentKeyIDProvider{}
)

// ---------------------------------------------------------------------------
// ValueTransformer interface method tests
// ---------------------------------------------------------------------------

func TestValueTransformer_InterfaceMethods(t *testing.T) {
	ctx := context.Background()
	var tr kcrypto.ValueTransformer = fakeTransformer{}

	plaintext := []byte("secret-value")
	aad := []byte("cell:test-cell/key:test-key") // AADForConfig moved to cells/configcore/internal/crypto

	result, err := tr.Encrypt(ctx, plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: unexpected error: %v", err)
	}
	if result.KeyID == "" {
		t.Fatal("Encrypt: keyID must not be empty")
	}

	recovered, err := tr.Decrypt(ctx, result.Ciphertext, result.KeyID, result.Nonce, result.EDK, aad)
	if err != nil {
		t.Fatalf("Decrypt: unexpected error: %v", err)
	}
	if !bytes.Equal(recovered, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", recovered, plaintext)
	}
}

// ---------------------------------------------------------------------------
// CurrentKeyIDProvider interface test
// ---------------------------------------------------------------------------

func TestCurrentKeyIDProvider_InterfaceMethods(t *testing.T) {
	ctx := context.Background()
	var p kcrypto.CurrentKeyIDProvider = fakeCurrentKeyIDProvider{}

	id, err := p.CurrentKeyID(ctx)
	if err != nil {
		t.Fatalf("CurrentKeyID: unexpected error: %v", err)
	}
	if id == "" {
		t.Fatal("CurrentKeyID: returned empty string unexpectedly")
	}
}
