//go:build archtest_fixture

package modulepathfunnelfixture

import "github.com/ghbvf/gocell/tools/archtest/internal/modulepathfunnelfixture/frag"

// This blank var reconstructs a platform CHILD path from a CROSS-PACKAGE const
// fragment (frag.Host = "github.com/ghbvf/") plus a literal tail "gocell/internal/y"
// — folding to "github.com/ghbvf/gocell/internal/y", which isBarePlatformValue
// matches (HasPrefix PlatformModulePath+"/"). The old same-package AST flatten
// could not resolve a cross-package selector (frag.Host was never in the
// same-package constMap); go/types folds it across the boundary, so the typed
// detector MUST flag this BinaryExpr. This is residual (b). (frag.Host alone is a
// FRAGMENT, not a bare value — only the reconstructed concat trips the detector.)
var _ = frag.Host + "gocell/internal/y"
