package flagwrite

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithTxManager(&testutil.NoopTxRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo
}

func seedFlag(t *testing.T, repo *mem.FlagRepository, key string) {
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
}

// --- Test: constructor ---

// TestNewService_TxRunnerRequired locks the constructor fail-fast contract
// introduced in 029 #03 ADR Decision 2 (deletion of persistence.RunnerOrNoop).
// Demo callers must inject an explicit pass-through TxRunner via WithTxManager
// rather than relying on a silent nil fallback.
func TestNewService_TxRunnerRequired(t *testing.T) {
	t.Run("with_tx_runner_succeeds", func(t *testing.T) {
		_, err := NewService(mem.NewFlagRepository(clock.Real()), slog.Default(), clock.Real(),
			WithTxManager(&testutil.NoopTxRunner{}))
		require.NoError(t, err)
	})
	t.Run("without_tx_runner_fails_fast", func(t *testing.T) {
		_, err := NewService(mem.NewFlagRepository(clock.Real()), slog.Default(), clock.Real())
		require.Error(t, err)
	})
}

// --- Test: Create atomicity ---

// TestFlagWrite_Create_Atomic_RepoInTx verifies that Create writes repo
// inside a single tx; repo failure propagates correctly.
func TestFlagWrite_Create_Atomic_RepoInTx(t *testing.T) {
	repo := mem.NewFlagRepository(clock.Real())
	tx := &testutil.NoopTxRunner{}
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(tx))
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
	repo := mem.NewFlagRepository(clock.Real())
	tx := &failingTxRunner{failErr: errors.New("tx commit failed")}
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(tx))
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

	flag, err := svc.Toggle(context.Background(), "feature-x", 1, true)
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
		ExpectedVersion:   1,
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

	err := svc.Delete(context.Background(), "feat-delete", 1)
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
	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	_, createErr := svc.Create(context.Background(), CreateInput{Key: "flag-no-emit", Description: "test"})
	require.NoError(t, createErr, "Create must succeed without emitter (L1 only)")

	_, updateErr := svc.Update(context.Background(), UpdateInput{Key: "flag-no-emit", ExpectedVersion: 1, Description: "updated"})
	require.NoError(t, updateErr, "Update must succeed without emitter (L1 only)")

	_, toggleErr := svc.Toggle(context.Background(), "flag-no-emit", 2, true)
	require.NoError(t, toggleErr, "Toggle must succeed without emitter (L1 only)")

	deleteErr := svc.Delete(context.Background(), "flag-no-emit", 3)
	require.NoError(t, deleteErr, "Delete must succeed without emitter (L1 only)")
}

// concurrentSafeTxRunner is a stateless pass-through TxRunner safe for concurrent use.
// Unlike testutil.NoopTxRunner it has no mutable Calls field, avoiding data races.
type concurrentSafeTxRunner struct{}

func (concurrentSafeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// TestConcurrentToggle_ExactlyOneSucceeds verifies that when two goroutines race
// to toggle the same feature flag with the same expectedVersion, exactly one
// succeeds and the other receives ErrVersionConflict.
func TestConcurrentToggle_ExactlyOneSucceeds(t *testing.T) {
	t.Parallel()

	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(concurrentSafeTxRunner{}))
	require.NoError(t, err)
	seedFlag(t, repo, "cas-toggle-flag")

	var (
		successes        atomic.Int32
		versionConflicts atomic.Int32
	)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		enabled := i == 0
		go func(enabled bool) {
			defer wg.Done()
			_, togErr := svc.Toggle(context.Background(), "cas-toggle-flag", 1, enabled)
			if togErr == nil {
				successes.Add(1)
			} else {
				var ce *errcode.Error
				if errors.As(togErr, &ce) && ce.Code == errcode.ErrVersionConflict {
					versionConflicts.Add(1)
				} else {
					t.Errorf("unexpected error in concurrent Toggle: %v", togErr)
				}
			}
		}(enabled)
	}
	wg.Wait()

	assert.Equal(t, int32(1), successes.Load(), "exactly one concurrent Toggle must succeed")
	assert.Equal(t, int32(1), versionConflicts.Load(), "exactly one concurrent Toggle must yield ErrVersionConflict")
}

// TestConcurrentUpdate_ExactlyOneSucceeds verifies that when two goroutines race
// to update the same feature flag with the same expectedVersion, exactly one
// succeeds and the other receives ErrVersionConflict.
func TestConcurrentUpdate_ExactlyOneSucceeds(t *testing.T) {
	t.Parallel()

	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(concurrentSafeTxRunner{}))
	require.NoError(t, err)
	seedFlag(t, repo, "cas-update-flag")

	var (
		successes        atomic.Int32
		versionConflicts atomic.Int32
	)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		desc := "desc-A"
		if i == 1 {
			desc = "desc-B"
		}
		go func(desc string) {
			defer wg.Done()
			_, upErr := svc.Update(context.Background(), UpdateInput{
				Key:             "cas-update-flag",
				ExpectedVersion: 1,
				Description:     desc,
			})
			if upErr == nil {
				successes.Add(1)
			} else {
				var ce *errcode.Error
				if errors.As(upErr, &ce) && ce.Code == errcode.ErrVersionConflict {
					versionConflicts.Add(1)
				} else {
					t.Errorf("unexpected error in concurrent Update: %v", upErr)
				}
			}
		}(desc)
	}
	wg.Wait()

	assert.Equal(t, int32(1), successes.Load(), "exactly one concurrent Update must succeed")
	assert.Equal(t, int32(1), versionConflicts.Load(), "exactly one concurrent Update must yield ErrVersionConflict")
}

// TestConcurrentDelete_ExactlyOneSucceeds verifies that when two goroutines race
// to delete the same feature flag with the same expectedVersion, exactly one
// succeeds. The loser receives either ErrVersionConflict or ErrFlagNotFound
// (when the winner committed the delete before the loser's CAS check).
func TestConcurrentDelete_ExactlyOneSucceeds(t *testing.T) {
	t.Parallel()

	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(concurrentSafeTxRunner{}))
	require.NoError(t, err)
	seedFlag(t, repo, "cas-delete-flag")

	var (
		successes atomic.Int32
		losers    atomic.Int32
	)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			delErr := svc.Delete(context.Background(), "cas-delete-flag", 1)
			if delErr == nil {
				successes.Add(1)
			} else {
				var ce *errcode.Error
				if errors.As(delErr, &ce) &&
					(ce.Code == errcode.ErrVersionConflict || ce.Code == errcode.ErrFlagNotFound) {
					losers.Add(1)
				} else {
					t.Errorf("unexpected error in concurrent Delete: %v", delErr)
				}
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), successes.Load(), "exactly one concurrent Delete must succeed")
	assert.Equal(t, int32(1), losers.Load(), "exactly one concurrent Delete must yield ErrVersionConflict or ErrFlagNotFound")
}
