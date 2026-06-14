package mem

import (
	"cmp"
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/corecells/configcore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

// Compile-time check.
var _ ports.FlagRepository = (*FlagRepository)(nil)

const (
	flagInternalKeyQuotedFmt = "key=%q"
	msgFlagInvalidTenant     = "flag repo: invalid tenant"
	msgFlagNotFound          = "flag not found"
)

// FlagRepository is an in-memory implementation of ports.FlagRepository.
//
// # Tenancy (#1337 PR-2b)
//
// Every data method takes a mandatory tenant.TenantID positional parameter and
// scopes all reads/writes to the tenant's inner map. Cross-tenant rows are
// structurally unreachable — they live in a different inner map — so the
// existing not-found error path fires naturally for any mismatched access.
type FlagRepository struct {
	mu    sync.RWMutex
	flags map[tenant.TenantID]map[string]*domain.FeatureFlag // tenant -> key -> flag
	clock clock.Clock
}

// NewFlagRepository creates an empty in-memory FlagRepository.
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
func NewFlagRepository(clk clock.Clock) *FlagRepository {
	clock.MustHaveClock(clk, "mem.NewFlagRepository")
	return &FlagRepository{
		flags: make(map[tenant.TenantID]map[string]*domain.FeatureFlag),
		clock: clk,
	}
}

// tenantFlags lazily creates and returns the inner flags map for t.
// Caller must hold mu (write lock).
func (r *FlagRepository) tenantFlags(t tenant.TenantID) map[string]*domain.FeatureFlag {
	m, ok := r.flags[t]
	if !ok {
		m = make(map[string]*domain.FeatureFlag)
		r.flags[t] = m
	}
	return m
}

func (r *FlagRepository) Create(_ context.Context, t tenant.TenantID, flag *domain.FeatureFlag) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgFlagInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	tf := r.tenantFlags(t)
	if _, exists := tf[flag.Key]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrFlagDuplicate, "flag key already exists",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(flagInternalKeyQuotedFmt, flag.Key))))
	}
	clone := *flag
	tf[flag.Key] = &clone
	return nil
}

//nolint:dupl // mirrors ConfigRepository.GetByKey; typed differences (FeatureFlag vs ConfigEntry, distinct codes) preclude shared helper
func (r *FlagRepository) GetByKey(_ context.Context, t tenant.TenantID, key string) (*domain.FeatureFlag, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgFlagInvalidTenant, err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	tf, ok := r.flags[t]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, msgFlagNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(flagInternalKeyQuotedFmt, key))))
	}
	flag, ok := tf[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, msgFlagNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(flagInternalKeyQuotedFmt, key))))
	}
	clone := *flag
	return &clone, nil
}

// Update atomically sets enabled, rollout_percentage, description, and
// increments version by 1 if expectedVersion matches. Returns the updated flag.
// Returns ErrFlagNotFound if the key does not exist,
// or ErrVersionConflict if expectedVersion does not match.
func (r *FlagRepository) Update(
	_ context.Context, t tenant.TenantID, key string, expectedVersion int, enabled bool, rolloutPercentage int, description string,
) (*domain.FeatureFlag, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgFlagInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	tf := r.tenantFlags(t)
	existing, exists := tf[key]
	if !exists {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, msgFlagNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(flagInternalKeyQuotedFmt, key))))
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "feature_flag", key)
	}
	existing.Enabled = enabled
	existing.RolloutPercentage = rolloutPercentage
	existing.Description = description
	existing.Version++
	existing.UpdatedAt = r.clock.Now()
	clone := *existing
	return &clone, nil
}

// Delete removes a feature flag by key if expectedVersion matches.
// Returns the deleted entity. Returns ErrFlagNotFound if the key does not exist,
// or ErrVersionConflict if expectedVersion does not match.
func (r *FlagRepository) Delete(_ context.Context, t tenant.TenantID, key string, expectedVersion int) (*domain.FeatureFlag, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgFlagInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	tf := r.tenantFlags(t)
	existing, exists := tf[key]
	if !exists {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, msgFlagNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(flagInternalKeyQuotedFmt, key))))
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "feature_flag", key)
	}
	clone := *existing
	delete(tf, key)
	return &clone, nil
}

// Toggle sets the enabled state atomically if expectedVersion matches.
// It increments version by 1 and sets UpdatedAt to now().
// It does not overwrite RolloutPercentage or Description.
// Returns ErrFlagNotFound if the key does not exist,
// or ErrVersionConflict if expectedVersion does not match.
//
//nolint:dupl // mirrors ConfigRepository.Update; typed differences (FeatureFlag vs ConfigEntry, distinct fields) preclude shared helper
func (r *FlagRepository) Toggle(
	_ context.Context, t tenant.TenantID, key string, expectedVersion int, enabled bool,
) (*domain.FeatureFlag, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgFlagInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	tf := r.tenantFlags(t)
	existing, exists := tf[key]
	if !exists {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, msgFlagNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(flagInternalKeyQuotedFmt, key))))
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "feature_flag", key)
	}
	existing.Enabled = enabled
	existing.Version++
	existing.UpdatedAt = r.clock.Now()
	clone := *existing
	return &clone, nil
}

// List returns flags sorted and paginated according to params.
// It applies keyset cursor filtering and returns up to FetchLimit() rows
// for N+1 hasMore detection.
func (r *FlagRepository) List(_ context.Context, t tenant.TenantID, params query.ListParams) ([]*domain.FeatureFlag, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgFlagInvalidTenant, err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	tf := r.flags[t] // nil-safe: ranging over nil map is a no-op
	all := make([]*domain.FeatureFlag, 0, len(tf))
	for _, f := range tf {
		clone := *f
		all = append(all, &clone)
	}

	query.Sort(all, params.Sort, compareFlagField)
	result, err := query.ApplyCursor(all, params, flagFieldValue)
	if err != nil {
		return nil, fmt.Errorf("flag-repo: list: %w", err)
	}
	return result, nil
}

// compareFlagField compares a single field of two feature flags.
func compareFlagField(a, b *domain.FeatureFlag, field string) int {
	switch field {
	case "key":
		return cmp.Compare(a.Key, b.Key)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

// flagFieldValue extracts a cursor-comparable value from a feature flag.
func flagFieldValue(f *domain.FeatureFlag, field string) any {
	switch field {
	case "key":
		return f.Key
	case "id":
		return f.ID
	default:
		return ""
	}
}
