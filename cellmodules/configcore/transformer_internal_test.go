package configcore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/crypto"
)

// TestResolveValueTransformer covers F3: the silent nil→NoopTransformer fallback
// is gone. dev mode resolves a nil provider to an explicit NoopTransformer; real
// mode rejects it fail-closed.
func TestResolveValueTransformer(t *testing.T) {
	t.Run("dev mode nil provider resolves to explicit NoopTransformer", func(t *testing.T) {
		vt, err := resolveValueTransformer(nil, false)
		require.NoError(t, err)
		_, isNoop := vt.(crypto.NoopTransformer)
		assert.True(t, isNoop, "dev mode must use an explicit NoopTransformer")
	})

	t.Run("real mode nil provider is rejected fail-closed", func(t *testing.T) {
		vt, err := resolveValueTransformer(nil, true)
		require.Error(t, err)
		assert.Nil(t, vt)
		assert.Contains(t, err.Error(), "GOCELL_CONFIGCORE_KEY_PROVIDER")
		assert.Contains(t, err.Error(), "dev-only")
	})
}
