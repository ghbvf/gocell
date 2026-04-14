package configpublish

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
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

func seedEntry(t *testing.T, repo *mem.ConfigRepository, key, value string) {
	t.Helper()
	mustSeedEntry(repo, key, value)
}

func mustSeedEntry(repo *mem.ConfigRepository, key, value string) {
	now := time.Now()
	_ = repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
}

func TestService_Publish(t *testing.T) {
	tests := []struct {
		name    string
		seed    bool
		key     string
		wantErr bool
	}{
		{name: "valid publish", seed: true, key: "app.name", wantErr: false},
		{name: "empty key", seed: false, key: "", wantErr: true},
		{name: "non-existent key", seed: false, key: "missing", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			if tt.seed {
				seedEntry(t, repo, tt.key, "value")
			}

			ver, err := svc.Publish(context.Background(), tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, 1, ver.Version)
				assert.NotNil(t, ver.PublishedAt)
			}
		})
	}
}

func TestService_Rollback(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*Service, *mem.ConfigRepository)
		key     string
		version int
		wantErr bool
	}{
		{
			name: "valid rollback",
			setup: func(svc *Service, repo *mem.ConfigRepository) {
				mustSeedEntry(repo, "app.name", "v1")
				_, _ = svc.Publish(context.Background(), "app.name")
			},
			key: "app.name", version: 1, wantErr: false,
		},
		{
			name:    "empty key",
			setup:   func(_ *Service, _ *mem.ConfigRepository) {},
			key:     "",
			version: 1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			if tt.setup != nil {
				tt.setup(svc, repo)
			}

			entry, err := svc.Rollback(context.Background(), tt.key, tt.version)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "v1", entry.Value)
			}
		})
	}
}

// --- #27d OUTBOX-WRITE-ERR-01: outbox.Write error must propagate ---

func TestService_Publish_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	mustSeedEntry(repo, "app.name", "value")

	_, err := svc.Publish(context.Background(), "app.name")
	require.Error(t, err, "Publish must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Rollback_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	mustSeedEntry(repo, "app.name", "v1")
	// Publish first (use a working writer), then swap to failing writer for rollback.
	goodWriter := &recordingWriter{}
	svcGood := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(goodWriter), WithTxManager(&noopTxRunner{}))
	_, err := svcGood.Publish(context.Background(), "app.name")
	require.NoError(t, err)

	_, err = svc.Rollback(context.Background(), "app.name", 1)
	require.Error(t, err, "Rollback must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Publish_DurableMode_CapturesOutboxEntry(t *testing.T) {
	svc, repo, writer := newDurableTestService()
	mustSeedEntry(repo, "app.name", "value")

	ver, err := svc.Publish(context.Background(), "app.name")
	require.NoError(t, err)
	assert.Equal(t, 1, ver.Version)
	require.Len(t, writer.entries, 1)
	assert.Equal(t, TopicConfigChanged, writer.entries[0].EventType)
}
