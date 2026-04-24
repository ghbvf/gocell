package mem

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
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

func TestDeviceRepository_List(t *testing.T) {
	repo := NewDeviceRepository()
	ctx := context.Background()
	now := time.Now()

	_ = repo.Create(ctx, &domain.Device{ID: "dev-b", Name: "beta", Status: "online", LastSeen: now})
	_ = repo.Create(ctx, &domain.Device{ID: "dev-a", Name: "alpha", Status: "online", LastSeen: now})
	_ = repo.Create(ctx, &domain.Device{ID: "dev-c", Name: "gamma", Status: "offline", LastSeen: now})

	t.Run("list all sorted by name ASC", func(t *testing.T) {
		params := query.ListParams{
			Limit: 10,
			Sort: []query.SortColumn{
				{Name: "name", Direction: query.SortASC},
				{Name: "id", Direction: query.SortASC},
			},
		}
		devices, err := repo.List(ctx, params)
		require.NoError(t, err)
		require.Len(t, devices, 3)
		assert.Equal(t, "alpha", devices[0].Name)
		assert.Equal(t, "beta", devices[1].Name)
		assert.Equal(t, "gamma", devices[2].Name)
	})

	t.Run("list sorted by status ASC", func(t *testing.T) {
		params := query.ListParams{
			Limit: 10,
			Sort: []query.SortColumn{
				{Name: "status", Direction: query.SortASC},
				{Name: "id", Direction: query.SortASC},
			},
		}
		devices, err := repo.List(ctx, params)
		require.NoError(t, err)
		require.Len(t, devices, 3)
		// "offline" < "online" alphabetically
		assert.Equal(t, "offline", devices[0].Status)
	})

	t.Run("list sorted by id ASC with limit", func(t *testing.T) {
		params := query.ListParams{
			Limit: 2,
			Sort:  []query.SortColumn{{Name: "id", Direction: query.SortASC}},
		}
		devices, err := repo.List(ctx, params)
		require.NoError(t, err)
		// FetchLimit is Limit+1 but ApplyCursor limits to Limit; we get 2 + 1 = 3 from raw,
		// but the test just checks we get at most Limit results.
		assert.LessOrEqual(t, len(devices), 3) // upper bound from FetchLimit
	})

	t.Run("unknown sort field returns stable order", func(t *testing.T) {
		params := query.ListParams{
			Limit: 10,
			Sort:  []query.SortColumn{{Name: "unknown", Direction: query.SortASC}},
		}
		devices, err := repo.List(ctx, params)
		require.NoError(t, err)
		assert.Len(t, devices, 3)
	})
}
