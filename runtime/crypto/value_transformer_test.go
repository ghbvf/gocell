package crypto_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// testAAD constructs a test AAD in the configcore format.
// AADForConfig was moved to cells/configcore/internal/crypto;
// runtime/crypto tests use this local helper to avoid a cells/ import.
func testAAD(configKey string) []byte {
	return []byte("cell:configcore/key:" + configKey)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestTransformer(t *testing.T) crypto.ValueTransformer {
	t.Helper()
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, "")
	require.NoError(t, err)
	return crypto.NewValueTransformer(p)
}

// ---------------------------------------------------------------------------
// TestValueTransformer_EncryptDecrypt_RoundTrip
// ---------------------------------------------------------------------------

func TestValueTransformer_EncryptDecrypt_RoundTrip(t *testing.T) {
	ctx := context.Background()
	tr := newTestTransformer(t)

	tests := []struct {
		name      string
		value     string
		configKey string
	}{
		{"api key", "sk-secretapikey1234", "api_key"},
		{"database password", "p@ssw0rd!", "db_password"},
		{"empty value", "", "empty_config"},
		{"unicode value", "秘密の値", "unicode_key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plaintext := []byte(tc.value)
			aad := testAAD(tc.configKey)

			ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
			require.NoError(t, err)
			assert.NotEmpty(t, keyID)
			assert.NotEmpty(t, nonce)
			assert.NotEmpty(t, edk)

			recovered, err := tr.Decrypt(ctx, ct, keyID, nonce, edk, aad)
			require.NoError(t, err)
			// AES-GCM Open on empty plaintext returns nil; normalise both.
			if len(recovered) == 0 {
				recovered = []byte{}
			}
			if len(plaintext) == 0 {
				plaintext = []byte{}
			}
			assert.Equal(t, plaintext, recovered)
		})
	}
}

// ---------------------------------------------------------------------------
// TestValueTransformer_DecryptByHistoricalKeyID
// ---------------------------------------------------------------------------

