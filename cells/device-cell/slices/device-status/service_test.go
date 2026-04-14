package devicestatus

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.DeviceRepository) {
	repo := mem.NewDeviceRepository()
	return NewService(repo, slog.Default()), repo
}

func seedDevice(repo *mem.DeviceRepository, id, name, status string) {
	_ = repo.Create(context.Background(), &domain.Device{
		ID: id, Name: name, Status: status,
	})
}

func TestService_GetStatus(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mem.DeviceRepository)
		id      string
		wantErr bool
		check   func(t *testing.T, dev *domain.Device)
	}{
		{
			name: "existing device returns status",
			setup: func(r *mem.DeviceRepository) {
				seedDevice(r, "dev-1", "sensor-a", "online")
			},
			id:      "dev-1",
			wantErr: false,
			check: func(t *testing.T, dev *domain.Device) {
				assert.Equal(t, "dev-1", dev.ID)
				assert.Equal(t, "sensor-a", dev.Name)
				assert.Equal(t, "online", dev.Status)
			},
		},
		{
			name:    "non-existent device returns error",
			setup:   func(_ *mem.DeviceRepository) {},
			id:      "dev-missing",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newTestService()
			tc.setup(repo)

			dev, err := svc.GetStatus(context.Background(), tc.id)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, dev)
			} else {
				require.NoError(t, err)
				require.NotNil(t, dev)
				if tc.check != nil {
					tc.check(t, dev)
				}
			}
		})
	}
}
