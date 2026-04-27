package postgres

import (
	"log/slog"
	"reflect"
	"testing"

	configcore "github.com/ghbvf/gocell/cells/configcore"
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

func TestWithPoolWiresStaleCipherCallback(t *testing.T) {
	c := configcore.NewConfigCore(
		WithPool(nil, WithOnStaleCipher(func(_, _, _ string) {})),
	)

	repo := reflect.ValueOf(c).Elem().FieldByName("configRepo")
	assert.False(t, repo.IsNil(), "WithPool must inject a config repository")
	concreteRepo := repo.Elem().Elem()
	callback := concreteRepo.FieldByName("onStaleCipher")
	assert.False(t, callback.IsNil(), "WithPool must pass WithOnStaleCipher to the repository")
}
