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
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
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

type failingPublisher struct{ err error }

func (p failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return p.err }

var _ outbox.Publisher = failingPublisher{}

func newTestService() (*Service, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	eb := eventbus.New()
	logger := slog.Default()
	return NewService(repo, eb, logger, WithRunMode(query.RunModeDemo)), repo
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

// --- CONFIG-DEMO-FAILOPEN-01: publisher error must propagate in prod mode ---

// TestService_Publish_PublisherError_ProdMode_PropagatesError asserts that
// when the service is wired without an outbox writer (publisher-only path)
// and NOT configured for demo fail-open, a publisher failure surfaces as an
// error. L2 cell declares transactional atomicity; silently swallowing the
// error would violate that contract.
// ref: watermill/components/forwarder — publish failure wraps+returns.
func TestService_Publish_PublisherError_ProdMode_PropagatesError(t *testing.T) {
	repo := mem.NewConfigRepository()
	pub := failingPublisher{err: errors.New("broker unavailable")}
	svc := NewService(repo, pub, slog.Default()) // zero-value RunMode = RunModeProd → fail-closed

	mustSeedEntry(repo, "k", "v1")
	_, err := svc.Publish(context.Background(), "k")
	require.Error(t, err, "prod-mode publisher failure must propagate")
	assert.Contains(t, err.Error(), "broker unavailable")
}

func TestService_Publish_PublisherError_DemoMode_SwallowsError(t *testing.T) {
	repo := mem.NewConfigRepository()
	pub := failingPublisher{err: errors.New("broker unavailable")}
	svc := NewService(repo, pub, slog.Default(), WithRunMode(query.RunModeDemo))

	mustSeedEntry(repo, "k", "v1")
	_, err := svc.Publish(context.Background(), "k")
	require.NoError(t, err, "demo fail-open must swallow publisher failure")
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

// TestPublishVersion_CallsTxRunnerRunInTxOnce asserts that Publish wraps both
// the repo.PublishVersion write and outbox write inside a single RunInTx call
// (L2 atomicity).
func TestPublishVersion_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	tx := &noopTxRunner{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(tx))

	mustSeedEntry(repo, "app.name", "value")
	_, err := svc.Publish(context.Background(), "app.name")
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls, "Publish must call RunInTx exactly once")
	assert.Len(t, writer.entries, 1, "outbox entry must be written inside the tx")
}

// H2-2 CONFIGPUBLISH-REDACT-01: domain.ConfigVersion must carry the source entry's
// Sensitive flag so downstream consumers (handler, postgres replay) can redact uniformly.
func TestService_Publish_SensitiveEntry_VersionCarriesFlag(t *testing.T) {
	repo := mem.NewConfigRepository()
	svc := NewService(repo, stubPublisher{}, slog.Default())
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-secret", Key: "db.password", Value: "s3cret!", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	ver, err := svc.Publish(context.Background(), "db.password")
	require.NoError(t, err)
	assert.True(t, ver.Sensitive, "snapshot must inherit the source entry's Sensitive flag")
	assert.Equal(t, "s3cret!", ver.Value, "domain snapshot keeps the raw value; redaction is a DTO concern")
}

// PR#155 followup F4 (Cx1, P2): service-level rollback NotFound coverage.
// Asserts the typed error code so handler→HTTP status mapping cannot drift.
func TestService_Rollback_KeyNotFound(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.Rollback(context.Background(), "missing-key", 1)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "rollback must return a typed errcode.Error")
	assert.Equal(t, errcode.ErrConfigNotFound, ec.Code,
		"missing key must return ErrConfigNotFound (mem repo) for 404 mapping")
}

func TestService_Rollback_VersionNotFound(t *testing.T) {
	svc, repo := newTestService()
	mustSeedEntry(repo, "app.name", "v1") // entry exists; no version published

	_, err := svc.Rollback(context.Background(), "app.name", 99)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigNotFound, ec.Code,
		"missing version must return ErrConfigNotFound (mem repo) for 404 mapping")
}

func TestService_Publish_NonSensitiveEntry_VersionFlagFalse(t *testing.T) {
	repo := mem.NewConfigRepository()
	svc := NewService(repo, stubPublisher{}, slog.Default())
	mustSeedEntry(repo, "app.name", "gocell")

	ver, err := svc.Publish(context.Background(), "app.name")
	require.NoError(t, err)
	assert.False(t, ver.Sensitive)
}

// PR#155 review F1: rollback must restore the snapshot's Sensitive flag onto
// the live entry so a sensitivity flip between target version and current
// state cannot leak (sensitive→plain) or over-redact (plain→sensitive).
func TestService_Rollback_RestoresSnapshotSensitivity(t *testing.T) {
	tests := []struct {
		name              string
		seedSensitive     bool
		flipToSensitiveAt int // 0 = no flip
		wantSensitive     bool
	}{
		{name: "snapshot sensitive, live plain → entry becomes sensitive", seedSensitive: true, flipToSensitiveAt: 0, wantSensitive: true},
		{name: "snapshot plain, live sensitive → entry becomes plain", seedSensitive: false, flipToSensitiveAt: 1, wantSensitive: false},
		{name: "snapshot plain, live plain → stays plain", seedSensitive: false, flipToSensitiveAt: 0, wantSensitive: false},
		{name: "snapshot sensitive, live sensitive → stays sensitive", seedSensitive: true, flipToSensitiveAt: 1, wantSensitive: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mem.NewConfigRepository()
			svc := NewService(repo, stubPublisher{}, slog.Default())
			now := time.Now()
			require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
				ID: "cfg-x", Key: "app.x", Value: "v1", Sensitive: tt.seedSensitive,
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}))
			// Snapshot v1 with the seeded sensitivity.
			_, err := svc.Publish(context.Background(), "app.x")
			require.NoError(t, err)

			// Optionally flip the live entry's sensitivity to differ from the snapshot.
			if tt.flipToSensitiveAt > 0 {
				live, err := repo.GetByKey(context.Background(), "app.x")
				require.NoError(t, err)
				live.Sensitive = !tt.seedSensitive
				live.Value = "v-live"
				require.NoError(t, repo.Update(context.Background(), live))
			}

			rolled, err := svc.Rollback(context.Background(), "app.x", 1)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSensitive, rolled.Sensitive,
				"rollback must inherit the snapshot's Sensitive flag, not the live entry's")
		})
	}
}
