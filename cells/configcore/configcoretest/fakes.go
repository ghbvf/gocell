package configcoretest

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time interface check.
var _ ports.ConfigRepository = (*FakeConfigRepository)(nil)

// FakeCall records a single call to FakeConfigRepository, capturing the method
// name and the arguments passed.
type FakeCall struct {
	Method string
	Args   []any
}

// FakeConfigRepository is an in-memory ports.ConfigRepository for use in tests.
// It records every call for later assertion via CallsOf and exposes the current
// stored state via Snapshot.
//
// All methods are safe for concurrent use.
type FakeConfigRepository struct {
	mu       sync.Mutex
	entries  map[string]*domain.ConfigEntry
	versions map[string]*domain.ConfigVersion
	calls    []FakeCall
	clk      clock.Clock
}

// NewFakeConfigRepository returns an empty FakeConfigRepository backed by
// clock.Real(). Use WithWriteRepository to inject it into BuildWriteService.
func NewFakeConfigRepository() *FakeConfigRepository {
	return &FakeConfigRepository{
		entries:  make(map[string]*domain.ConfigEntry),
		versions: make(map[string]*domain.ConfigVersion),
		clk:      clock.Real(),
	}
}

// Snapshot returns a copy of all currently stored entries in key-sorted order.
// Entries with Sensitive=true have their Value replaced with "<REDACTED>" to
// prevent accidental exposure of secret values in test assertions and logs.
func (r *FakeConfigRepository) Snapshot() []domain.ConfigEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.ConfigEntry, 0, len(r.entries))
	for _, e := range r.entries {
		clone := *e
		if clone.Sensitive {
			clone.Value = "<REDACTED>"
		}
		out = append(out, clone)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Key < out[j].Key
	})
	return out
}

// CallsOf returns the recorded calls whose Method equals the given name, in
// the order they were recorded.
func (r *FakeConfigRepository) CallsOf(method string) []FakeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []FakeCall
	for _, c := range r.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Reset clears all stored entries, versions, and recorded calls.
func (r *FakeConfigRepository) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = make(map[string]*domain.ConfigEntry)
	r.versions = make(map[string]*domain.ConfigVersion)
	r.calls = r.calls[:0]
}

func (r *FakeConfigRepository) record(method string, args ...any) {
	r.calls = append(r.calls, FakeCall{Method: method, Args: args})
}

func (r *FakeConfigRepository) Create(_ context.Context, entry *domain.ConfigEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("Create", entry.Key)
	if _, exists := r.entries[entry.Key]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrConfigDuplicate,
			"config key already exists",
			errcode.WithInternal(fmt.Sprintf("key=%q", entry.Key)))
	}
	clone := *entry
	r.entries[entry.Key] = &clone
	return nil
}

func (r *FakeConfigRepository) GetByKey(_ context.Context, key string) (*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("GetByKey", key)
	entry, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config not found",
			errcode.WithInternal(fmt.Sprintf("key=%q", key)))
	}
	clone := *entry
	return &clone, nil
}

func (r *FakeConfigRepository) Update(_ context.Context, key string, expectedVersion int, value string) (*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("Update", key, expectedVersion, value)
	existing, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config not found",
			errcode.WithInternal(fmt.Sprintf("key=%q", key)))
	}
	if existing.Version != expectedVersion {
		return nil, errcode.New(errcode.KindConflict, errcode.ErrVersionConflict,
			"version conflict",
			errcode.WithInternal(fmt.Sprintf("key=%q expected=%d actual=%d", key, expectedVersion, existing.Version)))
	}
	existing.Value = value
	existing.Version++
	existing.UpdatedAt = r.clk.Now()
	clone := *existing
	return &clone, nil
}

func (r *FakeConfigRepository) UpdateForRollback(
	_ context.Context, key string, expectedVersion int, value string, sensitive bool,
) (*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	recordedValue := value
	if sensitive {
		recordedValue = "<REDACTED>"
	}
	r.record("UpdateForRollback", key, expectedVersion, recordedValue, sensitive)
	existing, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config not found",
			errcode.WithInternal(fmt.Sprintf("key=%q", key)))
	}
	if existing.Version != expectedVersion {
		return nil, errcode.New(errcode.KindConflict, errcode.ErrVersionConflict,
			"version conflict",
			errcode.WithInternal(fmt.Sprintf("key=%q expected=%d actual=%d", key, expectedVersion, existing.Version)))
	}
	existing.Value = value
	existing.Sensitive = sensitive
	existing.Version++
	existing.UpdatedAt = r.clk.Now()
	clone := *existing
	return &clone, nil
}

func (r *FakeConfigRepository) Delete(_ context.Context, key string, expectedVersion int) (*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("Delete", key, expectedVersion)
	existing, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config not found",
			errcode.WithInternal(fmt.Sprintf("key=%q", key)))
	}
	if existing.Version != expectedVersion {
		return nil, errcode.New(errcode.KindConflict, errcode.ErrVersionConflict,
			"version conflict",
			errcode.WithInternal(fmt.Sprintf("key=%q expected=%d actual=%d", key, expectedVersion, existing.Version)))
	}
	clone := *existing
	delete(r.entries, key)
	return &clone, nil
}

// List returns copies of all stored entries in key-sorted order.
// NOTE: pagination and filtering in ListParams are intentionally ignored;
// all entries are returned in key-sorted order.
func (r *FakeConfigRepository) List(_ context.Context, params query.ListParams) ([]*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("List")
	all := make([]*domain.ConfigEntry, 0, len(r.entries))
	for _, e := range r.entries {
		clone := *e
		all = append(all, &clone)
	}
	_ = params
	sort.Slice(all, func(i, j int) bool {
		return all[i].Key < all[j].Key
	})
	return all, nil
}

// PublishVersion stores the version snapshot keyed by "configID:version".
func (r *FakeConfigRepository) PublishVersion(_ context.Context, version *domain.ConfigVersion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("PublishVersion", version)
	key := fmt.Sprintf("%s:%d", version.ConfigID, version.Version)
	clone := *version
	r.versions[key] = &clone
	return nil
}

// GetVersion retrieves a previously published version snapshot by (configID, version).
// Returns ErrConfigNotFound if the version was not published via PublishVersion.
func (r *FakeConfigRepository) GetVersion(_ context.Context, configID string, version int) (*domain.ConfigVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record("GetVersion", configID, version)
	key := fmt.Sprintf("%s:%d", configID, version)
	v, ok := r.versions[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "version not found",
			errcode.WithInternal(fmt.Sprintf("configID=%q version=%d", configID, version)))
	}
	clone := *v
	return &clone, nil
}

// RepoReady implements cell.RepoHealthProber. In-memory stores are always ready.
func (r *FakeConfigRepository) RepoReady(_ context.Context) error {
	return nil
}
