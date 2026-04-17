package configwrite

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles (ref: order-create/service_test.go) ---

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

type stubPublisher struct{}

func (stubPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

var _ outbox.Publisher = stubPublisher{}

func newTestService() (*Service, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	eb := eventbus.New()
	logger := slog.Default()
	return NewService(repo, eb, logger), repo
}

func newDurableTestService() (*Service, *mem.ConfigRepository, *recordingWriter) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	return svc, repo, writer
}

func TestService_Create(t *testing.T) {
	tests := []struct {
		name    string
		input   CreateInput
		wantErr bool
	}{
		{
			name:    "valid create",
			input:   CreateInput{Key: "app.name", Value: "gocell"},
			wantErr: false,
		},
		{
			name:    "empty key",
			input:   CreateInput{Key: "", Value: "v"},
			wantErr: true,
		},
		{
			name:    "empty value is allowed",
			input:   CreateInput{Key: "app.empty", Value: ""},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestService()
			entry, err := svc.Create(context.Background(), tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, entry)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.input.Key, entry.Key)
				assert.Equal(t, tt.input.Value, entry.Value)
				assert.Equal(t, 1, entry.Version)
			}
		})
	}
}

func TestService_CreateDuplicate(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)

	_, err = svc.Create(context.Background(), CreateInput{Key: "k", Value: "v2"})
	assert.Error(t, err)
}

func TestService_Update(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*Service)
		input   UpdateInput
		wantErr bool
		wantVer int
	}{
		{
			name: "valid update",
			setup: func(svc *Service) {
				_, _ = svc.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
			},
			input:   UpdateInput{Key: "k", Value: "v2"},
			wantErr: false,
			wantVer: 2,
		},
		{
			name:    "update non-existent",
			setup:   func(_ *Service) {},
			input:   UpdateInput{Key: "missing", Value: "v"},
			wantErr: true,
		},
		{
			name:    "empty key",
			setup:   func(_ *Service) {},
			input:   UpdateInput{Key: "", Value: "v"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestService()
			tt.setup(svc)
			entry, err := svc.Update(context.Background(), tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantVer, entry.Version)
				assert.Equal(t, tt.input.Value, entry.Value)
			}
		})
	}
}

func TestService_Delete(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*Service)
		key     string
		wantErr bool
	}{
		{
			name: "valid delete",
			setup: func(svc *Service) {
				_, _ = svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
			},
			key:     "k",
			wantErr: false,
		},
		{
			name:    "delete non-existent",
			setup:   func(_ *Service) {},
			key:     "missing",
			wantErr: true,
		},
		{
			name:    "empty key",
			setup:   func(_ *Service) {},
			key:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestService()
			tt.setup(svc)
			err := svc.Delete(context.Background(), tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- #27d OUTBOX-WRITE-ERR-01: outbox.Write error must propagate ---

func TestService_Create_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
	require.Error(t, err, "Create must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Update_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository()
	goodWriter := &recordingWriter{}
	svcGood := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(goodWriter), WithTxManager(&noopTxRunner{}))
	_, err := svcGood.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)

	failWriter := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(failWriter), WithTxManager(&noopTxRunner{}))

	_, err = svc.Update(context.Background(), UpdateInput{Key: "k", Value: "v2"})
	require.Error(t, err, "Update must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Delete_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository()
	goodWriter := &recordingWriter{}
	svcGood := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(goodWriter), WithTxManager(&noopTxRunner{}))
	_, err := svcGood.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)

	failWriter := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(failWriter), WithTxManager(&noopTxRunner{}))

	err = svc.Delete(context.Background(), "k")
	require.Error(t, err, "Delete must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Create_DurableMode_CapturesOutboxEntry(t *testing.T) {
	svc, _, writer := newDurableTestService()

	entry, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, "k", entry.Key)
	require.Len(t, writer.entries, 1)
	assert.Equal(t, TopicConfigChanged, writer.entries[0].EventType)
}

// TestCreate_CallsTxRunnerRunInTxOnce asserts that Create wraps both the repo
// write and outbox write inside a single RunInTx call (L2 atomicity).
func TestCreate_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	tx := &noopTxRunner{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(tx))

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls, "Create must call RunInTx exactly once")
	assert.Len(t, writer.entries, 1, "outbox entry must be written inside the tx")
}

// TestUpdate_CallsTxRunnerRunInTxOnce asserts that Update wraps the repo+outbox
// writes in a single RunInTx (the pre-fetch GetByKey is outside the tx).
func TestUpdate_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	tx := &noopTxRunner{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(tx))

	// Seed via direct repo insert (bypasses service tx counter).
	_, _ = NewService(repo, stubPublisher{}, slog.Default()).Create(
		context.Background(), CreateInput{Key: "k", Value: "v1"})

	tx.calls = 0 // reset counter after seed
	_, err := svc.Update(context.Background(), UpdateInput{Key: "k", Value: "v2"})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls, "Update must call RunInTx exactly once")
}

// TestDelete_CallsTxRunnerRunInTxOnce asserts that Delete wraps the repo+outbox
// writes in a single RunInTx (the pre-fetch GetByKey is outside the tx).
func TestDelete_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	tx := &noopTxRunner{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(tx))

	// Seed via direct repo insert (bypasses service tx counter).
	_, _ = NewService(repo, stubPublisher{}, slog.Default()).Create(
		context.Background(), CreateInput{Key: "k", Value: "v1"})

	tx.calls = 0 // reset counter after seed
	err := svc.Delete(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls, "Delete must call RunInTx exactly once")
}
