//go:build archtest_fixture

package modulepathfunnelfixture

import "github.com/ghbvf/gocell/tools/archtest/internal/modulepathfunnelfixture/frag"

// This blank var reconstructs the bare platform module path from a CROSS-PACKAGE
// const fragment (frag.Host) plus a literal tail. The old same-package AST flatten
// could not resolve a cross-package selector (frag.Host was never in the
// same-package constMap); go/types folds it across the boundary, so the typed
// detector MUST flag this BinaryExpr. This is residual (b).
var _ = frag.Host + "gocell/internal/y"
