package webhook

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// fakeVT is an AAD-binding identity transformer for funnel tests. Encrypt copies
// the plaintext into Ciphertext and records the AAD in EDK; Decrypt fails unless
// the caller-supplied AAD matches the recorded EDK — modeling AES-GCM's tag
// check over the AAD without real crypto. failEncrypt / failDecrypt force the
// error paths.
type fakeVT struct {
	failEncrypt bool
	failDecrypt bool
}

func (f fakeVT) Encrypt(_ context.Context, pt, aad []byte) (kcrypto.EncryptResult, error) {
	if f.failEncrypt {
		return kcrypto.EncryptResult{}, errors.New("kms unavailable")
	}
	return kcrypto.EncryptResult{
		Ciphertext: append([]byte(nil), pt...),
		Nonce:      []byte("nonce"),
		EDK:        append([]byte(nil), aad...),
		KeyID:      "fake-v1",
	}, nil
}

func (f fakeVT) Decrypt(_ context.Context, ct []byte, _ string, _, edk, aad []byte) ([]byte, error) {
	if f.failDecrypt {
		return nil, errors.New("kms unavailable")
	}
	// AES-GCM authenticates the AAD: a tuple whose encrypt-time AAD (recorded in
	// edk) differs from the decrypt-time AAD fails the tag check.
	if !bytes.Equal(edk, aad) {
		return nil, errors.New("aad mismatch (tag check failed)")
	}
	return append([]byte(nil), ct...), nil
}

func mustSource(t *testing.T, id, secret string) Source {
	t.Helper()
	sid, err := NewSourceID(id)
	require.NoError(t, err)
	src, err := NewSource(sid, []byte(secret))
	require.NoError(t, err)
	return src
}

const testSecret = "0123456789abcdef01234567" // 24 bytes, the minimum secret length

// TestSourceEncrypt_Roundtrip proves the sealed funnel preserves the secret:
// Encrypt → NewSourceFromCiphertext yields a Source whose secret equals the
// original, while the plaintext is only ever handed back through the sealed type.
func TestSourceEncrypt_Roundtrip(t *testing.T) {
	ctx := context.Background()
	vt := fakeVT{}
	src := mustSource(t, "github", testSecret)

	res, err := src.Encrypt(ctx, vt)
	require.NoError(t, err)
	assert.Equal(t, "fake-v1", res.KeyID)
	assert.Equal(t, []byte(testSecret), res.Ciphertext)

	got, err := NewSourceFromCiphertext(ctx, vt, src.ID(), res.Ciphertext, res.KeyID, res.Nonce, res.EDK)
	require.NoError(t, err)
	assert.Equal(t, src.ID(), got.ID())
	// Internal-package access: the decrypted secret round-trips byte-for-byte.
	assert.Equal(t, []byte(testSecret), got.secret)
}

// TestNewSourceFromCiphertext_AADTransplantRejected proves the AAD binds the
// ciphertext to its source id: a tuple encrypted for "github" cannot be decrypted
// under "stripe" (cross-source transplant), fail-closed.
func TestNewSourceFromCiphertext_AADTransplantRejected(t *testing.T) {
	ctx := context.Background()
	vt := fakeVT{}
	src := mustSource(t, "github", testSecret)
	res, err := src.Encrypt(ctx, vt)
	require.NoError(t, err)

	other, err := NewSourceID("stripe")
	require.NoError(t, err)

	_, err = NewSourceFromCiphertext(ctx, vt, other, res.Ciphertext, res.KeyID, res.Nonce, res.EDK)
	requireErrCode(t, err, errcode.ErrWebhookSecretCryptoFailed, errcode.KindInternal)
}

func TestSourceEncrypt_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	src := mustSource(t, "github", testSecret)

	t.Run("empty secret (zero-value Source)", func(t *testing.T) {
		_, err := Source{}.Encrypt(ctx, fakeVT{})
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
	t.Run("nil transformer", func(t *testing.T) {
		_, err := src.Encrypt(ctx, nil)
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
	t.Run("transformer encrypt failure", func(t *testing.T) {
		_, err := src.Encrypt(ctx, fakeVT{failEncrypt: true})
		requireErrCode(t, err, errcode.ErrWebhookSecretCryptoFailed, errcode.KindInternal)
	})
}

func TestNewSourceFromCiphertext_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	id, err := NewSourceID("github")
	require.NoError(t, err)
	aad := sourceAAD(id)

	t.Run("nil transformer", func(t *testing.T) {
		_, err := NewSourceFromCiphertext(ctx, nil, id, []byte(testSecret), "fake-v1", nil, aad)
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
	t.Run("transformer decrypt failure", func(t *testing.T) {
		_, err := NewSourceFromCiphertext(ctx, fakeVT{failDecrypt: true}, id, []byte(testSecret), "fake-v1", nil, aad)
		requireErrCode(t, err, errcode.ErrWebhookSecretCryptoFailed, errcode.KindInternal)
	})
	t.Run("decrypted secret below length floor", func(t *testing.T) {
		// A valid AAD but a too-short recovered secret must be rejected by
		// NewSource's length floor, not silently accepted.
		_, err := NewSourceFromCiphertext(ctx, fakeVT{}, id, []byte("short"), "fake-v1", nil, aad)
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
}

// TestSourceAAD_DomainIsolation locks the AAD shape: the webhook domain prefix is
// distinct from configcore's, and the id is bound in.
func TestSourceAAD_DomainIsolation(t *testing.T) {
	gh, err := NewSourceID("github")
	require.NoError(t, err)
	stripe, err := NewSourceID("stripe")
	require.NoError(t, err)

	assert.Equal(t, "cell:webhook/source:github", string(sourceAAD(gh)))
	assert.NotEqual(t, sourceAAD(gh), sourceAAD(stripe))
}
