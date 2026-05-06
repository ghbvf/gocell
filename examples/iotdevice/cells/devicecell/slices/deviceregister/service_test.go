package deviceregister

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
)

// failPublisher is a Publisher that always returns an error.
type failPublisher struct{}

func (failPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return errors.New("publish failed")
}
func (failPublisher) Close(_ context.Context) error { return nil }

func newTestService() (*Service, *mem.DeviceRepository) {
	repo := mem.NewDeviceRepository()
	return NewService(repo, slog.Default(), WithClock(clock.Real())), repo
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
			svc, _ := newTestService()

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
	svc, repo := newTestService()
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
	svc := NewService(repo, slog.Default(), WithEmitter(emitter), WithClock(clock.Real()))

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
	svc := NewService(repo, slog.Default(), WithEmitter(emitter), WithClock(clock.Real()))

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
	svc := NewService(repo, logger, WithEmitter(emitter), WithClock(clock.Real()))

	resp, err := svc.Register(context.Background(), &registercontract.Request{Name: "sensor-log"})
	require.NoError(t, err)
	require.NotNil(t, resp)

	logOutput := logBuf.String()
	warnEntry := sloghelper.FindLogEntry(logOutput, "direct publish failed")
	require.NotNil(t, warnEntry, "expected warn log for fail-open publish miss")
	assert.Nil(t, sloghelper.FindLogEntry(logOutput, "event published"),
		"fail-open path must not log a false published-success message")
}

func TestService_Register_DuplicateID_IsUnlikelyButHandled(t *testing.T) {
	// Since uuid.NewString generates random IDs, duplicate is practically
	// impossible. We verify two sequential calls succeed without collision.
	svc, _ := newTestService()
	ctx := context.Background()

	resp1, err := svc.Register(ctx, &registercontract.Request{Name: "dev-1"})
	require.NoError(t, err)
	resp2, err := svc.Register(ctx, &registercontract.Request{Name: "dev-2"})
	require.NoError(t, err)
	r1 := resp1.(registercontract.Register201JSONResponse)
	r2 := resp2.(registercontract.Register201JSONResponse)
	assert.NotEqual(t, r1.Data.ID, r2.Data.ID)
}
