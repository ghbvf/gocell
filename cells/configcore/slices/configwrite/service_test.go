package configwrite

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

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
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// adminSvcCtx returns a context with an admin principal for direct service calls.
func adminSvcCtx() context.Context {
	return auth.TestContext("test-admin", []string{"admin"})
}

func newTestService() *Service {
	repo := mem.NewConfigRepository(clock.Real())
	logger := slog.Default()
	svc, err := NewService(repo, logger, clock.Real(), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	if err != nil {
		panic("newTestService: " + err.Error())
	}
	return svc
}

func newDurableTestService(t testing.TB) (*Service, *mem.ConfigRepository, *testutil.RecordingWriter) {
	t.Helper()
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)
	return svc, repo, writer
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
			input:   UpdateInput{Key: "k", Value: "v2", ExpectedVersion: 1},
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
			err := svc.Delete(adminSvcCtx(), tt.key, 1)
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
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)

	_, err = svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
	require.Error(t, err, "Create must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Update_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	goodWriter := &testutil.RecordingWriter{}
	svcGood, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, goodWriter)), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)
	_, err = svcGood.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)

	failWriter := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, failWriter)), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)

	_, err = svc.Update(adminSvcCtx(), UpdateInput{Key: "k", Value: "v2", ExpectedVersion: 1})
	require.Error(t, err, "Update must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}

func TestService_Delete_OutboxWriteError(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	goodWriter := &testutil.RecordingWriter{}
	svcGood, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, goodWriter)), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)
	_, err = svcGood.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)

	failWriter := &testutil.RecordingWriter{Err: errors.New("outbox unavailable")}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, failWriter)), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)

	err = svc.Delete(adminSvcCtx(), "k", 1)
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
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(persistence.WrapForCell(tx)))
	require.NoError(t, err)

	_, err = svc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.Calls, "Create must call RunInTx exactly once")
	assert.Len(t, writer.Entries, 1, "outbox entry must be written inside the tx")
}

// TestUpdate_CallsTxRunnerRunInTxOnce asserts that Update wraps repo.Update
// write and outbox write inside a single RunInTx call (L2 atomicity).
func TestUpdate_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(persistence.WrapForCell(tx)))
	require.NoError(t, err)

	// Seed via direct repo insert (bypasses service tx counter).
	seedSvc, seedErr := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, seedErr)
	_, _ = seedSvc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})

	tx.Calls = 0 // reset counter after seed
	_, err = svc.Update(adminSvcCtx(), UpdateInput{Key: "k", Value: "v2", ExpectedVersion: 1})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.Calls, "Update must call RunInTx exactly once")
}

// TestDelete_CallsTxRunnerRunInTxOnce asserts that Delete wraps repo.Delete
// (which returns the deleted entry via RETURNING) and outbox write inside a
// single RunInTx call (L2 atomicity — no pre-fetch outside the tx).
func TestDelete_CallsTxRunnerRunInTxOnce(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	writer := &testutil.RecordingWriter{}
	tx := &testutil.NoopTxRunner{}
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(persistence.WrapForCell(tx)))
	require.NoError(t, err)

	// Seed via direct repo insert (bypasses service tx counter).
	seedSvc, seedErr := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, seedErr)
	_, _ = seedSvc.Create(adminSvcCtx(), CreateInput{Key: "k", Value: "v1"})

	tx.Calls = 0 // reset counter after seed
	err = svc.Delete(adminSvcCtx(), "k", 1)
	require.NoError(t, err)
	assert.Equal(t, 1, tx.Calls, "Delete must call RunInTx exactly once")
}

// TestService_Create_PublishError_DoesNotFailCreate verifies that demo-mode
// publisher failure in Service.publishChange is logged but does not propagate
// as an error — covering the warn-log branch introduced when the direct
// publish path was wrapped in a v1 envelope (P1-14 follow-up).
func TestService_Create_PublishError_DoesNotFailCreate(t *testing.T) {
	repo := mem.NewConfigRepository(clock.Real())
	fp := testutil.FailingPublisher{Err: errors.New("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "configcore",
		outbox.WithLogger(slog.Default()))
	require.NoError(t, err)
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithEmitter(emitter), WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)

	entry, err := svc.Create(adminSvcCtx(), CreateInput{Key: "pub-err", Value: "v"})
	require.NoError(t, err, "publish failure in demo mode must not fail Create")
	assert.Equal(t, "pub-err", entry.Key)
}

// concurrentSafeTxRunner is a stateless pass-through TxRunner safe for concurrent use.
// Unlike testutil.NoopTxRunner it has no mutable Calls field, avoiding data races.
type concurrentSafeTxRunner struct{}

func (concurrentSafeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// TestConcurrentUpdate_ExactlyOneSucceeds verifies that when two goroutines race
// to update the same config entry with the same expectedVersion, exactly one
// succeeds and the other receives ErrVersionConflict.
func TestConcurrentUpdate_ExactlyOneSucceeds(t *testing.T) {
	t.Parallel()

	repo := mem.NewConfigRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(concurrentSafeTxRunner{})))
	require.NoError(t, err)
	_, err = svc.Create(adminSvcCtx(), CreateInput{Key: "cas-race-key", Value: "initial"})
	require.NoError(t, err)

	var (
		successes        atomic.Int32
		versionConflicts atomic.Int32
	)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		newVal := "value-A"
		if i == 1 {
			newVal = "value-B"
		}
		go func(newVal string) {
			defer wg.Done()
			_, upErr := svc.Update(adminSvcCtx(), UpdateInput{
				Key:             "cas-race-key",
				Value:           newVal,
				ExpectedVersion: 1,
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
		}(newVal)
	}
	wg.Wait()

	assert.Equal(t, int32(1), successes.Load(), "exactly one concurrent Update must succeed")
	assert.Equal(t, int32(1), versionConflicts.Load(), "exactly one concurrent Update must yield ErrVersionConflict")
}

// TestConcurrentDelete_ExactlyOneSucceeds verifies that when two goroutines race
// to delete the same config entry with the same expectedVersion, exactly one
// succeeds. The loser receives either ErrVersionConflict or ErrConfigNotFound
// (when the winner committed the delete before the loser's CAS check).
func TestConcurrentDelete_ExactlyOneSucceeds(t *testing.T) {
	t.Parallel()

	repo := mem.NewConfigRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(persistence.WrapForCell(concurrentSafeTxRunner{})))
	require.NoError(t, err)
	_, err = svc.Create(adminSvcCtx(), CreateInput{Key: "cas-delete-race-key", Value: "initial"})
	require.NoError(t, err)

	var (
		successes atomic.Int32
		losers    atomic.Int32
	)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			delErr := svc.Delete(adminSvcCtx(), "cas-delete-race-key", 1)
			if delErr == nil {
				successes.Add(1)
			} else {
				var ce *errcode.Error
				if errors.As(delErr, &ce) &&
					(ce.Code == errcode.ErrVersionConflict || ce.Code == errcode.ErrConfigNotFound) {
					losers.Add(1)
				} else {
					t.Errorf("unexpected error in concurrent Delete: %v", delErr)
				}
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), successes.Load(), "exactly one concurrent Delete must succeed")
	assert.Equal(t, int32(1), losers.Load(), "exactly one concurrent Delete must yield ErrVersionConflict or ErrConfigNotFound")
}
