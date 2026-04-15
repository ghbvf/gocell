package cell

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// DurabilityMode declares whether an assembly runs in demo or durable mode.
// Cells use this to reject noop/test implementations at Init() time,
// preventing "pseudo-success" assemblies in production.
//
// ref: Spring @Profile (advisory) / Uber fx (no equivalent) / Watermill (no equivalent).
// GoCell's fail-fast approach is stricter than all three reference frameworks.
type DurabilityMode int

const (
	// DurabilityDemo allows noop implementations (NoopWriter, NoopTxRunner,
	// DiscardPublisher). Used by examples/ and unit tests.
	DurabilityDemo DurabilityMode = iota

	// DurabilityDurable rejects noop implementations at Init() time.
	// Used by production assemblies (e.g., cmd/core-bundle).
	DurabilityDurable
)

// String returns "demo" or "durable".
func (m DurabilityMode) String() string {
	if m == DurabilityDurable {
		return "durable"
	}
	return "demo"
}

// Nooper is a marker interface for test/demo-only implementations.
// Types that implement Nooper are rejected by CheckNotNoop when the
// assembly runs in DurabilityDurable mode.
//
// Kernel noop types (outbox.NoopWriter, outbox.DiscardPublisher,
// persistence.NoopTxRunner) implement this interface.
type Nooper interface {
	Noop() bool
}

// CheckNotNoop returns an error if any dep implements Nooper and mode is
// DurabilityDurable. In DurabilityDemo mode, all deps are accepted.
// nil deps are silently skipped (nil checks belong in the caller).
func CheckNotNoop(mode DurabilityMode, cellID string, deps ...any) error {
	if mode != DurabilityDurable {
		return nil
	}
	for _, dep := range deps {
		if dep == nil {
			continue
		}
		if n, ok := dep.(Nooper); ok && n.Noop() {
			return errcode.New(errcode.ErrCellMissingOutbox,
				fmt.Sprintf("%s: durable mode rejects %T; inject a real implementation", cellID, dep))
		}
	}
	return nil
}
