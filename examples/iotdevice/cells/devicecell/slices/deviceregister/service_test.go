package deviceregister

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
)

// stepClock advances a fixed step on every Now() call. A value captured from an
// earlier Now() (device.LastSeen) is therefore strictly less than a later
// default-stamped Now() (NewEntry's occurredAt/createdAt fallback), which is
// what makes the OccurredAt assertion a real regression guard against dropping
// WithOccurredAt(device.LastSeen) in registerInternal.
type stepClock struct {
	*clockmock.FakeClock
	step time.Duration
}

func (c *stepClock) Now() time.Time {
	now := c.FakeClock.Now() // explicit embedded selector: c.Now() would recurse
	c.Advance(c.step)
	return now
}

// failPublisher is a Publisher that always returns an error.
type failPublisher struct{}

func (failPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return errors.New("publish failed")
}
func (failPublisher) Close(_ context.Context) error { return nil }

func newTestService(t testing.TB) (*Service, *mem.DeviceRepository) {
	t.Helper()
	repo := mem.NewDeviceRepository()
	svc, err := NewService(clock.Real(), repo, slog.Default())
	if err != nil {
		t.Fatalf("newTestService: %v", err)
	}
	return svc, repo
}

// TestNewService_NilRepo verifies that NewService returns a non-nil error when
// required dependency repo is nil.
func TestNewService_NilRepo(t *testing.T) {
	tests := []struct {
		name string
		repo domain.DeviceRepository
	}{
		{"bare nil", nil},
		// Real typed-nil: a nil *mem.DeviceRepository boxed into the interface
		// is a non-nil interface wrapping a nil pointer. bare `== nil` would
		// miss it; validation.IsNilInterface (used by the generated guard for
		// interface fields) catches it.
		{"typed nil", (*mem.DeviceRepository)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewService(clock.Real(), tt.repo, slog.Default())
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.KindInternal, ecErr.Kind)
		})
	}
}

func TestService_Register(t *testing.T) {
	tests := []struct {
		name       string
		deviceName string
		wantErr    bool
		checkResp  func(t *testing.T, resp registercontract.Register201JSONResponse)
	}{
		{
			name:       "valid registration",
			deviceName: "sensor-a",
			wantErr:    false,
			checkResp: func(t *testing.T, resp registercontract.Register201JSONResponse) {
				require.NotNil(t, resp.Data)
				assert.NotEmpty(t, resp.Data.ID)
				assert.Equal(t, "sensor-a", resp.Data.Name)
				assert.Equal(t, "online", resp.Data.Status)
			},
		},
		{
			name:       "empty name returns validation error",
			deviceName: "",
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService(t)

			resp, err := svc.Register(context.Background(), &registercontract.Request{Name: tc.deviceName})
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, resp)
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				if tc.checkResp != nil {
					tc.checkResp(t, resp.(registercontract.Register201JSONResponse))
				}
			}
		})
	}
}

func TestService_Register_PersistsDevice(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()

	resp, err := svc.Register(ctx, &registercontract.Request{Name: "sensor-b"})
	require.NoError(t, err)
	r := resp.(registercontract.Register201JSONResponse)
	require.NotNil(t, r.Data)

	stored, err := repo.GetByID(ctx, r.Data.ID)
	require.NoError(t, err)
	assert.Equal(t, r.Data.ID, stored.ID)
	assert.Equal(t, "sensor-b", stored.Name)
}

func TestService_Register_PublishFails_StillReturnsDevice(t *testing.T) {
	repo := mem.NewDeviceRepository()
	emitter, err := outbox.NewDirectEmitter(
		failPublisher{}, outbox.DirectPublishFailOpen,
		metrics.NopProvider{}, clock.Real(), "devicecell", outbox.WithLogger(slog.Default()),
	)
	require.NoError(t, err)
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithEmitter(outbox.WrapEmitterForCell(emitter)))
	require.NoError(t, err)

	resp, err := svc.Register(context.Background(), &registercontract.Request{Name: "sensor-c"})
	require.NoError(t, err, "publish failure should not propagate as error")
	require.NotNil(t, resp)
	r := resp.(registercontract.Register201JSONResponse)
	require.NotNil(t, r.Data)
	assert.NotEmpty(t, r.Data.ID)
}

