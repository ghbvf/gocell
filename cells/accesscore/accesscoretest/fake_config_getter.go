package accesscoretest

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ConfigGetterStub is the sealed value type accepted by NewFakeConfigGetter.
// The fields are unexported; the only way to construct one is via the four
// typed constructors below. This makes self-contradictory states like
// "Sensitive=true with plaintext Value" unrepresentable in the type system
// — selecting the wrong stub semantic is selecting the wrong constructor
// name, surfaced at compile time.
//
// AI Hard form: "typed function choice" — one API per semantic, no flag
// fields decoded at runtime.
type ConfigGetterStub struct {
	entry *fakeConfigEntry
	err   error
}

// fakeConfigEntry mirrors ports.ConfigEntry but lives behind unexported
// constructors so the Sensitive/Value redaction contract cannot be violated
// from outside this package.
type fakeConfigEntry struct {
	key       string
	value     string
	sensitive bool
	version   int
}

// PresentStub returns a stub for a non-sensitive config key whose value is
// returned to callers via GetEntry. Use this for the common "key exists with
// plaintext value" path.
func PresentStub(key, value string, version int) ConfigGetterStub {
	return ConfigGetterStub{entry: &fakeConfigEntry{
		key: key, value: value, sensitive: false, version: version,
	}}
}

// SensitiveStub returns a stub mirroring what configcore's internal HTTP
// getter returns for keys flagged Sensitive=true: Value is the redacted
// placeholder "******". This matches the production redaction contract
// documented in cells/accesscore/internal/ports/configport.go and lets tests
// assert the "reload triggers but plaintext never revealed" path without
// hard-coding the redaction sentinel in every test.
func SensitiveStub(key string, version int) ConfigGetterStub {
	return ConfigGetterStub{entry: &fakeConfigEntry{
		key: key, value: "******", sensitive: true, version: version,
	}}
}

// NotFoundStub returns a stub representing a key that is explicitly absent;
// GetEntry returns ErrConfigNotFound with CategoryDomain. Use this when
// stubbing stale-event paths (configcore reports the key was upserted but
// the subsequent fetch races a delete and finds nothing).
func NotFoundStub() ConfigGetterStub {
	return ConfigGetterStub{}
}

// ErrorStub returns a stub that surfaces the supplied error from GetEntry.
// Use for transient or permanent error coverage; the error is returned to
// the caller verbatim (no wrapping).
func ErrorStub(err error) ConfigGetterStub {
	return ConfigGetterStub{err: err}
}

// FakeConfigGetter is a stub implementation of ports.ConfigGetter for use in
// tests. It is concurrency-safe. Unknown keys (not in the stubs map) return
// ErrConfigNotFound by convention so test authors do not need to explicitly
// stub every key they do not care about.
//
// Usage:
//
//	g := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
//	    "jwt.ttl":   accesscoretest.PresentStub("jwt.ttl", "3600", 1),
//	    "kms.key":   accesscoretest.SensitiveStub("kms.key", 4),
//	    "stale.key": accesscoretest.NotFoundStub(),
//	    "broken":    accesscoretest.ErrorStub(myTransientErr),
//	})
type FakeConfigGetter struct {
	mu    sync.Mutex
	stubs map[string]ConfigGetterStub
	calls []string
}

// Compile-time assertion: FakeConfigGetter must satisfy ports.ConfigGetter.
var _ ports.ConfigGetter = (*FakeConfigGetter)(nil)

// NewFakeConfigGetter constructs a FakeConfigGetter with a deep copy of the
// supplied stubs map. Subsequent mutations of the caller's map (or of any
// ConfigGetterStub value re-used as a map alias) do not affect the fake.
// A nil or empty stubs map means all keys return ErrConfigNotFound.
func NewFakeConfigGetter(stubs map[string]ConfigGetterStub) *FakeConfigGetter {
	cp := make(map[string]ConfigGetterStub, len(stubs))
	for k, v := range stubs {
		if v.entry != nil {
			entryCopy := *v.entry
			v.entry = &entryCopy
		}
		cp[k] = v
	}
	return &FakeConfigGetter{stubs: cp}
}

// GetEntry implements ports.ConfigGetter. It honors ctx.Err() (matching the
// production HTTP getter's cancellation semantic), records the call, and
// returns the stubbed response for the given key. Unknown keys and
// NotFoundStub entries return ErrConfigNotFound with CategoryDomain.
func (g *FakeConfigGetter) GetEntry(ctx context.Context, key string) (ports.ConfigEntry, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigEntry{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, key)

	stub, ok := g.stubs[key]
	if !ok || (stub.entry == nil && stub.err == nil) {
		return ports.ConfigEntry{}, errcode.New(
			errcode.KindNotFound, errcode.ErrConfigNotFound,
			"fake config getter: key not found",
			errcode.WithCategory(errcode.CategoryDomain),
		)
	}
	if stub.err != nil {
		return ports.ConfigEntry{}, stub.err
	}
	return ports.ConfigEntry{
		Key:       stub.entry.key,
		Value:     stub.entry.value,
		Sensitive: stub.entry.sensitive,
		Version:   stub.entry.version,
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
