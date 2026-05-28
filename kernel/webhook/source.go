package webhook

import (
	"sync"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// SourceStore resolves a [SourceID] to its registered [Source]. The receiver
// looks up the secret for an inbound delivery through this interface, so a
// persistent / encrypted backing store (configcore, Vault) can replace the
// in-memory default without touching the verification path.
type SourceStore interface {
	// Lookup returns the source registered under id and true, or the zero
	// Source and false when id is unknown.
	Lookup(id SourceID) (Source, bool)
}

// SourceRegistry is an in-memory [SourceStore]. It is safe for concurrent use
// and is the default for single-process deployments, tests, and demos.
type SourceRegistry struct {
	mu      sync.RWMutex
	sources map[SourceID]Source
}

// NewSourceRegistry returns an empty in-memory registry.
func NewSourceRegistry() *SourceRegistry {
	return &SourceRegistry{sources: make(map[SourceID]Source)}
}

// Register adds or replaces the source registered under src.ID(). It fails if
// src is a zero-value Source (no secret), which cannot occur for a Source built
// via [NewSource].
func (r *SourceRegistry) Register(src Source) error {
	if len(src.secret) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: cannot register a source with an empty secret")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[src.id] = src
	return nil
}

// Lookup implements [SourceStore].
func (r *SourceRegistry) Lookup(id SourceID) (Source, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src, ok := r.sources[id]
	return src, ok
}

// compile-time assertion that the in-memory registry satisfies SourceStore.
var _ SourceStore = (*SourceRegistry)(nil)
