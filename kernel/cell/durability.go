package cell

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// DurabilityMode declares whether an assembly runs in demo or durable mode.
// Cells use this to reject noop/test implementations at Init() time,
// preventing "pseudo-success" assemblies in production.
//
// The zero value is intentionally invalid (unset), forcing callers to
// explicitly choose DurabilityDemo or DurabilityDurable.
// ref: Vault StoredKeysInvalid=0, gRPC InvalidSecurityLevel=0, net/http SameSite iota+1
type DurabilityMode int

const (
	// DurabilityDemo allows noop implementations (NoopWriter, NoopTxRunner,
	// DiscardPublisher). Used by examples/ and unit tests.
	DurabilityDemo DurabilityMode = iota + 1

	// DurabilityDurable rejects noop implementations at Init() time.
	// Used by production assemblies (e.g., cmd/core-bundle).
	DurabilityDurable
)

// String returns "demo", "durable", or "unset".
func (m DurabilityMode) String() string {
	switch m {
	case DurabilityDemo:
		return "demo"
	case DurabilityDurable:
		return "durable"
	default:
		return "unset"
	}
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

// ValidateMode returns an error if mode is not a known DurabilityMode.
// Use at assembly-start boundaries to reject misconfiguration early.
func ValidateMode(mode DurabilityMode) error {
	switch mode {
	case DurabilityDemo, DurabilityDurable:
		return nil
	default:
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("invalid DurabilityMode %d; explicitly choose DurabilityDemo or DurabilityDurable", int(mode)))
	}
}

// CheckNotNoop returns an error if mode is invalid, or if any dep implements
// Nooper and mode is DurabilityDurable. In DurabilityDemo mode, all deps are
// accepted. nil deps are silently skipped (nil checks belong in the caller).
func CheckNotNoop(mode DurabilityMode, cellID string, deps ...any) error {
	if err := ValidateMode(mode); err != nil {
		return errcode.Wrap(errcode.ErrValidationFailed,
			fmt.Sprintf("%s: DurabilityMode check", cellID), err)
	}
	if mode == DurabilityDemo {
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