// TestValueTransformer_DecryptByHistoricalKeyID verifies that a value encrypted
// with the previous key (local-aes-v0) can still be decrypted by a provider
// that has validMasterKeyPrevious loaded as the "previous" key.
//
// Note: LocalAES.Rotate() is intentionally disabled (returns ErrNotImplemented).
// "Historical key" support is achieved by constructing a provider with both a
// current key and a previous key (caller-supplied via NewLocalAESKeyProviderFromKeys).
// The provider with both keys can decrypt values encrypted with either.
func TestValueTransformer_DecryptByHistoricalKeyID(t *testing.T) {
	ctx := context.Background()

	// Provider with both current and previous key loaded.
	// validMasterKey → "local-aes-v1" (current)
	// validMasterKeyPrevious → "local-aes-v0" (previous)
	p, err := crypto.NewLocalAESKeyProviderFromKeys(validMasterKey, validMasterKeyPrevious)
	require.NoError(t, err)

	// Obtain the previous key handle directly to encrypt with the old key.
	previousHandle, err := p.ByID(ctx, crypto.LocalAESPreviousKeyID)
	require.NoError(t, err)

	plaintext := []byte("old-secret")
	aad := testAAD("legacy_key")

	ct, nonce, edk, _, err := previousHandle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// The transformer should decrypt using the historical keyID.
	tr := crypto.NewValueTransformer(p)
	recovered, err := tr.Decrypt(ctx, ct, crypto.LocalAESPreviousKeyID, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestValueTransformer_DecryptWrongAAD_FailClosed
// ---------------------------------------------------------------------------

func TestValueTransformer_DecryptWrongAAD_FailClosed(t *testing.T) {
	ctx := context.Background()
	tr := newTestTransformer(t)

	plaintext := []byte("value")
	aad := testAAD("my_key")
	wrongAAD := testAAD("other_key")

	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	_, err = tr.Decrypt(ctx, ct, keyID, nonce, edk, wrongAAD)
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code)
}

// ---------------------------------------------------------------------------
// TestValueTransformer_UnknownKeyID_FailClosed
// ---------------------------------------------------------------------------

func TestValueTransformer_UnknownKeyID_FailClosed(t *testing.T) {
	ctx := context.Background()
	tr := newTestTransformer(t)

	plaintext := []byte("value")
	aad := testAAD("my_key")
	ct, _, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	_, err = tr.Decrypt(ctx, ct, "nonexistent-key-id", nonce, edk, aad)
	require.Error(t, err, "unknown keyID must return an error (fail-closed)")
}

// ---------------------------------------------------------------------------
// TestNoopTransformer_Passthrough
// ---------------------------------------------------------------------------

func TestNoopTransformer_Passthrough(t *testing.T) {
	ctx := context.Background()
	tr := crypto.NoopTransformer{}

	plaintext := []byte("public-config-value")
	aad := testAAD("public_key")

	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, ct)
	assert.Empty(t, keyID)
	assert.Nil(t, nonce)
	assert.Nil(t, edk)

	recovered, err := tr.Decrypt(ctx, ct, keyID, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TestAADForConfig_Format — local helper format test
// ---------------------------------------------------------------------------

// TestTestAAD_Format verifies the local testAAD helper used by this test file
// produces the expected configcore AAD format.
// The canonical AADForConfig implementation lives in
// cells/configcore/internal/crypto.
func TestTestAAD_Format(t *testing.T) {
	aad := testAAD("api_key")
	assert.Equal(t, []byte("cell:configcore/key:api_key"), aad)
}

// ---------------------------------------------------------------------------
// ValueTransformer error paths (value_transformer.go uncovered branches)
// ---------------------------------------------------------------------------

// errorKeyProvider is a KeyProvider that always returns the configured error.
type errorKeyProvider struct {
	currentErr error
	byIDErr    error
}

func (p *errorKeyProvider) Current(_ context.Context) (crypto.KeyHandle, error) {
	return nil, p.currentErr
}

func (p *errorKeyProvider) ByID(_ context.Context, _ string) (crypto.KeyHandle, error) {
	return nil, p.byIDErr
}

func (p *errorKeyProvider) Rotate(_ context.Context) (string, error) {
	return "", nil
}

// errorKeyHandle is a KeyHandle whose Encrypt always returns the configured error.
type errorKeyHandle struct {
	encryptErr error
}

func (h *errorKeyHandle) ID() string { return "error-key" }

func (h *errorKeyHandle) Encrypt(_ context.Context, _, _ []byte) ([]byte, []byte, []byte, string, error) {
	return nil, nil, nil, "", h.encryptErr
}

func (h *errorKeyHandle) Decrypt(_ context.Context, _, _, _, _ []byte) ([]byte, error) {
	return nil, nil
}

// singleHandleProvider returns the given handle for Current().
type singleHandleProvider struct {
	handle crypto.KeyHandle
}

func (p *singleHandleProvider) Current(_ context.Context) (crypto.KeyHandle, error) {
	return p.handle, nil
}

func (p *singleHandleProvider) ByID(_ context.Context, _ string) (crypto.KeyHandle, error) {
	if p.handle == nil {
		return nil, errors.New("test provider: missing handle")
	}
	return p.handle, nil
}

func (p *singleHandleProvider) Rotate(_ context.Context) (string, error) {
	return "", nil
}

// TestValueTransformer_Encrypt_CurrentKeyError verifies that when
// provider.Current returns an error, Encrypt surfaces it.
func TestValueTransformer_Encrypt_CurrentKeyError(t *testing.T) {
	ctx := context.Background()
	p := &errorKeyProvider{currentErr: errors.New("current key unavailable")}
	tr := crypto.NewValueTransformer(p)

	_, _, _, _, err := tr.Encrypt(ctx, []byte("value"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get current key")
}

// TestValueTransformer_Encrypt_HandleEncryptError verifies that when
// handle.Encrypt returns an error, Encrypt surfaces it.
func TestValueTransformer_Encrypt_HandleEncryptError(t *testing.T) {
	ctx := context.Background()
	encErr := &errcode.Error{Code: errcode.ErrKeyProviderEncryptFailed, Message: "cipher error"}
	p := &singleHandleProvider{handle: &errorKeyHandle{encryptErr: encErr}}
	tr := crypto.NewValueTransformer(p)

	_, _, _, _, err := tr.Encrypt(ctx, []byte("value"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypt")
}

// TestValueTransformer_Decrypt_ByIDError verifies that when provider.ByID returns
// an error, Decrypt surfaces it.
func TestValueTransformer_Decrypt_ByIDError(t *testing.T) {
	ctx := context.Background()
	p := &errorKeyProvider{byIDErr: errors.New("key not found")}
	tr := crypto.NewValueTransformer(p)

	_, err := tr.Decrypt(ctx, []byte("ct"), "missing-key", nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve key")
}

// ---------------------------------------------------------------------------
// Fake KeyHandle/KeyProvider for Phase 1-d tests (5-return-value interface)
// ---------------------------------------------------------------------------

// fixedKeyIDHandle implements kernel/crypto.KeyHandle for TC-VT-1.
// Its ID() returns handleID, but Encrypt returns encryptKeyID — allowing
// TC-VT-1 to verify that the transformer uses the Encrypt-returned keyID,
// not handle.ID().
type fixedKeyIDHandle struct {
	handleID     string
	encryptKeyID string
}

func (h *fixedKeyIDHandle) ID() string { return h.handleID }

// Encrypt returns a trivial ciphertext with encryptKeyID as keyID.
// The ciphertext is just plaintext XOR'd with 0x55 (dummy cipher — correctness
// is not the concern of TC-VT-1; keyID routing is).
// Implements kernel/crypto.KeyHandle with the Phase 0-b 5-return-value signature.
func (h *fixedKeyIDHandle) Encrypt(_ context.Context, plaintext, _ []byte) (ciphertext, nonce, edk []byte, keyID string, err error) {
	ct := make([]byte, len(plaintext))
	for i, b := range plaintext {
		ct[i] = b ^ 0x55
	}
	// Return encryptKeyID as the authoritative keyID (may differ from h.handleID).
	return ct, []byte("nonce"), []byte("edk"), h.encryptKeyID, nil
}

// Decrypt reverses fixedKeyIDHandle.Encrypt.
func (h *fixedKeyIDHandle) Decrypt(_ context.Context, ciphertext, _, _, _ []byte) ([]byte, error) {
	pt := make([]byte, len(ciphertext))
	for i, b := range ciphertext {
		pt[i] = b ^ 0x55
	}
	return pt, nil
}

// transientErrorHandle implements kernel/crypto.KeyHandle for TC-VT-2.
// Its Encrypt returns ErrKeyProviderTransient so the transformer propagates it.
type transientErrorHandle struct{}

func (h *transientErrorHandle) ID() string { return "transient-key" }

// Encrypt returns ErrKeyProviderTransient — simulating a Vault 503 or network timeout.
// Implements kernel/crypto.KeyHandle with the Phase 0-b 5-return-value signature.
func (h *transientErrorHandle) Encrypt(_ context.Context, _, _ []byte) ([]byte, []byte, []byte, string, error) {
	return nil, nil, nil, "", errcode.New(errcode.KindUnavailable, errcode.ErrKeyProviderTransient,
		"vault: service unavailable (503)")
}

func (h *transientErrorHandle) Decrypt(_ context.Context, _, _, _, _ []byte) ([]byte, error) {
	return nil, nil
}

// fixedHandleProvider is a minimal KeyProvider that always returns the same handle.
// Used by TC-VT-1 and TC-VT-2 to inject controlled fake handles.
type fixedHandleProvider struct {
	handle crypto.KeyHandle
}

func (p *fixedHandleProvider) Current(_ context.Context) (crypto.KeyHandle, error) {
	return p.handle, nil
}

func (p *fixedHandleProvider) ByID(_ context.Context, _ string) (crypto.KeyHandle, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound, "ByID not needed in this test")
}

func (p *fixedHandleProvider) Rotate(_ context.Context) (string, error) {
	return "", nil
}

// ---------------------------------------------------------------------------
// TC-VT-1: TestKeyProviderTransformer_UsesEncryptReturnedKeyID_NotHandleID
// Phase 1-d — TDD RED
// ---------------------------------------------------------------------------

// TestKeyProviderTransformer_UsesEncryptReturnedKeyID_NotHandleID is the core
// verification for the Phase 0-b keyID race fix: the transformer must use the
// keyID returned by handle.Encrypt (encrypt-time), not handle.ID() (pre-call).
//
// Setup:
//   - fixedKeyIDHandle.ID()          == "fake-handle-id"
//   - fixedKeyIDHandle.Encrypt(...)   returns keyID == "actual-used-keyID"
//
// Expected behavior after Phase 2-d:
//   - EncryptedPayload.KeyID == "actual-used-keyID"  (from Encrypt return)
//   - EncryptedPayload.KeyID != "fake-handle-id"     (NOT from handle.ID())
//
// This eliminates the Current()->Encrypt() window in which a VaultTransit key
// rotation could change the active key version, causing the stored keyID to
// mismatch the actual KEK used for wrapping the DEK.
//
// ref: k8s KMS v2 EncryptResponse.KeyID (kubernetes/kubernetes
// staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go).
func TestKeyProviderTransformer_UsesEncryptReturnedKeyID_NotHandleID(t *testing.T) {
	ctx := context.Background()

	h := &fixedKeyIDHandle{
		handleID:     "fake-handle-id",
		encryptKeyID: "actual-used-keyID",
	}
	p := &fixedHandleProvider{handle: h}
	tr := crypto.NewValueTransformer(p)

	plaintext := []byte("sensitive value")
	aad := testAAD("tc_vt1_key")

	ct, keyID, nonce, edk, err := tr.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.NotEmpty(t, ct)
	assert.NotEmpty(t, nonce)
	assert.NotEmpty(t, edk)

	// Core assertion: transformer must persist the keyID returned by Encrypt.
	assert.Equal(t, "actual-used-keyID", keyID,
		"transformer must use keyID returned by handle.Encrypt, not handle.ID()")

	// Negative assertion: must NOT use handle.ID() ("fake-handle-id").
	assert.NotEqual(t, "fake-handle-id", keyID,
		"transformer must NOT use handle.ID() as the stored keyID")
}

// ---------------------------------------------------------------------------
// TC-VT-2: TestKeyProviderTransformer_EncryptErrorPropagates
// Phase 1-d — TDD RED
// ---------------------------------------------------------------------------

// TestKeyProviderTransformer_EncryptErrorPropagates verifies that a transient
// error from handle.Encrypt is propagated through the transformer so that
// EventBus handlers can route it to DispositionRequeue.
//
// Expected behavior after Phase 2-d (unchanged from current: error wrapping
// must preserve the ErrKeyProviderTransient code in the chain):
//   - err != nil
//   - errcode.IsTransient(err) == true
//   - The error chain contains ErrKeyProviderTransient
//
// This guarantees that envelope-layer transient failures (Vault 503/429)
// can be distinguished from permanent failures (400/403/404) by EventBus
// consumer handlers using errcode.IsTransient.
func TestKeyProviderTransformer_EncryptErrorPropagates(t *testing.T) {
	ctx := context.Background()

	h := &transientErrorHandle{}
	p := &fixedHandleProvider{handle: h}
	tr := crypto.NewValueTransformer(p)

	_, _, _, _, err := tr.Encrypt(ctx, []byte("value"), testAAD("tc_vt2_key"))
	require.Error(t, err, "transient handle.Encrypt error must propagate")

	// errcode.IsTransient traverses the error chain via errors.As.
	// fmt.Errorf("value-transformer: encrypt: %w", transientErr) must preserve
	// the ErrKeyProviderTransient code in the chain.
	assert.True(t, errcode.IsTransient(err),
		"error chain must contain ErrKeyProviderTransient (got: %v)", err)
}

// ---------------------------------------------------------------------------
// TC-VT-3: Decrypt defense-in-depth — provider returns handle with wrong ID
// ---------------------------------------------------------------------------

// mismatchedHandleProvider returns a handle whose ID() differs from the requested keyID.
// This simulates a buggy KeyProvider that routes the lookup to the wrong key.
type mismatchedHandleProvider struct {
	returnedHandle crypto.KeyHandle
}

func (p *mismatchedHandleProvider) Current(_ context.Context) (crypto.KeyHandle, error) {
	return p.returnedHandle, nil
}

func (p *mismatchedHandleProvider) ByID(_ context.Context, _ string) (crypto.KeyHandle, error) {
	// Intentionally return a handle with a different ID than requested.
	return p.returnedHandle, nil
}

func (p *mismatchedHandleProvider) Rotate(_ context.Context) (string, error) {
	return "", nil
}

// mismatchedIDHandle is a handle whose ID() returns a different value than what
// the caller requested — simulating a buggy provider routing error.
type mismatchedIDHandle struct{}

func (h *mismatchedIDHandle) ID() string { return "wrong-key-id" }

func (h *mismatchedIDHandle) Encrypt(_ context.Context, plaintext, _ []byte) ([]byte, []byte, []byte, string, error) {
	return plaintext, nil, nil, "wrong-key-id", nil
}

func (h *mismatchedIDHandle) Decrypt(_ context.Context, ct, _, _, _ []byte) ([]byte, error) {
	return ct, nil
}

// TestValueTransformer_Decrypt_HandleIDMismatch_FailsClosed verifies that
// when the provider returns a handle whose ID() does not match the requested
// keyID, Decrypt returns an error (defense-in-depth against buggy providers).
func TestValueTransformer_Decrypt_HandleIDMismatch_FailsClosed(t *testing.T) {
	ctx := context.Background()
	p := &mismatchedHandleProvider{returnedHandle: &mismatchedIDHandle{}}
	tr := crypto.NewValueTransformer(p)

	_, err := tr.Decrypt(ctx, []byte("ct"), "requested-key-id", nil, nil, nil)
	require.Error(t, err, "handle ID mismatch must return an error (defense-in-depth)")
	assert.Contains(t, err.Error(), "provider returned handle id")
	assert.Contains(t, err.Error(), "requested-key-id")
}
