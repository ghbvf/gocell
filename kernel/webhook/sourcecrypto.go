package webhook

import (
	"context"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// sourceAAD computes the Additional Authenticated Data that binds a webhook
// source secret's ciphertext to its owning source. AES-GCM authenticates the
// AAD, so any mismatch surfaces as a decryption (tag) failure — fail-closed.
//
// The "cell:webhook/source:" domain prefix is deliberately distinct from
// configcore's "cell:configcore/..." AAD (see corecells configcore crypto), so a
// ciphertext encrypted for a config entry can never be transplanted into a
// webhook source row and decrypt, and vice versa. The trailing source id binds
// the ciphertext to one source so a row cannot be moved under a different id.
// Unlike configcore, there is no tenant segment: webhook sources are global (the
// [SourceStore] lookup carries no tenant), so the id alone is the identity.
//
// The AAD is computed here — inside the package that owns the sealed secret —
// rather than supplied by the persistence layer, so a backing store cannot pass
// a wrong or absent AAD and weaken the binding (the domain-isolation guarantee
// is a type-system property, not repo discipline).
func sourceAAD(id SourceID) []byte {
	return []byte("cell:webhook/source:" + string(id))
}

// Encrypt seals this source's secret under vt's current key, binding the
// ciphertext to the source id via [sourceAAD]. It is the only sanctioned path
// to turn a webhook secret into ciphertext: the plaintext secret never leaves
// this package — callers receive only the [kcrypto.EncryptResult] (ciphertext +
// key metadata) to persist, never the raw bytes. Together with [Source] having
// no secret getter, this makes a plaintext webhook secret unrepresentable
// outside kernel/webhook and the transformer internals.
//
// A zero-value Source (empty secret — i.e. one not built via [NewSource]) is
// rejected with [errcode.ErrWebhookConfigInvalid]; vt must be non-nil.
func (s Source) Encrypt(ctx context.Context, vt kcrypto.ValueTransformer) (kcrypto.EncryptResult, error) {
	if len(s.secret) == 0 {
		return kcrypto.EncryptResult{}, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: cannot encrypt a source with an empty secret")
	}
	if vt == nil {
		return kcrypto.EncryptResult{}, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: cannot encrypt a source without a value transformer")
	}
	res, err := vt.Encrypt(ctx, s.secret, sourceAAD(s.id))
	if err != nil {
		return kcrypto.EncryptResult{}, errcode.Wrap(errcode.KindInternal,
			errcode.ErrWebhookSecretCryptoFailed, "webhook: encrypt source secret failed", err)
	}
	return res, nil
}

// NewSourceFromCiphertext decrypts a persisted cipher tuple via vt and returns
// the sealed [Source]. It is the read-path counterpart to [Source.Encrypt]: the
// recovered plaintext is handed straight to [NewSource] and never returned to
// the caller as raw bytes, so a persistent [SourceStore] reconstructs sources
// without ever materializing a loose secret slice. The AAD is recomputed from id
// (see [sourceAAD]); a tuple whose AAD does not match — e.g. a ciphertext moved
// under a different source id — fails the AES-GCM tag check and is rejected
// fail-closed with [errcode.ErrWebhookSecretCryptoFailed].
//
// vt must be non-nil. The recovered secret is still subject to [NewSource]'s
// length floor, so a too-short decrypted secret is rejected.
func NewSourceFromCiphertext(
	ctx context.Context, vt kcrypto.ValueTransformer,
	id SourceID, ciphertext []byte, keyID string, nonce, edk []byte,
) (Source, error) {
	if vt == nil {
		return Source{}, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: cannot decrypt a source without a value transformer")
	}
	pt, err := vt.Decrypt(ctx, ciphertext, keyID, nonce, edk, sourceAAD(id))
	if err != nil {
		return Source{}, errcode.Wrap(errcode.KindInternal,
			errcode.ErrWebhookSecretCryptoFailed, "webhook: decrypt source secret failed", err)
	}
	return NewSource(id, pt)
}