func TestService_Register_PublishFails_FailClosedReturnsError(t *testing.T) {
	repo := mem.NewDeviceRepository()
	emitter, err := outbox.NewDirectEmitter(
		failPublisher{}, outbox.DirectPublishFailClosed,
		metrics.NopProvider{}, clock.Real(), "devicecell", outbox.WithLogger(slog.Default()),
	)
	require.NoError(t, err)
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithEmitter(outbox.WrapEmitterForCell(emitter)))
	require.NoError(t, err)

	resp, err := svc.Register(context.Background(), &registercontract.Request{Name: "sensor-c"})
	require.Error(t, err, "fail-closed publish failure must propagate")
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "emit event")
	assert.Contains(t, err.Error(), "publish failed")
}

func TestService_Register_FailOpenDoesNotLogPublished(t *testing.T) {
	repo := mem.NewDeviceRepository()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	emitter, err := outbox.NewDirectEmitter(
		failPublisher{}, outbox.DirectPublishFailOpen,
		metrics.NopProvider{}, clock.Real(), "devicecell", outbox.WithLogger(logger),
	)
	require.NoError(t, err)
	svc, err := NewService(clock.Real(), repo, logger, WithEmitter(outbox.WrapEmitterForCell(emitter)))
	require.NoError(t, err)

	resp, err := svc.Register(context.Background(), &registercontract.Request{Name: "sensor-log"})
	require.NoError(t, err)
	require.NotNil(t, resp)

	logOutput := logBuf.String()
	warnEntry := sloghelper.FindLogEntry(logOutput, "direct publish failed")
	require.NotNil(t, warnEntry, "expected warn log for fail-open publish miss")
	assert.Nil(t, sloghelper.FindLogEntry(logOutput, "event published"),
		"fail-open path must not log a false published-success message")
}

// TestService_Register_StampsOccurredAtFromDeviceLastSeen locks the event
// envelope semantics: the published device.registered event must carry
// OccurredAt == the device's registration instant (device.LastSeen), not the
// entry seal time. With the step clock advancing on each Now(), dropping the
// WithOccurredAt(device.LastSeen) option would default occurredAt to a later
// Now() inside NewEntry and fail both assertions below.
func TestService_Register_StampsOccurredAtFromDeviceLastSeen(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 600, time.UTC)
	clk := &stepClock{FakeClock: clockmock.New(base), step: time.Second}
	repo := mem.NewDeviceRepository()
	recorder := outboxtest.NewRecorder()
	svc, err := NewService(clk, repo, slog.Default(), WithEmitter(recorder.CellEmitter()))
	require.NoError(t, err)

	ctx := context.Background()
	resp, err := svc.Register(ctx, &registercontract.Request{Name: "sensor-occurred"})
	require.NoError(t, err)
	r := resp.(registercontract.Register201JSONResponse)
	require.NotNil(t, r.Data)

	stored, err := repo.GetByID(ctx, r.Data.ID)
	require.NoError(t, err)

	entries := recorder.Entries()
	require.Len(t, entries, 1)
	got := entries[0]

	assert.True(t, got.OccurredAt().Equal(stored.LastSeen),
		"event OccurredAt (%s) must equal device LastSeen (%s) — WithOccurredAt(device.LastSeen) regression",
		got.OccurredAt(), stored.LastSeen)
	// The seal time is stamped from a later Now(), proving occurredAt and
	// createdAt are independently carried (not collapsed to the seal instant).
	assert.True(t, got.CreatedAt().After(got.OccurredAt()),
		"CreatedAt (%s) should be strictly later than OccurredAt (%s)",
		got.CreatedAt(), got.OccurredAt())
}

func TestService_Register_DuplicateID_IsUnlikelyButHandled(t *testing.T) {
	// Since uuid.NewString generates random IDs, duplicate is practically
	// impossible. We verify two sequential calls succeed without collision.
	svc, _ := newTestService(t)
	ctx := context.Background()

	resp1, err := svc.Register(ctx, &registercontract.Request{Name: "dev-1"})
	require.NoError(t, err)
	resp2, err := svc.Register(ctx, &registercontract.Request{Name: "dev-2"})
	require.NoError(t, err)
	r1 := resp1.(registercontract.Register201JSONResponse)
	r2 := resp2.(registercontract.Register201JSONResponse)
	assert.NotEqual(t, r1.Data.ID, r2.Data.ID)
}
