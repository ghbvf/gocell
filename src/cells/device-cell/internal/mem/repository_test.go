package mem

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// DeviceRepository
// ---------------------------------------------------------------------------

func TestDeviceRepository_Create(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*DeviceRepository)
		device  *domain.Device
		wantErr bool
	}{
		{
			name:  "creates new device",
			setup: func(_ *DeviceRepository) {},
			device: &domain.Device{
				ID: "dev-1", Name: "sensor-a", Status: "online",
				LastSeen: time.Now(),
			},
			wantErr: false,
		},
		{
			name: "duplicate ID returns error",
			setup: func(r *DeviceRepository) {
				_ = r.Create(context.Background(), &domain.Device{
					ID: "dev-dup", Name: "first", Status: "online",
				})
			},
			device: &domain.Device{
				ID: "dev-dup", Name: "second", Status: "online",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewDeviceRepository()
			tc.setup(repo)

			err := repo.Create(context.Background(), tc.device)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDeviceRepository_GetByID(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*DeviceRepository)
		id       string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name: "existing device",
			setup: func(r *DeviceRepository) {
				_ = r.Create(context.Background(), &domain.Device{
					ID: "dev-1", Name: "sensor-a", Status: "online",
				})
			},
			id:      "dev-1",
			wantErr: false,
		},
		{
			name:     "non-existent device returns error",
			setup:    func(_ *DeviceRepository) {},
			id:       "dev-missing",
			wantErr:  true,
			wantCode: errcode.ErrDeviceNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewDeviceRepository()
			tc.setup(repo)

			device, err := repo.GetByID(context.Background(), tc.id)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, device)
				if tc.wantCode != "" {
					var ecErr *errcode.Error
					require.ErrorAs(t, err, &ecErr)
					assert.Equal(t, tc.wantCode, ecErr.Code)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.id, device.ID)
			}
		})
	}
}

func TestDeviceRepository_GetByID_ReturnsCopy(t *testing.T) {
	repo := NewDeviceRepository()
	ctx := context.Background()
	_ = repo.Create(ctx, &domain.Device{
		ID: "dev-copy", Name: "original", Status: "online",
	})

	d1, _ := repo.GetByID(ctx, "dev-copy")
	d1.Name = "mutated"

	d2, _ := repo.GetByID(ctx, "dev-copy")
	assert.Equal(t, "original", d2.Name, "mutation should not affect stored copy")
}

// ---------------------------------------------------------------------------
// CommandRepository
// ---------------------------------------------------------------------------

func TestCommandRepository_Create(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*CommandRepository)
		cmd     *domain.Command
		wantErr bool
	}{
		{
			name:  "creates new command",
			setup: func(_ *CommandRepository) {},
			cmd: &domain.Command{
				ID: "cmd-1", DeviceID: "dev-1", Payload: "reboot",
				Status: "pending", CreatedAt: time.Now(),
			},
			wantErr: false,
		},
		{
			name: "duplicate ID returns error",
			setup: func(r *CommandRepository) {
				_ = r.Create(context.Background(), &domain.Command{
					ID: "cmd-dup", DeviceID: "dev-1", Payload: "p",
					Status: "pending", CreatedAt: time.Now(),
				})
			},
			cmd: &domain.Command{
				ID: "cmd-dup", DeviceID: "dev-2", Payload: "q",
				Status: "pending", CreatedAt: time.Now(),
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewCommandRepository()
			tc.setup(repo)

			err := repo.Create(context.Background(), tc.cmd)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCommandRepository_ListPending(t *testing.T) {
	ctx := context.Background()
	repo := NewCommandRepository()

	// Seed: 2 pending for dev-1, 1 acked for dev-1, 1 pending for dev-2.
	_ = repo.Create(ctx, &domain.Command{ID: "c1", DeviceID: "dev-1", Payload: "a", Status: "pending"})
	_ = repo.Create(ctx, &domain.Command{ID: "c2", DeviceID: "dev-1", Payload: "b", Status: "pending"})
	_ = repo.Create(ctx, &domain.Command{ID: "c3", DeviceID: "dev-1", Payload: "c", Status: "acked"})
	_ = repo.Create(ctx, &domain.Command{ID: "c4", DeviceID: "dev-2", Payload: "d", Status: "pending"})

	tests := []struct {
		name     string
		deviceID string
		wantLen  int
	}{
		{name: "dev-1 has 2 pending", deviceID: "dev-1", wantLen: 2},
		{name: "dev-2 has 1 pending", deviceID: "dev-2", wantLen: 1},
		{name: "unknown device has 0", deviceID: "dev-none", wantLen: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmds, err := repo.ListPending(ctx, tc.deviceID)
			require.NoError(t, err)
			assert.Len(t, cmds, tc.wantLen)
		})
	}
}

func TestCommandRepository_Ack(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		setup    func(*CommandRepository)
		deviceID string
		cmdID    string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name: "ack pending command",
			setup: func(r *CommandRepository) {
				_ = r.Create(ctx, &domain.Command{
					ID: "cmd-1", DeviceID: "dev-1", Payload: "reboot", Status: "pending",
				})
			},
			deviceID: "dev-1",
			cmdID:    "cmd-1",
			wantErr:  false,
		},
		{
			name: "ack already acked is idempotent",
			setup: func(r *CommandRepository) {
				_ = r.Create(ctx, &domain.Command{
					ID: "cmd-2", DeviceID: "dev-1", Payload: "reboot", Status: "pending",
				})
				_ = r.Ack(ctx, "dev-1", "cmd-2")
			},
			deviceID: "dev-1",
			cmdID:    "cmd-2",
			wantErr:  false,
		},
		{
			name:     "non-existent command returns error",
			setup:    func(_ *CommandRepository) {},
			deviceID: "dev-1",
			cmdID:    "cmd-missing",
			wantErr:  true,
			wantCode: errcode.ErrCommandNotFound,
		},
		{
			name: "wrong device returns error",
			setup: func(r *CommandRepository) {
				_ = r.Create(ctx, &domain.Command{
					ID: "cmd-3", DeviceID: "dev-1", Payload: "x", Status: "pending",
				})
			},
			deviceID: "dev-other",
			cmdID:    "cmd-3",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewCommandRepository()
			tc.setup(repo)

			err := repo.Ack(ctx, tc.deviceID, tc.cmdID)
			if tc.wantErr {
				assert.Error(t, err)
				if tc.wantCode != "" {
					var ecErr *errcode.Error
					require.ErrorAs(t, err, &ecErr)
					assert.Equal(t, tc.wantCode, ecErr.Code)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCommandRepository_Ack_SetsAckedAt(t *testing.T) {
	ctx := context.Background()
	repo := NewCommandRepository()
	_ = repo.Create(ctx, &domain.Command{
		ID: "cmd-ts", DeviceID: "dev-1", Payload: "x", Status: "pending",
	})

	before := time.Now()
	require.NoError(t, repo.Ack(ctx, "dev-1", "cmd-ts"))
	after := time.Now()

	// Verify status changed and AckedAt set.
	cmds, _ := repo.ListPending(ctx, "dev-1")
	assert.Empty(t, cmds, "acked command should not appear in pending list")

	// Access the internal state to verify AckedAt.
	repo.mu.RLock()
	cmd := repo.commands["cmd-ts"]
	repo.mu.RUnlock()

	assert.Equal(t, "acked", cmd.Status)
	require.NotNil(t, cmd.AckedAt)
	assert.True(t, !cmd.AckedAt.Before(before) && !cmd.AckedAt.After(after))
}
