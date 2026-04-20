package deviceregister

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failPublisher is a Publisher that always returns an error.
type failPublisher struct{}

func (failPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return errors.New("publish failed")
}
func (failPublisher) Close(_ context.Context) error { return nil }

func newTestService() (*Service, *mem.DeviceRepository) {
	repo := mem.NewDeviceRepository()
	pub := eventbus.New()
	return NewService(repo, pub, slog.Default()), repo
}

func TestService_Register(t *testing.T) {
	tests := []struct {
		name       string
		deviceName string
		publisher  func() *Service
		wantErr    bool
		checkDev   func(t *testing.T, dev *domain.Device)
	}{
		{
			name:       "valid registration",
			deviceName: "sensor-a",
			wantErr:    false,
			checkDev: func(t *testing.T, dev *domain.Device) {
				assert.NotEmpty(t, dev.ID)
				assert.Equal(t, "sensor-a", dev.Name)
				assert.Equal(t, "online", dev.Status)
				assert.False(t, dev.LastSeen.IsZero())
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

			dev, err := svc.Register(context.Background(), tc.deviceName)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Nil(t, dev)
			} else {
				require.NoError(t, err)
				require.NotNil(t, dev)
				if tc.checkDev != nil {
					tc.checkDev(t, dev)
				}
			}
		})
	}
}

func TestService_Register_PersistsDevice(t *testing.T) {
	svc, repo := newTestService()
	ctx := context.Background()

	dev, err := svc.Register(ctx, "sensor-b")
	require.NoError(t, err)

	stored, err := repo.GetByID(ctx, dev.ID)
	require.NoError(t, err)
	assert.Equal(t, dev.ID, stored.ID)
	assert.Equal(t, "sensor-b", stored.Name)
}

func TestService_Register_PublishFails_StillReturnsDevice(t *testing.T) {
	repo := mem.NewDeviceRepository()
	svc := NewService(repo, failPublisher{}, slog.Default())

	dev, err := svc.Register(context.Background(), "sensor-c")
	require.NoError(t, err, "publish failure should not propagate as error")
	require.NotNil(t, dev)
	assert.NotEmpty(t, dev.ID)
}

func TestService_Register_DuplicateID_IsUnlikelyButHandled(t *testing.T) {
	// Since uuid.NewString generates random IDs, duplicate is practically
	// impossible. We verify two sequential calls succeed without collision.
	svc, _ := newTestService()
	ctx := context.Background()

	d1, err := svc.Register(ctx, "dev-1")
	require.NoError(t, err)
	d2, err := svc.Register(ctx, "dev-2")
	require.NoError(t, err)
	assert.NotEqual(t, d1.ID, d2.ID)
}
