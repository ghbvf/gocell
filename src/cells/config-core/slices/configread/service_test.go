package configread

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	logger := slog.Default()
	return NewService(repo, logger), repo
}

func seedEntry(t *testing.T, repo *mem.ConfigRepository, key, value string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "id-" + key, Key: key, Value: value, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func TestService_GetByKey(t *testing.T) {
	tests := []struct {
		name    string
		seed    bool
		key     string
		wantErr bool
	}{
		{
			name: "existing key", seed: true, key: "app.name", wantErr: false,
		},
		{
			name: "non-existent key", seed: false, key: "missing", wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			if tt.seed {
				seedEntry(t, repo, tt.key, "value")
			}

			entry, err := svc.GetByKey(context.Background(), tt.key)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, entry)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.key, entry.Key)
			}
		})
	}
}

func TestService_List(t *testing.T) {
	tests := []struct {
		name      string
		seedCount int
		wantLen   int
	}{
		{name: "empty", seedCount: 0, wantLen: 0},
		{name: "two entries", seedCount: 2, wantLen: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			for i := range tt.seedCount {
				seedEntry(t, repo, "key-"+string(rune('a'+i)), "v")
			}

			entries, err := svc.List(context.Background())
			require.NoError(t, err)
			assert.Len(t, entries, tt.wantLen)
		})
	}
}
