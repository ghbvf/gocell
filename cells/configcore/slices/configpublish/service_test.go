package configpublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/internal/testoutbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// adminSvcCtx returns a context with an admin principal for direct service calls.
func adminSvcCtx() context.Context {
	return auth.TestContext("test-admin", []string{"admin"})
}

// newTestService returns a Service wired with the default NoopEmitter
// (NoopWriter under the hood — Write returns nil silently, no WARN emitted).
// This implicitly covers the noop publisher CI path (PR320-FU): every
// happy-path test in this file exercises noop wiring without panic or error.
// The FailOpen WARN signal — a stricter property — is asserted separately
// by Test{Service_Publish,Service_Rollback}_FailOpen_PublisherError using
// FailingPublisher + DirectPublishFailOpen.
func newTestService() (*Service, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository(clock.Real())
	logger := slog.Default()
	svc, err := NewService(repo, logger, clock.Real(), WithTxManager(&testutil.NoopTxRunner{}))
	if err != nil {
		panic("newTestService: " + err.Error())
	}
	return svc, repo
}

func newDirectTestEmitter(t *testing.T, pub outbox.Publisher, mode outbox.DirectPublishFailureMode, logger *slog.Logger) outbox.Emitter {
	t.Helper()
	emitter, err := outbox.NewDirectEmitter(pub, mode, metrics.NopProvider{}, clock.Real(), "configcore", outbox.WithLogger(logger))
	require.NoError(t, err)
	return emitter
}

func newDurableTestService(t testing.TB) (*Service, *mem.ConfigRepository, *testutil.RecordingWriter) {
	t.Helper()
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
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

func TestNewService_TxRunnerRequired(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	_, err := NewService(repo, slog.Default(), clock.Real() /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
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

			ver, err := svc.Publish(adminSvcCtx(), tt.key)
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
				_, _ = svc.Publish(adminSvcCtx(), "app.name")
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
		{
			name:    "zero version rejected",
			setup:   func(_ *Service, _ *mem.ConfigRepository) {},
			key:     "app.name",
			version: 0,
			wantErr: true,
		},
		{
			name:    "negative version rejected",
			setup:   func(_ *Service, _ *mem.ConfigRepository) {},
			key:     "app.name",
			version: -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			if tt.setup != nil {
				tt.setup(svc, repo)
			}

			entry, err := svc.Rollback(adminSvcCtx(), tt.key, tt.version)
			if tt.wantErr {
				assert.Error(t, err)
				var ec *errcode.Error
				if errors.As(err, &ec) {
					assert.Equal(t, errcode.ErrConfigPublishInvalidInput, ec.Code,
						"validation errors must surface ErrConfigPublishInvalidInput")
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, "v1", entry.Value)
			}
		})
	}
}

// --- publisher errors propagate (no fail-open on write path) ---

// TestService_Publish_PublisherError_Propagates asserts that a publisher failure
// on the publisher-only path (no outboxWriter) surfaces as an error.
// L2 cell declares transactional atomicity; silently swallowing the error
// would violate that contract.
// ref: watermill/components/forwarder — publish failure wraps+returns.
func TestService_Publish_PublisherError_Propagates(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	pub := testutil.FailingPublisher{Err: errors.New("broker unavailable")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(newDirectTestEmitter(t, pub, outbox.DirectPublishFailClosed, slog.Default())),
		WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	mustSeedEntry(repo, "k", "v1")
	_, err = svc.Publish(adminSvcCtx(), "k")
	require.Error(t, err, "publisher failure must propagate")
	assert.Contains(t, err.Error(), "broker unavailable")
}

// --- #27d OUTBOX-WRITE-ERR-01: outbox.Write error must propagate ---

func TestService_Publish_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	mustSeedEntry(repo, "app.name", "value")

	_, err = svc.Publish(adminSvcCtx(), "app.name")
	require.Error(t, err, "Publish must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Rollback_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	mustSeedEntry(repo, "app.name", "v1")
	// Publish first (use a working writer), then swap to failing writer for rollback.
	goodWriter := &testutil.RecordingWriter{}
	svcGood, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, goodWriter)), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	_, err = svcGood.Publish(adminSvcCtx(), "app.name")
	require.NoError(t, err)

	_, err = svc.Rollback(adminSvcCtx(), "app.name", 1)
	require.Error(t, err, "Rollback must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Publish_DurableMode_CapturesOutboxEntry(t *testing.T) {
	svc, repo, writer := newDurableTestService(t)
	mustSeedEntry(repo, "app.name", "value")

	ver, err := svc.Publish(adminSvcCtx(), "app.name")
	require.NoError(t, err)
	assert.Equal(t, 1, ver.Version)
	require.Len(t, writer.Entries, 1)
	assert.Equal(t, domain.TopicConfigVersionPublished, writer.Entries[0].EventType)
}

