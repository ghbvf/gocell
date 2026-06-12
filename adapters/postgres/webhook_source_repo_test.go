package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// webhookStubTransformer is a non-nil ValueTransformer for the constructor-guard
// tests (which never reach a SQL call, so the bodies are unused).
type webhookStubTransformer struct{}

func (webhookStubTransformer) Encrypt(context.Context, []byte, []byte) (kcrypto.EncryptResult, error) {
	return kcrypto.EncryptResult{}, nil
}

func (webhookStubTransformer) Decrypt(context.Context, []byte, string, []byte, []byte, []byte) ([]byte, error) {
	return nil, nil
}

// TestNewWebhookSourceRepository_Guards covers the fail-closed constructor:
// a nil transformer is rejected first (no plaintext-at-rest path), and a nil pool
// is rejected. Both fire before any SQL, so no database is required.
func TestNewWebhookSourceRepository_Guards(t *testing.T) {
	t.Run("nil transformer rejected (no plaintext fallback)", func(t *testing.T) {
		repo, err := NewWebhookSourceRepository(nil, nil)
		assert.Nil(t, repo)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
		assert.Contains(t, ec.Message, "plaintext")
	})

	t.Run("nil pool rejected", func(t *testing.T) {
		repo, err := NewWebhookSourceRepository(nil, webhookStubTransformer{})
		assert.Nil(t, repo)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	})
}
