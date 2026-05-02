package postgres

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
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
	pool := &pgxpool.Pool{}
	opt, err := WithPool(pool, clock.Real(), WithOnStaleCipher(func(_, _, _ string) {}))
	require.NoError(t, err)
	c := configcore.NewConfigCore(
		opt,
	)

	repo := reflect.ValueOf(c).Elem().FieldByName("configRepo")
	assert.False(t, repo.IsNil(), "WithPool must inject a config repository")
	concreteRepo := repo.Elem().Elem()
	callback := concreteRepo.FieldByName("onStaleCipher")
	assert.False(t, callback.IsNil(), "WithPool must pass WithOnStaleCipher to the repository")
}

func TestWithPoolRejectsNilPool(t *testing.T) {
	opt, err := WithPool(nil, clock.Real())
	require.Error(t, err)
	assert.Nil(t, opt)
	var coded *errcode.Error
	require.ErrorAs(t, err, &coded)
	assert.Equal(t, errcode.ErrCellInvalidConfig, coded.Code)
	assert.Contains(t, err.Error(), "non-nil *pgxpool.Pool")
}
