package featureflag

import (
	"context"
	"crypto/rand"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.FlagRepository) {
	repo := mem.NewFlagRepository()
	logger := slog.Default()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	codec, _ := query.NewCursorCodec(key)
	return NewService(repo, codec, logger), repo
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

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
}

func TestService_List_FirstPage(t *testing.T) {
	svc, repo := newTestService()
	for i := 0; i < 5; i++ {
		seedFlag(t, repo, "flag-"+string(rune('a'+i)), domain.FlagBoolean, true, 0)
	}

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 3})
	require.NoError(t, err)
	assert.Len(t, result.Items, 3)
	assert.True(t, result.HasMore)
	assert.NotEmpty(t, result.NextCursor)
}

func TestService_List_WithCursor(t *testing.T) {
	svc, repo := newTestService()
	for i := 0; i < 5; i++ {
		seedFlag(t, repo, "flag-"+string(rune('a'+i)), domain.FlagBoolean, true, 0)
	}

	page1, err := svc.List(context.Background(), query.PageRequest{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)

	page2, err := svc.List(context.Background(), query.PageRequest{Limit: 3, Cursor: page1.NextCursor})
	require.NoError(t, err)
	assert.Len(t, page2.Items, 2)
	assert.NotEqual(t, page1.Items[0].ID, page2.Items[0].ID)
}

func TestService_List_InvalidCursor(t *testing.T) {
	svc, _ := newTestService()

	_, err := svc.List(context.Background(), query.PageRequest{Cursor: "garbage"})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

func TestService_List_LastPage(t *testing.T) {
	svc, repo := newTestService()
	seedFlag(t, repo, "flag-a", domain.FlagBoolean, true, 0)
	seedFlag(t, repo, "flag-b", domain.FlagBoolean, true, 0)

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_List_Empty(t *testing.T) {
	svc, _ := newTestService()

	result, err := svc.List(context.Background(), query.PageRequest{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
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
