package accesscoretest

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ConfigGetterStub holds the preset response for a single key.
// When Entry is non-nil it is returned; otherwise Err is returned.
// If both are nil, ErrConfigNotFound is returned.
type ConfigGetterStub struct {
	Entry *ports.ConfigEntry
	Err   error
}

// FakeConfigGetter implements ports.ConfigGetter for unit tests.
// It returns preset stubs per key; unregistered keys return errcode.ErrConfigNotFound.
type FakeConfigGetter struct {
	mu    sync.Mutex
	stubs map[string]ConfigGetterStub
	calls []string
}

// NewFakeConfigGetter returns a FakeConfigGetter with the given stubs.
// Pass an empty or nil map to get a getter that always returns ErrConfigNotFound.
func NewFakeConfigGetter(stubs map[string]ConfigGetterStub) *FakeConfigGetter {
	m := make(map[string]ConfigGetterStub, len(stubs))
	for k, v := range stubs {
		m[k] = v
	}
	return &FakeConfigGetter{stubs: m}
}

// GetEntry satisfies ports.ConfigGetter. Records the key in the call log.
func (g *FakeConfigGetter) GetEntry(_ context.Context, key string) (ports.ConfigEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, key)
	stub, ok := g.stubs[key]
	if !ok {
		return ports.ConfigEntry{}, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config entry not found",
			errcode.WithCategory(errcode.CategoryDomain))
	}
	if stub.Err != nil {
		return ports.ConfigEntry{}, stub.Err
	}
	if stub.Entry != nil {
		return *stub.Entry, nil
	}
	return ports.ConfigEntry{}, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config entry not found",
		errcode.WithCategory(errcode.CategoryDomain))
}

// Calls returns the ordered list of keys passed to GetEntry since construction
// or the last Reset.
func (g *FakeConfigGetter) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.calls))
	copy(out, g.calls)
	return out
}

// Reset clears the call log.
func (g *FakeConfigGetter) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = g.calls[:0]
}

// compile-time interface check.
var _ ports.ConfigGetter = (*FakeConfigGetter)(nil)
