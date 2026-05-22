package devicestatus

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func newTestService(t testing.TB) (*Service, *mem.DeviceRepository) {
	t.Helper()
	repo := mem.NewDeviceRepository()
	svc, err := NewService(repo, slog.Default())
	if err != nil {
		t.Fatalf("newTestService: %v", err)
	}
	return svc, repo
}

func seedDevice(repo *mem.DeviceRepository, id, name, status string) {
	_ = repo.Create(context.Background(), &domain.Device{
		ID: id, Name: name, Status: status,
	})
}

// TestNewService_NilRepo verifies that NewService returns a non-nil error when
// required dependency repo is nil (bare nil or typed-nil).
func TestNewService_NilRepo(t *testing.T) {
	tests := []struct {
		name string
		repo domain.DeviceRepository
	}{
		{"bare nil", nil},
		{"typed nil", (domain.DeviceRepository)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewService(tt.repo, slog.Default())
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.KindInternal, ecErr.Kind)
		})
	}
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
			svc, repo := newTestService(t)
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
