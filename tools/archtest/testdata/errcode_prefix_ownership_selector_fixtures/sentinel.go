// Package errcodeselectorfixture is a typed red/green fixture for
// ERRCODE-PREFIX-OWNERSHIP-01 Target B (exported Code sentinels).
//
// It exercises residual #2 (non-literal sentinel): before the typed-const-eval
// fix, scanSentinelValueSpec only matched *ast.BasicLit string values, so a
// sentinel whose value is a const SelectorExpr (codes.Unregistered) or a
// same-package const Ident (localUnregistered) was silently skipped — an
// unregistered prefix could be laundered into an exported sentinel. With the
// fix the scan resolves the value through EvaluateConstString and flags it.
//
// Loaded ONLY via StandaloneModule (typed) in
// TestErrcodePrefixOwnership01_SentinelConstEval — const SelectorExpr/Ident
// resolution requires go/types, unavailable in AST-only mode.
package errcodeselectorfixture

import (
	"github.com/ghbvf/gocell/pkg/errcode"

	"fixturetest/errcode_prefix_ownership_selector/codes"
)

// localUnregistered is an unregistered-prefix code as a same-package const,
// referenced by ErrIdentLaunder via an *ast.Ident (not a BasicLit).
const localUnregistered = "ERR_IDENTBOGUS_NOPE"

// ErrSelectorLaunder (RED): value is a const SelectorExpr → must be flagged.
var ErrSelectorLaunder errcode.Code = codes.Unregistered

// ErrIdentLaunder (RED): value is a same-package const Ident → must be flagged.
var ErrIdentLaunder errcode.Code = localUnregistered

// ErrRegisteredSelector (GREEN control): value resolves to a registered
// platform code → must NOT be flagged. Proves the typed scan is selective,
// not a blanket flag on every non-literal sentinel (anti-vacuity).
var ErrRegisteredSelector errcode.Code = codes.RegisteredOK
