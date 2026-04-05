package featureflag

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.FlagRepository) {
	repo := mem.NewFlagRepository()
	logger := slog.Default()
	return NewService(repo, logger), repo
}

func seedFlag(t *testing.T, repo *mem.FlagRepository, key string, flagType domain.FlagType, enabled bool, pct int) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &domain.FeatureFlag{
		ID: "flag-" + key, Key: key, Type: flagType,
		Enabled: enabled, RolloutPercentage: pct,
	}))
}

func TestService_GetByKey(t *testing.T) {
	tests := []struct {
		name    string
		seed    bool
		key     string
		wantErr bool
	}{
		{name: "existing flag", seed: true, key: "dark-mode", wantErr: false},
		{name: "non-existent", seed: false, key: "missing", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			if tt.seed {
				seedFlag(t, repo, tt.key, domain.FlagBoolean, true, 0)
			}

			flag, err := svc.GetByKey(context.Background(), tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.key, flag.Key)
			}
		})
	}
}

func TestService_List(t *testing.T) {
	svc, repo := newTestService()
	seedFlag(t, repo, "f1", domain.FlagBoolean, true, 0)
	seedFlag(t, repo, "f2", domain.FlagPercentage, true, 50)

	flags, err := svc.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, flags, 2)
}

func TestService_Evaluate(t *testing.T) {
	tests := []struct {
		name    string
		flagKey string
		subject string
		setup   func(*mem.FlagRepository)
		wantErr bool
	}{
		{
			name: "boolean enabled",
			flagKey: "feat", subject: "user-1",
			setup: func(repo *mem.FlagRepository) {
				_ = repo.Create(context.Background(), &domain.FeatureFlag{
					ID: "f1", Key: "feat", Type: domain.FlagBoolean, Enabled: true,
				})
			},
			wantErr: false,
		},
		{
			name: "empty key",
			flagKey: "", subject: "user-1",
			setup:   func(_ *mem.FlagRepository) {},
			wantErr: true,
		},
		{
			name: "empty subject",
			flagKey: "feat", subject: "",
			setup:   func(_ *mem.FlagRepository) {},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			tt.setup(repo)

			result, err := svc.Evaluate(context.Background(), tt.flagKey, tt.subject)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.flagKey, result.Key)
			}
		})
	}
}
