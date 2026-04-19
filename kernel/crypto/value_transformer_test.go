package crypto_test

import (
	"context"
	"testing"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
)

// ---------------------------------------------------------------------------
// Compile-time contract assertions
// ---------------------------------------------------------------------------

// fakeTransformer satisfies ValueTransformer.
type fakeTransformer struct{}

func (fakeTransformer) Encrypt(_ context.Context, plaintext, _ []byte) ([]byte, string, []byte, []byte, error) {
	return plaintext, "fake-key-v1", nil, nil, nil
}
func (fakeTransformer) Decrypt(_ context.Context, ciphertext []byte, _ string, _, _, _ []byte) ([]byte, error) {
	return ciphertext, nil
}

// fakeCurrentKeyIDProvider satisfies CurrentKeyIDProvider.
type fakeCurrentKeyIDProvider struct{}

func (fakeCurrentKeyIDProvider) CurrentKeyID(_ context.Context) (string, error) {
	return "fake-key-v1", nil
}

var _ kcrypto.ValueTransformer = fakeTransformer{}
var _ kcrypto.CurrentKeyIDProvider = fakeCurrentKeyIDProvider{}

// ---------------------------------------------------------------------------
// ValueTransformer interface method tests
// ---------------------------------------------------------------------------

func TestValueTransformer_InterfaceMethods(t *testing.T) {
	ctx := context.Background()
	var tr kcrypto.ValueTransformer = fakeTransformer{}

	plaintext := []byte("secret-value")
	aad := []byte("cell:test-cell/key:test-key") // AADForConfig moved to cells/config-core/internal/crypto

	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: unexpected error: %v", err)
	}
	if keyID == "" {
		t.Fatal("Encrypt: keyID must not be empty")
	}

	recovered, err := tr.Decrypt(ctx, ct, keyID, nonce, edk, aad)
	if err != nil {
		t.Fatalf("Decrypt: unexpected error: %v", err)
	}
	if string(recovered) != string(plaintext) {
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
