package devicecommand

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCodec() *query.CursorCodec {
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	return codec
}

func newTestService() (*Service, *mem.DeviceRepository, *mem.CommandRepository) {
	devRepo := mem.NewDeviceRepository()
	cmdRepo := mem.NewCommandRepository()
	return NewService(cmdRepo, devRepo, testCodec(), slog.Default(), query.RunModeProd), devRepo, cmdRepo
}

func seedDevice(repo *mem.DeviceRepository, id, name string) {
	_ = repo.Create(context.Background(), &domain.Device{
		ID: id, Name: name, Status: "online",
	})
}

func TestService_Enqueue(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*mem.DeviceRepository)
		deviceID string
		payload  string
		wantErr  bool
		checkCmd func(t *testing.T, cmd *domain.Command)
	}{
		{
			name:     "valid enqueue",
			setup:    func(r *mem.DeviceRepository) { seedDevice(r, "dev-1", "sensor-a") },
			deviceID: "dev-1",
			payload:  "reboot",
			wantErr:  false,
			checkCmd: func(t *testing.T, cmd *domain.Command) {
				assert.NotEmpty(t, cmd.ID)
				assert.Equal(t, "dev-1", cmd.DeviceID)
				assert.Equal(t, "reboot", cmd.Payload)
				assert.Equal(t, "pending", cmd.Status)
				assert.False(t, cmd.CreatedAt.IsZero())
			},
		},
		{
			name:     "non-existent device returns error",
			setup:    func(_ *mem.DeviceRepository) {},
			deviceID: "dev-missing",
			payload:  "reboot",
			wantErr:  true,
		},
		{
			name:     "empty payload returns validation error",
			setup:    func(r *mem.DeviceRepository) { seedDevice(r, "dev-2", "sensor-b") },
			deviceID: "dev-2",
			payload:  "",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, devRepo, _ := newTestService()
			tc.setup(devRepo)

			cmd, err := svc.Enqueue(context.Background(), tc.deviceID, tc.payload)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, cmd)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cmd)
				if tc.checkCmd != nil {
					tc.checkCmd(t, cmd)
				}
			}
		})
	}
}

func TestService_ListPending(t *testing.T) {
	svc, devRepo, cmdRepo := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")
	seedDevice(devRepo, "dev-2", "sensor-b")

	// Enqueue 2 commands for dev-1 and 1 for dev-2.
	_ = cmdRepo.Create(ctx, &domain.Command{ID: "c1", DeviceID: "dev-1", Payload: "a", Status: "pending"})
	_ = cmdRepo.Create(ctx, &domain.Command{ID: "c2", DeviceID: "dev-1", Payload: "b", Status: "pending"})
	_ = cmdRepo.Create(ctx, &domain.Command{ID: "c3", DeviceID: "dev-2", Payload: "c", Status: "pending"})

	tests := []struct {
		name     string
		deviceID string
		wantLen  int
		wantErr  bool
	}{
		{name: "dev-1 has 2 pending", deviceID: "dev-1", wantLen: 2},
		{name: "dev-2 has 1 pending", deviceID: "dev-2", wantLen: 1},
		{name: "non-existent device returns error", deviceID: "dev-missing", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := svc.ListPending(ctx, tc.deviceID, query.PageRequest{})
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, result.Items, tc.wantLen)
			}
		})
	}
}

func TestService_Ack(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*mem.DeviceRepository, *mem.CommandRepository)
		deviceID string
		cmdID    string
		wantErr  bool
	}{
		{
			name: "ack pending command",
			setup: func(dr *mem.DeviceRepository, cr *mem.CommandRepository) {
				seedDevice(dr, "dev-1", "sensor-a")
				_ = cr.Create(context.Background(), &domain.Command{
					ID: "cmd-1", DeviceID: "dev-1", Payload: "reboot", Status: "pending",
				})
			},
			deviceID: "dev-1",
			cmdID:    "cmd-1",
			wantErr:  false,
		},
		{
			name: "ack non-existent command returns error",
			setup: func(dr *mem.DeviceRepository, _ *mem.CommandRepository) {
				seedDevice(dr, "dev-1", "sensor-a")
			},
			deviceID: "dev-1",
			cmdID:    "cmd-missing",
			wantErr:  true,
		},
		{
			name: "ack with wrong device returns error",
			setup: func(dr *mem.DeviceRepository, cr *mem.CommandRepository) {
				seedDevice(dr, "dev-1", "sensor-a")
				seedDevice(dr, "dev-2", "sensor-b")
				_ = cr.Create(context.Background(), &domain.Command{
					ID: "cmd-2", DeviceID: "dev-1", Payload: "x", Status: "pending",
				})
			},
			deviceID: "dev-2",
			cmdID:    "cmd-2",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, devRepo, cmdRepo := newTestService()
			tc.setup(devRepo, cmdRepo)

			err := svc.Ack(context.Background(), tc.deviceID, tc.cmdID)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestService_ListPending_CursorDeviceMismatch(t *testing.T) {
	// Create a cursor for device-A, then use it on device-B.
	// The cursor should be rejected.
	svc, devRepo, cmdRepo := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-A", "sensor-a")
	seedDevice(devRepo, "dev-B", "sensor-b")

	// Enqueue enough commands for dev-A so a cursor is generated.
	for i := 0; i < 5; i++ {
		_ = cmdRepo.Create(ctx, &domain.Command{
			ID: fmt.Sprintf("c%d", i), DeviceID: "dev-A", Payload: "x", Status: "pending",
		})
	}

	// Get first page for dev-A.
	page1, err := svc.ListPending(ctx, "dev-A", query.PageRequest{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	// Replay the cursor against dev-B — must fail with context mismatch.
	_, err = svc.ListPending(ctx, "dev-B", query.PageRequest{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "query context mismatch", ecErr.Details["reason"])
}

func TestService_Enqueue_ThenListPending_ThenAck(t *testing.T) {
	svc, devRepo, _ := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")

	// Enqueue
	cmd, err := svc.Enqueue(ctx, "dev-1", "upgrade-fw")
	require.NoError(t, err)

	// List pending should include the command.
	result, err := svc.ListPending(ctx, "dev-1", query.PageRequest{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 1)
	assert.Equal(t, cmd.ID, result.Items[0].ID)

	// Ack
	require.NoError(t, svc.Ack(ctx, "dev-1", cmd.ID))

	// List pending should be empty after ack.
	result, err = svc.ListPending(ctx, "dev-1", query.PageRequest{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
}
