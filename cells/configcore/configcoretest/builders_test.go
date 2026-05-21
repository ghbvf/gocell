package configcoretest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth"
)

var testTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

func TestBuildWriteServiceSmoke(t *testing.T) {
	t.Run("build with defaults and create an entry", func(t *testing.T) {
		svc, rec := BuildWriteService(t)
		require.NotNil(t, svc)
		require.NotNil(t, rec)

		ctx := auth.TestContext("test-admin", []string{"admin"})
		entry, err := svc.Create(ctx, configwrite.CreateInput{
			Key:   "smoke-key",
			Value: "smoke-value",
		})
		require.NoError(t, err)
		assert.Equal(t, "smoke-key", entry.Key)
		assert.Equal(t, "smoke-value", entry.Value)

		entries := rec.Entries()
		require.Len(t, entries, 1)
		assert.Equal(t, domain.TopicConfigEntryUpserted, entries[0].EventType)
	})
}

func TestBuildSubscribeServiceSmoke(t *testing.T) {
	t.Run("build with defaults returns non-nil service", func(t *testing.T) {
		svc := BuildSubscribeService(t)
		require.NotNil(t, svc)

		cache := svc.Cache()
		require.NotNil(t, cache)
		assert.Equal(t, 0, cache.Len())
	})
}

func TestNewFakeConfigRepositoryRoundtrip(t *testing.T) {
	t.Run("seed and snapshot returns seeded entries", func(t *testing.T) {
		repo := NewFakeConfigRepository(clock.Real())
		ctx := context.Background()

		entry := &domain.ConfigEntry{
			ID:        "cfg-test-001",
			Key:       "roundtrip-key",
			Value:     "roundtrip-value",
			Version:   1,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		err := repo.Seed(ctx, entry)
		require.NoError(t, err)

		snapshot, err := repo.Snapshot(ctx)
		require.NoError(t, err)
		require.Len(t, snapshot, 1)
		assert.Equal(t, "roundtrip-key", snapshot[0].Key)
		assert.Equal(t, "roundtrip-value", snapshot[0].Value)
	})

	t.Run("seed multiple entries", func(t *testing.T) {
		repo := NewFakeConfigRepository(clock.Real())
		ctx := context.Background()

		entries := []*domain.ConfigEntry{
			{ID: "cfg-001", Key: "key-a", Value: "val-a", Version: 1},
			{ID: "cfg-002", Key: "key-b", Value: "val-b", Version: 1},
		}
		for _, e := range entries {
			require.NoError(t, repo.Seed(ctx, e))
		}

		snapshot, err := repo.Snapshot(ctx)
		require.NoError(t, err)
		assert.Len(t, snapshot, 2)
	})
}

func TestBuildWriteServiceWithCustomClock(t *testing.T) {
	t.Run("custom clock propagates to created entry timestamps", func(t *testing.T) {
		clk := clockmock.New(testTime)
		svc, rec := BuildWriteService(t, WithWriteClock(clk))
		require.NotNil(t, svc)

		ctx := auth.TestContext("test-admin", []string{"admin"})
		entry, err := svc.Create(ctx, configwrite.CreateInput{
			Key:   "clock-key",
			Value: "clock-value",
		})
		require.NoError(t, err)
		assert.Equal(t, testTime, entry.CreatedAt)
		assert.Equal(t, testTime, entry.UpdatedAt)

		// emitter also captured the event
		require.Len(t, rec.Entries(), 1)
	})
}

func TestBuildWriteServiceWithCustomRepo(t *testing.T) {
	t.Run("injected fake repo is used by service", func(t *testing.T) {
		repo := NewFakeConfigRepository(clock.Real())
		svc, _ := BuildWriteService(t, WithWriteRepository(repo))

		ctx := auth.TestContext("test-admin", []string{"admin"})
		_, err := svc.Create(ctx, configwrite.CreateInput{
			Key:   "repo-key",
			Value: "repo-value",
		})
		require.NoError(t, err)

		snapshot, err := repo.Snapshot(context.Background())
		require.NoError(t, err)
		require.Len(t, snapshot, 1)
		assert.Equal(t, "repo-key", snapshot[0].Key)
	})
}
