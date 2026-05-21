package configcoretest_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/configcoretest"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
)

// clock2024 returns a fixed deterministic time for clock-injection tests.
func clock2024() time.Time {
	return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
}

// marshalEntryUpserted serializes a minimal entry-upserted event payload.
func marshalEntryUpserted(key string, version int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"key":     key,
		"version": version,
		"actorId": "test-actor",
	})
}

// adminCtx returns a context with a synthetic admin principal.
func adminCtx() context.Context {
	return auth.TestContext("test-admin", []string{"admin"})
}

// TestBuildWriteService_DefaultWiring verifies that the default assembly wires
// a functioning service: Create/Update emit entries to the Recorder and persist
// via FakeConfigRepository.
func TestBuildWriteService_DefaultWiring(t *testing.T) {
	t.Parallel()
	svc, rec := configcoretest.BuildWriteService(t)

	entry, err := svc.Create(adminCtx(), configwrite.CreateInput{Key: "app.name", Value: "gocell"})
	require.NoError(t, err)
	assert.Equal(t, "app.name", entry.Key)
	assert.Equal(t, 1, entry.Version)

	entries := rec.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, domain.TopicConfigEntryUpserted, entries[0].EventType)
}

// TestBuildWriteService_WithCustomRepo verifies that WithWriteRepository
// replaces the default FakeConfigRepository.
func TestBuildWriteService_WithCustomRepo(t *testing.T) {
	t.Parallel()
	repo := configcoretest.NewFakeConfigRepository()
	svc, _ := configcoretest.BuildWriteService(t, configcoretest.WithWriteRepository(repo))

	_, err := svc.Create(adminCtx(), configwrite.CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)

	snap := repo.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "k", snap[0].Key)
}

// TestBuildWriteService_WithCustomClock verifies that WithWriteClock replaces
// the default clock.
func TestBuildWriteService_WithCustomClock(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(clock2024())
	svc, _ := configcoretest.BuildWriteService(t, configcoretest.WithWriteClock(clk))

	entry, err := svc.Create(adminCtx(), configwrite.CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	assert.Equal(t, clock2024(), entry.CreatedAt)
}

// TestBuildSubscribeService_DefaultWiring verifies that the default assembly
// produces a service that processes events correctly.
func TestBuildSubscribeService_DefaultWiring(t *testing.T) {
	t.Parallel()
	svc := configcoretest.BuildSubscribeService(t)

	payload, err := marshalEntryUpserted("key1", 1)
	require.NoError(t, err)

	result := svc.HandleEntryUpserted(context.Background(), outbox.Entry{
		ID:        "test-entry-1",
		EventType: domain.TopicConfigEntryUpserted,
		Payload:   payload,
	})
	assert.Equal(t, outbox.DispositionAck, result.Disposition)

	ver, present := svc.Cache().GetVersion("key1")
	assert.Equal(t, 1, ver)
	assert.True(t, present)
}

// TestFakeConfigRepository_RoundTrip exercises the full Create → GetByKey →
// Update → Delete → Snapshot lifecycle.
func TestFakeConfigRepository_RoundTrip(t *testing.T) {
	t.Parallel()
	repo := configcoretest.NewFakeConfigRepository()
	ctx := context.Background()

	// Create
	entry := &domain.ConfigEntry{ID: "cfg-1", Key: "foo", Value: "bar", Version: 1}
	require.NoError(t, repo.Create(ctx, entry))

	// GetByKey
	got, err := repo.GetByKey(ctx, "foo")
	require.NoError(t, err)
	assert.Equal(t, "bar", got.Value)

	// Update
	updated, err := repo.Update(ctx, "foo", 1, "baz")
	require.NoError(t, err)
	assert.Equal(t, "baz", updated.Value)
	assert.Equal(t, 2, updated.Version)

	// Delete
	deleted, err := repo.Delete(ctx, "foo", 2)
	require.NoError(t, err)
	assert.Equal(t, "foo", deleted.Key)

	// Snapshot should be empty
	assert.Empty(t, repo.Snapshot())
}

// TestFakeConfigRepository_CallsOf verifies that every method invocation is
// recorded with the correct method name.
func TestFakeConfigRepository_CallsOf(t *testing.T) {
	t.Parallel()
	repo := configcoretest.NewFakeConfigRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, &domain.ConfigEntry{ID: "e1", Key: "k", Value: "v", Version: 1})
	_, _ = repo.GetByKey(ctx, "k")
	_, _ = repo.Update(ctx, "k", 1, "v2")
	_, _ = repo.Delete(ctx, "k", 2)

	assert.Len(t, repo.CallsOf("Create"), 1)
	assert.Len(t, repo.CallsOf("GetByKey"), 1)
	assert.Len(t, repo.CallsOf("Update"), 1)
	assert.Len(t, repo.CallsOf("Delete"), 1)
	assert.Empty(t, repo.CallsOf("List"))
}

