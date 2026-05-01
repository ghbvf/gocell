package configwrite

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/internal/testoutbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
)

// adminSvcCtx returns a context with an admin principal for direct service calls.
func adminSvcCtx() context.Context {
	return auth.TestContext("test-admin", []string{"admin"})
}

func newTestService() *Service {
	repo := mem.NewConfigRepository()
	logger := slog.Default()
	return NewService(repo, logger)
}

func newDurableTestService(t testing.TB) (*Service, *mem.ConfigRepository, *testutil.RecordingWriter) {
	t.Helper()
	repo := mem.NewConfigRepository()
	writer := &testutil.RecordingWriter{}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))
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
			svc := newTestService()
			entry, err := svc.Create(adminSvcCtx(), tt.input)
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
	svc := newTestService()
	_, err := svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)

	_, err = svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v2"})
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
				_, _ = svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})
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
			svc := newTestService()
			tt.setup(svc)
			entry, err := svc.Update(adminSvcCtx(), tt.input)
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
				_, _ = svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
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
			svc := newTestService()
			tt.setup(svc)
			err := svc.Delete(adminSvcCtx(), tt.key)
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
	writer := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(&testutil.NoopTxRunner{}))

	_, err := svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
	require.Error(t, err, "Create must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Update_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository()
	goodWriter := &testutil.RecordingWriter{}
	svcGood := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, goodWriter)), WithTxManager(&testutil.NoopTxRunner{}))
	_, err := svcGood.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)

	failWriter := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, failWriter)), WithTxManager(&testutil.NoopTxRunner{}))

	_, err = svc.Update(adminSvcCtx(), UpdateInput{Key: "k", Value: "v2"})
	require.Error(t, err, "Update must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Delete_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository()
	goodWriter := &testutil.RecordingWriter{}
	svcGood := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, goodWriter)), WithTxManager(&testutil.NoopTxRunner{}))
	_, err := svcGood.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)

	failWriter := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, failWriter)), WithTxManager(&testutil.NoopTxRunner{}))

	err = svc.Delete(adminSvcCtx(), "k")
	require.Error(t, err, "Delete must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Create_DurableMode_CapturesOutboxEntry(t *testing.T) {
	svc, _, writer := newDurableTestService(t)

	entry, err := svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, "k", entry.Key)
	require.Len(t, writer.Entries, 1)
	assert.Equal(t, domain.TopicConfigEntryUpserted, writer.Entries[0].EventType)

	// F-TEST-02: assert payload is metadata-only — no "value" field.
	decoded, decErr := events.DecodeEntryUpserted(writer.Entries[0].Payload)
	require.NoError(t, decErr, "outbox payload must decode as valid entry-upserted")
	assert.Equal(t, "k", decoded.Key)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(writer.Entries[0].Payload, &raw))
	_, hasValue := raw["value"]
	assert.False(t, hasValue, "entry-upserted payload must NOT contain 'value' field (metadata-only)")
}

// TestCreate_CallsTxRunnerRunInTxOnce asserts that Create wraps both the repo
// write and outbox write inside a single RunInTx call (L2 atomicity).
func TestCreate_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(tx))

	_, err := svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.Calls, "Create must call RunInTx exactly once")
	assert.Len(t, writer.Entries, 1, "outbox entry must be written inside the tx")
}

// TestUpdate_CallsTxRunnerRunInTxOnce asserts that Update wraps repo.Update
// write and outbox write inside a single RunInTx call (L2 atomicity).
func TestUpdate_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(tx))

	// Seed via direct repo insert (bypasses service tx counter).
	_, _ = NewService(repo, slog.Default()).Create(
		adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})

	tx.Calls = 0 // reset counter after seed
	_, err := svc.Update(adminSvcCtx(), UpdateInput{Key: "k", Value: "v2"})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.Calls, "Update must call RunInTx exactly once")
}

// TestDelete_CallsTxRunnerRunInTxOnce asserts that Delete wraps repo.Delete
// (which returns the deleted entry via RETURNING) and outbox write inside a
// single RunInTx call (L2 atomicity — no pre-fetch outside the tx).
func TestDelete_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository()
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc := NewService(repo, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(tx))

	// Seed via direct repo insert (bypasses service tx counter).
	_, _ = NewService(repo, slog.Default()).Create(
		adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})

	tx.Calls = 0 // reset counter after seed
	err := svc.Delete(adminSvcCtx(), "k")
	require.NoError(t, err)
	assert.Equal(t, 1, tx.Calls, "Delete must call RunInTx exactly once")
}

// TestService_Create_PublishError_DoesNotFailCreate verifies that demo-mode
// publisher failure in Service.publishChange is logged but does not propagate
// as an error — covering the warn-log branch introduced when the direct
// publish path was wrapped in a v1 envelope (P1-14 follow-up).
func TestService_Create_PublishError_DoesNotFailCreate(t *testing.T) {
	repo := mem.NewConfigRepository()
	fp := testutil.FailingPublisher{Err: errors.New("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, "configcore",
		outbox.WithLogger(slog.Default()))
	require.NoError(t, err)
	svc := NewService(repo, slog.Default(), WithEmitter(emitter))

	entry, err := svc.Create(adminSvcCtx(), CreateInput{Key: "pub-err", Value: "v"})
	require.NoError(t, err, "publish failure in demo mode must not fail Create")
	assert.Equal(t, "pub-err", entry.Key)
}
