package cell

import (
	"fmt"
	"log/slog"

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
	// Used by production assemblies (e.g., cmd/corebundle).
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

// ParseDurabilityMode converts a cell.yaml durabilityMode string to the typed
// DurabilityMode enum. Parsing is case-sensitive and rejects trailing/leading
// whitespace — cell.yaml must declare exactly "demo" or "durable" when set.
//
// Empty string defaults to DurabilityDemo (fail-safe defaulting, K8s API
// defaulting same pattern as PodSpec.RestartPolicy default "Always"). Rationale:
// "cell.yaml missing durabilityMode" is equivalent to declaring demo — production
// assemblies running DurabilityDurable will still fail-fast at BaseCell.Init
// alignment, so this default cannot silently downgrade durability guarantees.
// L2+ cells should still declare durabilityMode explicitly (OUTGUARD-01 advisory).
//
// ref: kernel/cell/durability.go String() — partial inverse (empty defaults)
// ref: K8s API defaulting (PodSpec.RestartPolicy / DNSPolicy) — missing-field semantics
func ParseDurabilityMode(s string) (DurabilityMode, error) {
	switch s {
	case "":
		return DurabilityDemo, nil
	case "demo":
		return DurabilityDemo, nil
	case "durable":
		return DurabilityDurable, nil
	default:
		return 0, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			`cell.ParseDurabilityMode: must be "demo" or "durable"`,
			errcode.WithDetails(slog.String("got", s)),
		)
	}
}

// Nooper is a marker interface for test/demo-only implementations.
// Types that implement Nooper are rejected by CheckNotNoop when the
// assembly runs in DurabilityDurable mode.
//
// Kernel noop types (outbox.NoopWriter, outbox.DiscardPublisher) implement
// this interface; cells in Demo mode may register their own Nooper TxRunners.
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
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"invalid DurabilityMode; explicitly choose DurabilityDemo or DurabilityDurable",
			errcode.WithInternal(fmt.Sprintf("mode=%d", int(mode))))
	}
}

// CheckNotNoop returns an error if mode is invalid, or if any dep implements
// Nooper and mode is DurabilityDurable. In DurabilityDemo mode, all deps are
// accepted. nil deps are silently skipped (nil checks belong in the caller).
func CheckNotNoop(mode DurabilityMode, cellID string, deps ...any) error {
	if err := ValidateMode(mode); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
			"DurabilityMode check failed", err,
			errcode.WithInternal(fmt.Sprintf("cell=%s", cellID)))
	}
	if mode == DurabilityDemo {
		return nil
	}
	for _, dep := range deps {
		if dep == nil {
			continue
		}
		if n, ok := dep.(Nooper); ok && n.Noop() {
			return errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
				"durable mode rejects noop dependency; inject a real implementation",
				errcode.WithInternal(fmt.Sprintf("cell=%s type=%T", cellID, dep)))
		}
	}
	return nil
}
