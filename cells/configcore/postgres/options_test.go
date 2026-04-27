package postgres

import (
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/stretchr/testify/assert"
)

func TestOptionsApplySettings(t *testing.T) {
	logger := slog.Default()
	transformer := crypto.NoopTransformer{}
	calls := 0
	callback := func(_, _, _ string) { calls++ }

	cfg := settings{}
	WithLogger(logger)(&cfg)
	WithValueTransformer(transformer)(&cfg)
	WithOnStaleCipher(callback)(&cfg)

	assert.Same(t, logger, cfg.logger)
	assert.Equal(t, transformer, cfg.transformer)
	cfg.onStaleCipher("key", "old", "new")
	assert.Equal(t, 1, calls)
}
