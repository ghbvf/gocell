package configcoretest

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// TestTenant is the canonical test tenant UUID used by configcoretest helpers.
// All Seed / Snapshot operations and CtxWithTenant use this fixed value so test
// scenarios that write via service (which derives tenant from ctx) and then read
// via Snapshot (which queries under TestTenant) see consistent data.
//
// Mirrors the pattern established by accesscore PR-2a:
//
//	ctxkeys.WithTenantID(ctx, "00000000-0000-0000-0000-000000000001")
const testTenantStr = "00000000-0000-0000-0000-000000000001"

// TestTenant is the TenantID value that CtxWithTenant injects and that
// Seed / Snapshot operate under.
const TestTenant tenant.TenantID = testTenantStr

// CtxWithTenant returns ctx with TestTenant injected via the canonical
// ctxkeys.WithTenantID mechanism — the same path used by the JWT authenticator
// in production and by all accesscore service tests.
func CtxWithTenant(ctx context.Context) context.Context {
	return ctxkeys.WithTenantID(ctx, testTenantStr)
}

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
//
// Tenant scoping: Seed uses TestTenant so that service tests that inject
// CtxWithTenant(ctx) see the same rows. Tests that need cross-tenant isolation
// should call the underlying repo methods directly with explicit TenantID values.
func (r *FakeConfigRepository) Seed(ctx context.Context, entry SeededEntry) error {
	v := entry.Version
	if v == 0 {
		v = 1
	}
	if err := r.Create(ctx, TestTenant, &domain.ConfigEntry{
		Key:       entry.Key,
		Value:     entry.Value,
		Sensitive: entry.Sensitive,
		Version:   v,
	}); err != nil {
		return fmt.Errorf("configcoretest.FakeConfigRepository.Seed: %w", err)
	}
	return nil
}

// Snapshot returns a point-in-time copy of all config entries currently stored
// under TestTenant, sorted by key for deterministic ordering in assertions.
// It does not modify the repository.
//
// Snapshot returns up to 500 entries (query.MaxPageSize). Tests seeding more
// entries than this limit will receive a truncated result — the returned slice
// will contain exactly 500 entries in that case.
//
// WARNING: entries with Sensitive=true contain the plaintext Value as stored in
// the in-memory backend. Tests should not log entry.Value when Sensitive=true.
func (r *FakeConfigRepository) Snapshot(ctx context.Context) ([]SeededEntry, error) {
	entries, err := r.List(ctx, TestTenant, query.ListParams{
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
