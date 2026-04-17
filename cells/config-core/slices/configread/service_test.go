package configread

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	logger := slog.Default()
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	return NewService(repo, codec, logger), repo
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

			result, err := svc.List(context.Background(), query.PageRequest{})
			require.NoError(t, err)
			assert.Len(t, result.Items, tt.wantLen)
		})
	}
}

func TestService_List_FirstPage(t *testing.T) {
	svc, repo := newTestService()
	for i := 0; i < 5; i++ {
		seedEntry(t, repo, "key-"+string(rune('a'+i)), "v")
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
		seedEntry(t, repo, "key-"+string(rune('a'+i)), "v")
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

func TestService_List_ScopeMismatch(t *testing.T) {
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	differentSort := []query.SortColumn{
		{Name: "created_at", Direction: query.SortDESC},
		{Name: "id", Direction: query.SortASC},
	}
	cur := query.Cursor{
		Values:  []any{"some-key", "some-id"},
		Scope:   query.SortScope(differentSort),
		Context: query.QueryContext("endpoint", "config-read"),
	}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	svc, _ := newTestService()
	_, err = svc.List(context.Background(), query.PageRequest{Cursor: token})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "sort scope mismatch", ecErr.Details["reason"])
}

func TestService_List_ContextMismatch(t *testing.T) {
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	cur := query.Cursor{
		Values:  []any{"some-key", "some-id"},
		Scope:   query.SortScope(configSort),
		Context: query.QueryContext("endpoint", "wrong-endpoint"),
	}
	token, err := codec.Encode(cur)
	require.NoError(t, err)

	svc, _ := newTestService()
	_, err = svc.List(context.Background(), query.PageRequest{Cursor: token})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "query context mismatch", ecErr.Details["reason"])
}

func TestService_List_LastPage(t *testing.T) {
	svc, repo := newTestService()
	seedEntry(t, repo, "key-a", "v")
	seedEntry(t, repo, "key-b", "v")

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
