package flagwrite

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"log/slog"
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

func (f *failingTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	// Execute fn; the transaction will be rolled back regardless.
	_ = fn(context.Background())
	return f.failErr
}

var _ persistence.TxRunner = (*failingTxRunner)(nil)

// --- helpers ---

func newDurableTestService(t *testing.T) (*Service, *mem.FlagRepository, *testutil.RecordingWriter) {
	t.Helper()
	repo := mem.NewFlagRepository()
	writer := &testutil.RecordingWriter{}
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)),
		WithTxManager(&testutil.NoopTxRunner{}))
	if err != nil {
		t.Fatal(err)
	}
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

// --- Test: XOR violation guard ---

// TestNewService_AllowsHalfWiredDemoPath verifies that service construction no
// longer uses nil-mode coupling; Cell wiring owns durable-mode validation.
func TestNewService_AllowsHalfWiredDemoPath(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
	}{
		{"only_emitter", []Option{WithEmitter(testoutbox.MustEmitter(t, &testutil.RecordingWriter{}))}},
		{"only_tx_runner", []Option{WithTxManager(&testutil.NoopTxRunner{})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewService(mem.NewFlagRepository(), slog.Default(), tc.opts...)
			require.NoError(t, err)
		})
	}
}

// --- Test: Create atomicity ---

// TestFlagWrite_Create_Atomic_RepoAndOutbox verifies that Create writes repo +
// outbox in a single tx; repo failure also prevents outbox write.
func TestFlagWrite_Create_Atomic_RepoAndOutbox(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(tx))
	require.NoError(t, err)

	flag, err := svc.Create(context.Background(), CreateInput{
		Key:         "my-flag",
		Description: "test",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-flag", flag.Key)
	assert.Equal(t, 1, tx.Calls, "Create must call RunInTx exactly once")
	require.Len(t, writer.Entries, 1)
	assert.Equal(t, TopicFlagChanged, writer.Entries[0].EventType)
}

// TestFlagWrite_Create_RepoFails_NoOutboxWrite verifies that a tx-level
// failure propagates as an error to the caller.
//
// Scope note: the in-memory recordingWriter has no transaction-aware
// rollback — it records every Write call unconditionally. This test
// therefore exercises only the error-propagation path; the "outbox is not
// durable when tx rolls back" invariant is verified at the L2 durability
// boundary by PG integration tests that run against a real database
// where Write inside a rolled-back tx is genuinely discarded.
func TestFlagWrite_RepoFails_NoOutboxWrite(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &testutil.RecordingWriter{}
	tx := &failingTxRunner{failErr: errors.New("tx commit failed")}
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(tx))
	require.NoError(t, err)

	_, err = svc.Create(context.Background(), CreateInput{Key: "k"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tx commit failed")
}

// --- Test: Toggle emits flag.changed.v1 ---

// TestFlagWrite_Toggle_EmitsFlagChangedEvent verifies Toggle writes
// outbox with action=toggled payload.
func TestFlagWrite_Toggle_EmitsFlagChangedEvent(t *testing.T) {
	svc, repo, writer := newDurableTestService(t)
	seedFlag(t, repo, "feature-x")

	flag, err := svc.Toggle(context.Background(), "feature-x", true)
	require.NoError(t, err)
	assert.True(t, flag.Enabled)

	require.Len(t, writer.Entries, 1)
	assert.Equal(t, TopicFlagChanged, writer.Entries[0].EventType)

	var payload FlagChangedPayload
	require.NoError(t, json.Unmarshal(writer.Entries[0].Payload, &payload))
	assert.Equal(t, "toggled", payload.Action)
	assert.Equal(t, "feature-x", payload.Key)
	assert.True(t, payload.Enabled)
}

// --- Test: Update emits flag.changed.v1 ---

// TestFlagWrite_Update_EmitsFlagChangedEvent verifies Update outbox payload
// has action=updated.
func TestFlagWrite_Update_EmitsFlagChangedEvent(t *testing.T) {
	svc, repo, writer := newDurableTestService(t)
	seedFlag(t, repo, "feat-update")

	flag, err := svc.Update(context.Background(), UpdateInput{
		Key:               "feat-update",
		Enabled:           true,
		RolloutPercentage: 50,
		Description:       "updated desc",
	})
	require.NoError(t, err)
	assert.Equal(t, "feat-update", flag.Key)

	require.Len(t, writer.Entries, 1)
	var payload FlagChangedPayload
	require.NoError(t, json.Unmarshal(writer.Entries[0].Payload, &payload))
	assert.Equal(t, "updated", payload.Action)
	assert.Equal(t, "feat-update", payload.Key)
}

// --- Test: Delete emits flag.changed.v1 ---

// TestFlagWrite_Delete_EmitsFlagChangedEvent verifies Delete outbox payload
// has action=deleted.
func TestFlagWrite_Delete_EmitsFlagChangedEvent(t *testing.T) {
	svc, repo, writer := newDurableTestService(t)
	seedFlag(t, repo, "feat-delete")

	err := svc.Delete(context.Background(), "feat-delete")
	require.NoError(t, err)

	require.Len(t, writer.Entries, 1)
	var payload FlagChangedPayload
	require.NoError(t, json.Unmarshal(writer.Entries[0].Payload, &payload))
	assert.Equal(t, "deleted", payload.Action)
	assert.Equal(t, "feat-delete", payload.Key)
}

// --- Test: outbox write failure propagates ---

func TestFlagWrite_OutboxWriteError_PropagatesFromCreate(t *testing.T) {
	repo := mem.NewFlagRepository()
	writer := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	_, err = svc.Create(context.Background(), CreateInput{Key: "k"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox")
}
