package configcoretest

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
)

// SeededEntry is a configcoretest-local view of a config entry suitable for
// public test code outside the cells/configcore subtree. It mirrors the
// fields callers need without leaking internal/domain types.
//
// Version defaults to 1 when passed to Seed and Version == 0.
type SeededEntry struct {
	Key       string
	Value     string
	Sensitive bool
	Version   int // optional; defaults to 1 on Seed when zero
}

// FakeConfigRepository wraps an in-memory ConfigRepository and adds Seed and
// Snapshot helpers for test scenario setup and assertion.
//
// FakeConfigRepository implements ports.ConfigRepository via the embedded
// *mem.ConfigRepository. Tests obtain a wired instance from BuildWriteService
// (which returns the repo as its second value); direct construction via
// NewFakeConfigRepository is for repo-only tests that do not need a service.
//
// Spy methods (CallsOf/Reset) are deferred until a test needs them; add via
// Seed + Snapshot if you need state assertions without a Recorder.
type FakeConfigRepository struct {
	*mem.ConfigRepository
}

// NewFakeConfigRepository creates an empty FakeConfigRepository backed by the
// in-memory implementation.
// clk must be non-nil; pass clock.Real() or clockmock.New(...) in tests.
func NewFakeConfigRepository(clk clock.Clock) *FakeConfigRepository {
	return &FakeConfigRepository{
		ConfigRepository: mem.NewConfigRepository(clk),
	}
}

// Seed inserts a single entry into the repository directly, bypassing
// service-layer validation. Use this to pre-populate the repository before a
// test scenario.
//
// If entry.Version is 0 it is treated as 1.
//
// Seed does NOT support upsert; calling Seed twice with the same Key returns
// ErrConfigDuplicate. Construct a fresh repo via NewFakeConfigRepository
// between scenarios.
func (r *FakeConfigRepository) Seed(ctx context.Context, entry SeededEntry) error {
	v := entry.Version
	if v == 0 {
		v = 1
	}
	if err := r.Create(ctx, &domain.ConfigEntry{
		Key:       entry.Key,
		Value:     entry.Value,
		Sensitive: entry.Sensitive,
		Version:   v,
	}); err != nil {
		return fmt.Errorf("configcoretest.FakeConfigRepository.Seed: %w", err)
	}
	return nil
}

// Snapshot returns a point-in-time copy of all config entries currently stored,
// sorted by key for deterministic ordering in assertions. It does not modify
// the repository.
//
// Snapshot returns up to 500 entries (query.MaxPageSize). Tests seeding more
// entries than this limit will receive a truncated result — the returned slice
// will contain exactly 500 entries in that case.
//
// WARNING: entries with Sensitive=true contain the plaintext Value as stored in
// the in-memory backend. Tests should not log entry.Value when Sensitive=true.
func (r *FakeConfigRepository) Snapshot(ctx context.Context) ([]SeededEntry, error) {
	entries, err := r.List(ctx, query.ListParams{
		Limit: query.MaxPageSize,
		Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}},
	})
	if err != nil {
		return nil, fmt.Errorf("configcoretest.FakeConfigRepository.Snapshot: %w", err)
	}
	out := make([]SeededEntry, len(entries))
	for i, e := range entries {
		out[i] = SeededEntry{
			Key:       e.Key,
			Value:     e.Value,
			Sensitive: e.Sensitive,
			Version:   e.Version,
		}
	}
	return out, nil
}