// TestPublishVersion_CallsTxRunnerRunInTxOnce asserts that Publish wraps both
// the repo.PublishVersion write and outbox write inside a single RunInTx call
// (L2 atomicity).
func TestPublishVersion_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(tx))
	require.NoError(t, err)

	mustSeedEntry(repo, "app.name", "value")
	_, err = svc.Publish(adminSvcCtx(), "app.name")
	require.NoError(t, err)
	assert.Equal(t, 1, tx.Calls, "Publish must call RunInTx exactly once")
	assert.Len(t, writer.Entries, 1, "outbox entry must be written inside the tx")
}

// H2-2 CONFIGPUBLISH-REDACT-01: domain.ConfigVersion must carry the source entry's
// Sensitive flag so downstream consumers (handler, postgres replay) can redact uniformly.
func TestService_Publish_SensitiveEntry_VersionCarriesFlag(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-secret", Key: "db.password", Value: "s3cret!", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	ver, err := svc.Publish(adminSvcCtx(), "db.password")
	require.NoError(t, err)
	assert.True(t, ver.Sensitive, "snapshot must inherit the source entry's Sensitive flag")
	assert.Equal(t, "s3cret!", ver.Value, "domain snapshot keeps the raw value; redaction is a DTO concern")
}

// PR#155 followup F4 (Cx1, P2): service-level rollback NotFound coverage.
// Asserts the typed error code so handler→HTTP status mapping cannot drift.
func TestService_Rollback_KeyNotFound(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.Rollback(adminSvcCtx(), "missing-key", 1)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "rollback must return a typed errcode.Error")
	assert.Equal(t, errcode.ErrConfigNotFound, ec.Code,
		"missing key must return ErrConfigNotFound (mem repo) for 404 mapping")
}

func TestService_Rollback_VersionNotFound(t *testing.T) {
	svc, repo := newTestService()
	mustSeedEntry(repo, "app.name", "v1") // entry exists; no version published

	_, err := svc.Rollback(adminSvcCtx(), "app.name", 99)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrConfigNotFound, ec.Code,
		"missing version must return ErrConfigNotFound (mem repo) for 404 mapping")
}

func TestService_Publish_NonSensitiveEntry_VersionFlagFalse(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	mustSeedEntry(repo, "app.name", "gocell")

	ver, err := svc.Publish(adminSvcCtx(), "app.name")
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
			repo := mem.NewConfigRepository(clock.Real())
			svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(&testutil.NoopTxRunner{}))
			require.NoError(t, err)
			now := time.Now()
			require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
				ID: "cfg-x", Key: "app.x", Value: "v1", Sensitive: tt.seedSensitive,
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}))
			// Snapshot v1 with the seeded sensitivity.
			_, err = svc.Publish(adminSvcCtx(), "app.x")
			require.NoError(t, err)

			// Optionally flip the live entry's sensitivity to differ from the snapshot.
			if tt.flipToSensitiveAt > 0 {
				live, err := repo.GetByKey(context.Background(), "app.x")
				require.NoError(t, err)
				_, err = repo.UpdateForRollback(context.Background(), live.Key, "v-live", !tt.seedSensitive)
				require.NoError(t, err)
			}

			rolled, err := svc.Rollback(adminSvcCtx(), "app.x", 1)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSensitive, rolled.Sensitive,
				"rollback must inherit the snapshot's Sensitive flag, not the live entry's")
		})
	}
}

// --- S10 PublishFailureMode tests ---

