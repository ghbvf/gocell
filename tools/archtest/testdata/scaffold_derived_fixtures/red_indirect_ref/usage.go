// Package red_indirect_ref is a RED fixture for SCAFFOLD-DERIVED-FORCEOVERWRITE-01:
// taking the DerivedOverwrite function value (an indirect reference, not a
// direct CallExpr) must be flagged — it would otherwise defeat a CallExpr-only
// caller-allowlist scan.
package red_indirect_ref

import "github.com/ghbvf/gocell/framework/pkg/pathsafe"

// Sink captures the force-overwrite constructor as a function value.
var Sink = pathsafe.DerivedOverwrite
