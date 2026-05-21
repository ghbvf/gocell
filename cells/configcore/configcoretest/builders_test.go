package configcoretest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	configevents "github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
)

// testFixedTime is a fixed point-in-time used for clock-sensitive assertions.
// Named testFixedTime (not testTime) to make the read-only intent explicit:
// time.Time is a value type and cannot be declared const.
var testFixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

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
	t.Run("HandleEntryUpserted drives the wired pipeline end-to-end", func(t *testing.T) {
		svc := BuildSubscribeService(t)
		require.NotNil(t, svc)

		cache := svc.Cache()
		require.NotNil(t, cache)
		assert.Equal(t, 0, cache.Len())

		// Build an outbox.Entry carrying an EntryUpserted payload.
		payload, err := json.Marshal(configevents.EntryUpserted{
			Key:     "smoke-k",
			Version: 1,
			ActorID: "test-actor",
		})
		require.NoError(t, err)

		entry := outbox.Entry{
			ID:        "test-entry-001",
			EventType: domain.TopicConfigEntryUpserted,
			Payload:   payload,
		}

		result := svc.HandleEntryUpserted(context.Background(), entry)
		assert.Equal(t, outbox.DispositionAck, result.Disposition)

		version, present := cache.GetVersion("smoke-k")
		assert.True(t, present, "key should be present in cache after upsert")
		assert.Equal(t, 1, version)
		assert.Equal(t, 1, cache.Len())
	})
}

func TestNewFakeConfigRepositoryRoundtrip(t *testing.T) {
	t.Run("seed and snapshot returns seeded entries", func(t *testing.T) {
		repo := NewFakeConfigRepository(clock.Real())
		ctx := context.Background()

		err := repo.Seed(ctx, SeededEntry{
			Key:   "roundtrip-key",
			Value: "roundtrip-value",
		})
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

		entries := []SeededEntry{
			{Key: "key-a", Value: "val-a"},
			{Key: "key-b", Value: "val-b"},
		}
		for _, e := range entries {
			require.NoError(t, repo.Seed(ctx, e))
		}

		snapshot, err := repo.Snapshot(ctx)
		require.NoError(t, err)
		assert.Len(t, snapshot, 2)
	})

	t.Run("seed duplicate key returns error", func(t *testing.T) {
		repo := NewFakeConfigRepository(clock.Real())
		ctx := context.Background()

		require.NoError(t, repo.Seed(ctx, SeededEntry{Key: "dup-key", Value: "v1"}))
		err := repo.Seed(ctx, SeededEntry{Key: "dup-key", Value: "v2"})
		require.Error(t, err, "seeding a duplicate key must return an error")
	})
}

func TestBuildWriteServiceWithCustomClock(t *testing.T) {
	t.Run("custom clock propagates to created entry timestamps", func(t *testing.T) {
		clk := clockmock.New(testFixedTime)
		svc, rec := BuildWriteService(t, WithWriteClock(clk))
		require.NotNil(t, svc)

		ctx := auth.TestContext("test-admin", []string{"admin"})
		entry, err := svc.Create(ctx, configwrite.CreateInput{
			Key:   "clock-key",
			Value: "clock-value",
		})
		require.NoError(t, err)
		assert.Equal(t, testFixedTime, entry.CreatedAt)
		assert.Equal(t, testFixedTime, entry.UpdatedAt)

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

// TestFakeConfigRepositoryRepoReadiness satisfies CELL-REPO-READYZ-PROBE-01/P1:
// every kernel/cell.RepoHealthProber implementation must be wired through
// celltest.RunRepoReadinessConformance. FakeConfigRepository delegates to an
// in-memory backend that is always ready, so broken=nil (skip the failure-
// domain sub-test per conformance harness convention).
func TestFakeConfigRepositoryRepoReadiness(t *testing.T) {
	repo := NewFakeConfigRepository(clock.Real())
	celltest.RunRepoReadinessConformance(t, "fake_config_repo_ready", repo, nil)
}