// TestFakeConfigRepository_Reset verifies that Reset clears both entries and
// recorded calls.
func TestFakeConfigRepository_Reset(t *testing.T) {
	t.Parallel()
	repo := configcoretest.NewFakeConfigRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, &domain.ConfigEntry{ID: "e1", Key: "k", Value: "v", Version: 1})
	require.NotEmpty(t, repo.Snapshot())
	require.NotEmpty(t, repo.CallsOf("Create"))

	repo.Reset()

	assert.Empty(t, repo.Snapshot())
	assert.Empty(t, repo.CallsOf("Create"))
}

// TestFakeConfigRepository_PublishGetVersion verifies that PublishVersion stores
// a version snapshot and GetVersion retrieves it by (configID, version).
func TestFakeConfigRepository_PublishGetVersion(t *testing.T) {
	t.Parallel()
	repo := configcoretest.NewFakeConfigRepository()
	ctx := context.Background()

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	v := &domain.ConfigVersion{
		ID:          "ver-1",
		ConfigID:    "cfg-1",
		Version:     2,
		Value:       "hello",
		Sensitive:   false,
		PublishedAt: &now,
	}
	require.NoError(t, repo.PublishVersion(ctx, v))

	got, err := repo.GetVersion(ctx, "cfg-1", 2)
	require.NoError(t, err)
	assert.Equal(t, v.ID, got.ID)
	assert.Equal(t, v.Value, got.Value)
	assert.Equal(t, v.Version, got.Version)

	// GetVersion returns not-found for unknown (configID, version).
	_, err = repo.GetVersion(ctx, "cfg-1", 99)
	require.Error(t, err)

	// Calls are recorded.
	assert.Len(t, repo.CallsOf("PublishVersion"), 1)
	assert.Len(t, repo.CallsOf("GetVersion"), 2)
}

// TestFakeConfigRepository_Snapshot_SensitiveRedact verifies that Snapshot
// replaces sensitive entry values with "<REDACTED>".
func TestFakeConfigRepository_Snapshot_SensitiveRedact(t *testing.T) {
	t.Parallel()
	repo := configcoretest.NewFakeConfigRepository()
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "e1", Key: "pub", Value: "visible", Version: 1, Sensitive: false,
	}))
	require.NoError(t, repo.Create(ctx, &domain.ConfigEntry{
		ID: "e2", Key: "sec", Value: "topsecret", Version: 1, Sensitive: true,
	}))

	snap := repo.Snapshot()
	require.Len(t, snap, 2)
	// Snapshot is key-sorted: "pub" < "sec".
	assert.Equal(t, "pub", snap[0].Key)
	assert.Equal(t, "visible", snap[0].Value)
	assert.Equal(t, "sec", snap[1].Key)
	assert.Equal(t, "<REDACTED>", snap[1].Value)
}

// TestBuildWriteService_RecorderCapturesDeleteEvent verifies that Delete emits
// an entry-deleted event captured by the Recorder.
func TestBuildWriteService_RecorderCapturesDeleteEvent(t *testing.T) {
	t.Parallel()
	svc, rec := configcoretest.BuildWriteService(t)

	_, err := svc.Create(adminCtx(), configwrite.CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)

	rec.Reset()
	err = svc.Delete(adminCtx(), "k", 1)
	require.NoError(t, err)

	entries := rec.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, domain.TopicConfigEntryDeleted, entries[0].EventType)
}
