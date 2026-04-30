package flagwrite

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingTxRunner simulates a tx that wraps fn but fails after fn returns nil,
// used to simulate a rollback scenario where in-memory repo already applied the
// change but tx commits fail.
type failingTxRunner struct{ failErr error }

func (f *failingTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	// Execute fn; the transaction will be rolled back regardless.
	_ = fn(ctx)
	return f.failErr
}

var _ persistence.TxRunner = (*failingTxRunner)(nil)

// --- helpers ---

func newTestService(t *testing.T) (*Service, *mem.FlagRepository) {
	t.Helper()
	repo := mem.NewFlagRepository()
	svc, err := NewService(repo, slog.Default(),
		WithTxManager(&testutil.NoopTxRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo
}

func seedFlag(t *testing.T, repo *mem.FlagRepository, key string) *domain.FeatureFlag {
	t.Helper()
	flag := &domain.FeatureFlag{
		ID:                "flg-" + key,
		Key:               key,
		Enabled:           false,
		RolloutPercentage: 10,
		Description:       "test flag",
		Version:           1,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	require.NoError(t, repo.Create(context.Background(), flag))
	return flag
}

// --- Test: constructor ---

// TestNewService_AllowsHalfWiredDemoPath verifies that service construction no
// longer uses nil-mode coupling; Cell wiring owns durable-mode validation.
func TestNewService_AllowsHalfWiredDemoPath(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
	}{
		{"only_tx_runner", []Option{WithTxManager(&testutil.NoopTxRunner{})}},
		{"no_opts", []Option{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewService(mem.NewFlagRepository(), slog.Default(), tc.opts...)
			require.NoError(t, err)
		})
	}
}

// --- Test: Create atomicity ---

// TestFlagWrite_Create_Atomic_RepoInTx verifies that Create writes repo
// inside a single tx; repo failure propagates correctly.
func TestFlagWrite_Create_Atomic_RepoInTx(t *testing.T) {
	repo := mem.NewFlagRepository()
	tx := &testutil.NoopTxRunner{}
	svc, err := NewService(repo, slog.Default(), WithTxManager(tx))
	require.NoError(t, err)

	flag, err := svc.Create(context.Background(), CreateInput{
		Key:         "my-flag",
		Description: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-flag", flag.Key)
	assert.Equal(t, 1, tx.Calls, "Create must call RunInTx exactly once")

	// Flag must be persisted in repo.
	got, err := repo.GetByKey(context.Background(), "my-flag")
	require.NoError(t, err)
	assert.Equal(t, "my-flag", got.Key)
}

// TestFlagWrite_Create_RepoFails propagates tx-level failures to the caller.
func TestFlagWrite_Create_RepoFails(t *testing.T) {
	repo := mem.NewFlagRepository()
	tx := &failingTxRunner{failErr: errors.New("tx commit failed")}
	svc, err := NewService(repo, slog.Default(), WithTxManager(tx))
	require.NoError(t, err)

	_, err = svc.Create(context.Background(), CreateInput{Key: "k"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tx commit failed")
}

// --- Test: Toggle business result ---

// TestFlagWrite_Toggle_TogglesFlag verifies Toggle sets the correct enabled state.
func TestFlagWrite_Toggle_TogglesFlag(t *testing.T) {
	svc, repo := newTestService(t)
	seedFlag(t, repo, "feature-x")

	flag, err := svc.Toggle(context.Background(), "feature-x", true)
	require.NoError(t, err)
	assert.True(t, flag.Enabled)
	assert.Equal(t, "feature-x", flag.Key)
}

// --- Test: Update business result ---

// TestFlagWrite_Update_UpdatesFlag verifies Update modifies the flag fields.
func TestFlagWrite_Update_UpdatesFlag(t *testing.T) {
	svc, repo := newTestService(t)
	seedFlag(t, repo, "feat-update")

	flag, err := svc.Update(context.Background(), UpdateInput{
		Key:               "feat-update",
		Enabled:           true,
		RolloutPercentage: 50,
		Description:       "updated desc",
	})
	require.NoError(t, err)
	assert.Equal(t, "feat-update", flag.Key)
	assert.True(t, flag.Enabled)
	assert.Equal(t, 50, flag.RolloutPercentage)
	assert.Equal(t, "updated desc", flag.Description)
}

// --- Test: Delete business result ---

// TestFlagWrite_Delete_RemovesFlag verifies Delete removes the flag from repo.
func TestFlagWrite_Delete_RemovesFlag(t *testing.T) {
	svc, repo := newTestService(t)
	seedFlag(t, repo, "feat-delete")

	err := svc.Delete(context.Background(), "feat-delete")
	require.NoError(t, err)

	_, getErr := repo.GetByKey(context.Background(), "feat-delete")
	require.Error(t, getErr, "flag must be removed after Delete")
}

// --- Test: validation ---

// TestFlagWrite_Create_BlankKey rejects blank key with validation error.
func TestFlagWrite_Create_BlankKey(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Create(context.Background(), CreateInput{Key: ""})
	require.Error(t, err)
}

// TestFlagWrite_NoOutboxEmit_AfterDowngrade is a regression lock that guards
// against accidental re-introduction of an outbox emitter in the flag-write
// Service after the PR-CFG-B downgrade from L2 to L1.
//
// event.flag.changed.v1 was retired (lifecycle: deprecated) because it never
// had a subscriber. This test ensures flag-write remains emitter-free even if
// future refactors add outbox.Emitter-typed fields to the struct.
func TestFlagWrite_NoOutboxEmit_AfterDowngrade(t *testing.T) {
	// Structural assertion: Service must not contain an "emitter" field.
	svcType := reflect.TypeFor[Service]()
	for i := 0; i < svcType.NumField(); i++ {
		field := svcType.Field(i)
		assert.NotEqual(t, "emitter", field.Name,
			"Service must not have an emitter field after L2→L1 downgrade (event.flag.changed.v1 retired)")
	}

	// Behavioral assertion: Create/Update/Toggle/Delete must succeed without
	// any outbox writer being injected, confirming no emit attempt is made.
	repo := mem.NewFlagRepository()
	svc, err := NewService(repo, slog.Default(), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	_, createErr := svc.Create(context.Background(), CreateInput{Key: "flag-no-emit", Description: "test"})
	require.NoError(t, createErr, "Create must succeed without emitter (L1 only)")

	_, updateErr := svc.Update(context.Background(), UpdateInput{Key: "flag-no-emit", Description: "updated"})
	require.NoError(t, updateErr, "Update must succeed without emitter (L1 only)")

	_, toggleErr := svc.Toggle(context.Background(), "flag-no-emit", true)
	require.NoError(t, toggleErr, "Toggle must succeed without emitter (L1 only)")

	deleteErr := svc.Delete(context.Background(), "flag-no-emit")
	require.NoError(t, deleteErr, "Delete must succeed without emitter (L1 only)")
}
