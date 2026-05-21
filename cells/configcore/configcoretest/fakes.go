package configcoretest

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
)

// FakeConfigRepository wraps cells/configcore/internal/mem.ConfigRepository
// and adds Seed and Snapshot helpers for journey assertions.
//
// FakeConfigRepository implements ports.ConfigRepository via the embedded
// *mem.ConfigRepository, so it can be passed directly to BuildWriteService.
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

// Seed inserts entry into the repository directly, bypassing service-layer
// validation. Use this to pre-populate the repository before a test scenario.
//
// If an entry with the same Key already exists, Seed returns an error wrapping
// the underlying conflict from the mem store.
func (r *FakeConfigRepository) Seed(ctx context.Context, entry *domain.ConfigEntry) error {
	if err := r.Create(ctx, entry); err != nil {
		return fmt.Errorf("configcoretest.FakeConfigRepository.Seed: %w", err)
	}
	return nil
}

// Snapshot returns a point-in-time copy of all config entries currently stored,
// sorted by key for deterministic ordering in assertions. It does not modify
// the repository.
func (r *FakeConfigRepository) Snapshot(ctx context.Context) ([]*domain.ConfigEntry, error) {
	entries, err := r.List(ctx, query.ListParams{
		Limit: query.MaxPageSize,
		Sort:  []query.SortColumn{{Name: "key", Direction: query.SortASC}},
	})
	if err != nil {
		return nil, fmt.Errorf("configcoretest.FakeConfigRepository.Snapshot: %w", err)
	}
	return entries, nil
}
