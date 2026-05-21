package accesscoretest

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// FakeConfigEntry is a value-type mirror of ports.ConfigEntry for use in test
// stubs. It is exported separately so callers can write stub maps without
// importing cells/accesscore/internal/ports directly.
type FakeConfigEntry struct {
	Key       string
	Value     string
	Sensitive bool
	Version   int
}

// ConfigGetterStub describes the response for a single key lookup.
// When Entry is non-nil and Err is nil, GetEntry returns the entry.
// When Err is non-nil, GetEntry returns the error.
// When both are nil (stub has the key but Entry is nil), GetEntry returns
// ErrConfigNotFound — the stub represents a key that is explicitly absent.
type ConfigGetterStub struct {
	Entry *FakeConfigEntry // nil → return Err (or ErrConfigNotFound if Err also nil)
	Err   error
}

// FakeConfigGetter is a stub implementation of ports.ConfigGetter for use in
// tests. It is concurrency-safe. Unknown keys (not in the stubs map) return
// ErrConfigNotFound by convention so test authors do not need to explicitly
// stub every key they do not care about.
//
// Usage:
//
//	g := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
//	    "jwt.ttl": {Entry: &accesscoretest.FakeConfigEntry{Key: "jwt.ttl", Value: "3600", Version: 1}},
//	    "bad-key": {Err: someErr},
//	})
type FakeConfigGetter struct {
	mu    sync.Mutex
	stubs map[string]ConfigGetterStub
	calls []string
}

// Compile-time assertion: FakeConfigGetter must satisfy ports.ConfigGetter.
var _ ports.ConfigGetter = (*FakeConfigGetter)(nil)

// NewFakeConfigGetter constructs a FakeConfigGetter with the given stubs.
// A nil or empty stubs map means all keys return ErrConfigNotFound.
func NewFakeConfigGetter(stubs map[string]ConfigGetterStub) *FakeConfigGetter {
	if stubs == nil {
		stubs = map[string]ConfigGetterStub{}
	}
	return &FakeConfigGetter{stubs: stubs}
}

// GetEntry implements ports.ConfigGetter. It records the call and returns the
// stubbed response for the given key. Unknown keys return ErrConfigNotFound.
func (g *FakeConfigGetter) GetEntry(_ context.Context, key string) (ports.ConfigEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, key)

	stub, ok := g.stubs[key]
	if !ok {
		return ports.ConfigEntry{}, errcode.New(
			errcode.KindNotFound, errcode.ErrConfigNotFound,
			"fake config getter: key not found",
		)
	}
	if stub.Err != nil {
		return ports.ConfigEntry{}, stub.Err
	}
	if stub.Entry == nil {
		return ports.ConfigEntry{}, errcode.New(
			errcode.KindNotFound, errcode.ErrConfigNotFound,
			"fake config getter: key not found",
		)
	}
	return ports.ConfigEntry{
		Key:       stub.Entry.Key,
		Value:     stub.Entry.Value,
		Sensitive: stub.Entry.Sensitive,
		Version:   stub.Entry.Version,
	}, nil
}

// Calls returns the ordered list of keys passed to GetEntry since the last
// Reset. Returns a copy; mutating the slice does not affect the recorder.
func (g *FakeConfigGetter) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.calls))
	copy(out, g.calls)
	return out
}

// Reset clears the call log. Stubs are not modified.
func (g *FakeConfigGetter) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = nil
}
