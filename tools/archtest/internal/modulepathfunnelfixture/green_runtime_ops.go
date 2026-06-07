//go:build archtest_fixture

package modulepathfunnelfixture

import "strings"

// greenRuntimeOps reconstructs the platform module path at RUNTIME via
// strings.Join — a function call, not a compile-time constant. go/types cannot
// fold it (info.Types[expr].Value is nil), so the typed detector cannot reach it.
// The detector MUST NOT flag it: this is the documented PERMANENT residual (c).
// Closing it would require SSA/dataflow analysis, beyond archtest's ceiling; such
// deliberate obfuscation would not survive code review. Its presence here,
// asserted NOT-flagged, documents the honest boundary (not a vacuity miss).
var _ = strings.Join([]string{"github.com/ghbvf/", "gocell"}, "")
