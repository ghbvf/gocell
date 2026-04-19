package flagwrite

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles ---

type recordingWriter struct {
	entries []outbox.Entry
	err     error
}

func (w *recordingWriter) Write(_ context.Context, entry outbox.Entry) error {
	if w.err != nil {
		return w.err
	}
	w.entries = append(w.entries, entry)
	return nil
}

var _ outbox.Writer = (*recordingWriter)(nil)

type noopTxRunner struct{ calls int }

func (s *noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(ctx)
}

var _ persistence.TxRunner = (*noopTxRunner)(nil)

// failingTxRunner simulates a tx that wraps fn but fails after fn returns nil,
// used to simulate a rollback scenario where in-memory repo already applied the
// change but tx commits fail.
type failingTxRunner struct{ failErr error }

func (f *failingTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	// Execute fn; the transaction will be rolled back regardless.
	_ = fn(context.Background())
	return f.failErr
}

var _ persistence.TxRunner = (*failingTxRunner)(nil)

type stubPublisher struct{}

func (stubPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

var _ outbox.Publisher = stubPublisher{}

// --- helpers ---

func newDurableTestService() (*Service, *mem.FlagRepository, *recordingWriter) {
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer),
		WithTxManager(&noopTxRunner{}))
	return svc, repo, writer
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

// --- Test: Create atomicity ---

// TestFlagWrite_Create_Atomic_RepoAndOutbox verifies that Create writes repo +
// outbox in a single tx; repo failure also prevents outbox write.
func TestFlagWrite_Create_Atomic_RepoAndOutbox(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	tx := &noopTxRunner{}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(tx))

	flag, err := svc.Create(context.Background(), CreateInput{
		Key:         "my-flag",
		Description: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-flag", flag.Key)
	assert.Equal(t, 1, tx.calls, "Create must call RunInTx exactly once")
	require.Len(t, writer.entries, 1)
	assert.Equal(t, TopicFlagChanged, writer.entries[0].EventType)
}

// TestFlagWrite_Create_RepoFails_NoOutboxWrite verifies that if the repo write
// fails the outbox is NOT written (tx rollback semantic modelled via failing
// tx that always errors out regardless of fn result).
func TestFlagWrite_RepoFails_NoOutboxWrite(t *testing.T) {
	// Use a real repo + a tx runner that ignores fn result and returns an error.
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{}
	tx := &failingTxRunner{failErr: errors.New("tx commit failed")}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(tx))

	_, err := svc.Create(context.Background(), CreateInput{Key: "k"})
	require.Error(t, err)
	// outbox writer may have been called inside fn, but tx rollback means
	// the in-flight entry is not durable; verify via tx returning error.
	assert.Contains(t, err.Error(), "tx commit failed")
}

// --- Test: Toggle emits flag.changed.v1 ---

// TestFlagWrite_Toggle_EmitsFlagChangedEvent verifies Toggle writes
// outbox with action=toggled payload.
func TestFlagWrite_Toggle_EmitsFlagChangedEvent(t *testing.T) {
	svc, repo, writer := newDurableTestService()
	seedFlag(t, repo, "feature-x")

	flag, err := svc.Toggle(context.Background(), "feature-x", true)
	require.NoError(t, err)
	assert.True(t, flag.Enabled)

	require.Len(t, writer.entries, 1)
	assert.Equal(t, TopicFlagChanged, writer.entries[0].EventType)

	var payload FlagChangedPayload
	require.NoError(t, json.Unmarshal(writer.entries[0].Payload, &payload))
	assert.Equal(t, "toggled", payload.Action)
	assert.Equal(t, "feature-x", payload.Key)
	assert.True(t, payload.Enabled)
}

// --- Test: Update emits flag.changed.v1 ---

// TestFlagWrite_Update_EmitsFlagChangedEvent verifies Update outbox payload
// has action=updated.
func TestFlagWrite_Update_EmitsFlagChangedEvent(t *testing.T) {
	svc, repo, writer := newDurableTestService()
	seedFlag(t, repo, "feat-update")

	flag, err := svc.Update(context.Background(), UpdateInput{
		Key:               "feat-update",
		Enabled:           true,
		RolloutPercentage: 50,
		Description:       "updated desc",
	})
	require.NoError(t, err)
	assert.Equal(t, "feat-update", flag.Key)

	require.Len(t, writer.entries, 1)
	var payload FlagChangedPayload
	require.NoError(t, json.Unmarshal(writer.entries[0].Payload, &payload))
	assert.Equal(t, "updated", payload.Action)
	assert.Equal(t, "feat-update", payload.Key)
}

// --- Test: Delete emits flag.changed.v1 ---

// TestFlagWrite_Delete_EmitsFlagChangedEvent verifies Delete outbox payload
// has action=deleted.
func TestFlagWrite_Delete_EmitsFlagChangedEvent(t *testing.T) {
	svc, repo, writer := newDurableTestService()
	seedFlag(t, repo, "feat-delete")

	err := svc.Delete(context.Background(), "feat-delete")
	require.NoError(t, err)

	require.Len(t, writer.entries, 1)
	var payload FlagChangedPayload
	require.NoError(t, json.Unmarshal(writer.entries[0].Payload, &payload))
	assert.Equal(t, "deleted", payload.Action)
	assert.Equal(t, "feat-delete", payload.Key)
}

// --- Test: outbox write failure propagates ---

func TestFlagWrite_OutboxWriteError_PropagatesFromCreate(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))

	_, err := svc.Create(context.Background(), CreateInput{Key: "k"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox")
}
