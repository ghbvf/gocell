package configwrite

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	eb := eventbus.New()
	logger := slog.Default()
	return NewService(repo, eb, logger), repo
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
			svc, _ := newTestService()
			entry, err := svc.Create(context.Background(), tt.input)
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
	svc, _ := newTestService()
	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)

	_, err = svc.Create(context.Background(), CreateInput{Key: "k", Value: "v2"})
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
				_, _ = svc.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
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
			svc, _ := newTestService()
			tt.setup(svc)
			entry, err := svc.Update(context.Background(), tt.input)
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
				_, _ = svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
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
			svc, _ := newTestService()
			tt.setup(svc)
			err := svc.Delete(context.Background(), tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