// TestService_Publish_FailClosed_PublisherError verifies that when configured
// with PublishFailureMode=FailClosed (the default), a publisher error propagates
// as a hard failure.
func TestService_Publish_FailClosed_PublisherError(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	pub := testutil.FailingPublisher{Err: errors.New("broker down")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(newDirectTestEmitter(t, pub, outbox.DirectPublishFailClosed, slog.Default())),
		WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	mustSeedEntry(repo, "app.timeout", "30s")
	_, err = svc.Publish(adminSvcCtx(), "app.timeout")
	require.Error(t, err, "FailClosed: publisher failure must propagate")
	assert.Contains(t, err.Error(), "broker down")
}

// TestService_Publish_FailOpen_PublisherError verifies that when configured
// with PublishFailureMode=FailOpen, a publisher error is swallowed and logged
// rather than failing the entire Publish operation. Asserts the WARN signal
// is emitted (single-source const) so a future "silent swallow" regression
// fails CI rather than slipping through.
func TestService_Publish_FailOpen_PublisherError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	repo := mem.NewConfigRepository(clock.Real())
	pub := testutil.FailingPublisher{Err: errors.New("broker down")}
	svc, err := NewService(repo, logger, clock.Real(),
		WithEmitter(newDirectTestEmitter(t, pub, outbox.DirectPublishFailOpen, logger)),
		WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	mustSeedEntry(repo, "app.timeout", "30s")
	ver, err := svc.Publish(adminSvcCtx(), "app.timeout")
	require.NoError(t, err, "FailOpen: publisher failure must be swallowed")
	assert.Equal(t, 1, ver.Version)

	assert.Contains(t, buf.String(), outbox.WarnDirectPublishFailOpen,
		"FailOpen path must emit observable WARN; missing → silent swallow regression")
}

// TestService_Rollback_FailClosed_PublisherError verifies fail-closed on the
// Rollback path's direct-publisher fallback.
func TestService_Rollback_FailClosed_PublisherError(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	pub := testutil.FailingPublisher{Err: errors.New("broker down")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(newDirectTestEmitter(t, pub, outbox.DirectPublishFailClosed, slog.Default())),
		WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	mustSeedEntry(repo, "app.x", "v1")
	svcOK, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	_, err = svcOK.Publish(adminSvcCtx(), "app.x")
	require.NoError(t, err)

	_, err = svc.Rollback(adminSvcCtx(), "app.x", 1)
	require.Error(t, err, "FailClosed: publisher failure must propagate on rollback")
	assert.Contains(t, err.Error(), "broker down")
}

// TestService_Rollback_FailOpen_PublisherError verifies fail-open on the
// Rollback path's direct-publisher fallback. Rollback emits two events
// (entry-upserted + rollback); under FailOpen both publisher failures must
// produce WARN, so the assertion counts exactly 2 — a "first WARN then
// silent" regression would surface here.
func TestService_Rollback_FailOpen_PublisherError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	repo := mem.NewConfigRepository(clock.Real())
	pub := testutil.FailingPublisher{Err: errors.New("broker down")}
	svc, err := NewService(repo, logger, clock.Real(),
		WithEmitter(newDirectTestEmitter(t, pub, outbox.DirectPublishFailOpen, logger)),
		WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)

	mustSeedEntry(repo, "app.x", "v1")
	svcOK, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(&testutil.NoopTxRunner{}))
	require.NoError(t, err)
	_, err = svcOK.Publish(adminSvcCtx(), "app.x")
	require.NoError(t, err)

	rolled, err := svc.Rollback(adminSvcCtx(), "app.x", 1)
	require.NoError(t, err, "FailOpen: publisher failure must be swallowed on rollback")
	assert.Equal(t, "v1", rolled.Value)

	assert.Equal(t, 2, strings.Count(buf.String(), outbox.WarnDirectPublishFailOpen),
		"FailOpen rollback path emits two events; both must produce WARN to detect partial-silence regression")
}

// TestRollback_DurableMode_UpsertedPayloadIsMetadataOnly asserts that the
// entry-upserted event emitted during Rollback carries only key+version
// and does NOT include a "value" field (metadata-only contract, F-TEST-03).
func TestRollback_DurableMode_UpsertedPayloadIsMetadataOnly(t *testing.T) {
	svc, repo, writer := newDurableTestService(t)

	mustSeedEntry(repo, "app.name", "v1")

	// Publish first to create a version snapshot.
	_, err := svc.Publish(adminSvcCtx(), "app.name")
	require.NoError(t, err)
	writer.Entries = writer.Entries[:0] // reset writer after publish

	// Rollback to version 1.
	_, err = svc.Rollback(adminSvcCtx(), "app.name", 1)
	require.NoError(t, err)

	// Rollback emits two entries: [0]=entry-upserted, [1]=config-rollback.
	require.GreaterOrEqual(t, len(writer.Entries), 1, "Rollback must emit at least one outbox entry")

	// Find the entry-upserted entry.
	var upsertedPayload []byte
	for _, e := range writer.Entries {
		if e.EventType == domain.TopicConfigEntryUpserted {
			upsertedPayload = e.Payload
			break
		}
	}
	require.NotNil(t, upsertedPayload, "Rollback must emit a TopicConfigEntryUpserted entry")

	// Assert payload decodes correctly as metadata-only.
	decoded, decErr := events.DecodeEntryUpserted(upsertedPayload)
	require.NoError(t, decErr, "entry-upserted payload from Rollback must be valid")
	assert.Equal(t, "app.name", decoded.Key)

	// Assert no "value" field is present.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(upsertedPayload, &raw))
	_, hasValue := raw["value"]
	assert.False(t, hasValue, "entry-upserted payload must NOT contain 'value' field (metadata-only)")
}
