// Package red_imports_runtime is a RED fixture for
// TestKernelCell_DoesNotImportRuntime: it imports a package whose path
// contains "runtime/" which scanPassForForbiddenImports must flag.
package red_imports_runtime

import "runtime/debug"

// UseDebug references runtime/debug so the import is not pruned.
var _ = debug.Stack
