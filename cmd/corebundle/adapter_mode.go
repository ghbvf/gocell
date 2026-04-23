// adapter_mode.go: adapter mode 校验（mode coupling 与 allowlist 检查）。
package main

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// validateModeCoupling enforces that the DATA plane (cellAdapterMode) and
// CONTROL plane (adapterMode) agree on production posture. If the cell has
// committed to a real backend (postgres), operators MUST also set
// GOCELL_ADAPTER_MODE=real so key loading, /metrics, and /readyz?verbose
// run with production guards. Otherwise real persistence runs with dev-grade
// HMAC/cursor keys and unauthenticated control-plane endpoints — the exact
// split ops/security review flagged on PR #169.
//
// ref: go-zero serviceconf — single config drives all gates; misalignment is fatal.
// ref: go-micro mode/profile — runtime mode is observed by all subsystems.
func validateModeCoupling(cellAdapterMode, adapterMode string) error {
	if cellAdapterMode == "postgres" && adapterMode != "real" {
		return errcode.New(errcode.ErrValidationFailed,
			"GOCELL_CELL_ADAPTER_MODE=postgres requires GOCELL_ADAPTER_MODE=real "+
				"(real persistence demands production key loading, token-guarded "+
				"/metrics, and token-guarded /readyz?verbose)")
	}
	return nil
}

// validateAdapterMode rejects unrecognised GOCELL_ADAPTER_MODE values.
// Follows the project allowlist convention (cf. cell.ParseLevel, cmd/gocell/verify).
func validateAdapterMode(mode string) error {
	switch mode {
	case "", "real":
		return nil
	default:
		return fmt.Errorf("unknown GOCELL_ADAPTER_MODE %q; known values: \"\" (unset = dev) or \"real\"", mode)
	}
}
