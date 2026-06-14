// Package errcodeselectorfixture is a typed red/green fixture for
// ERRCODE-PREFIX-OWNERSHIP-01 Target B (exported Code sentinels).
//
// It exercises residual #2 (non-literal sentinel): before the typed-const-eval
// fix, scanSentinelValueSpec only matched *ast.BasicLit string values, so a
// sentinel whose value is a const SelectorExpr (codes.Unregistered), a typed
// const SelectorExpr (codes.UnregisteredTyped), or a same-package const Ident
// (localUnregistered) was silently skipped — an unregistered prefix could be
// laundered into an exported sentinel. With the fix the scan resolves the value
// through EvaluateConstString and flags it.
//
// It also exercises the isErrcodeCodeSentinel type gate via two NON-Code Err*
// sentinels (errIgnoredStdlib is error; errIgnoredErrcodeNew is *errcode.Error)
// that share the Err* convention but must be EXCLUDED — proving the typed scan
// is type-selective, not name-only.
//
// Loaded ONLY via StandaloneModule (typed) in
// TestErrcodePrefixOwnership01_SentinelConstEval — const SelectorExpr/Ident
// resolution requires go/types, unavailable in AST-only mode.
package errcodeselectorfixture

import (
	"errors"

	"github.com/ghbvf/gocell/framework/pkg/errcode"

	"fixturetest/errcode_prefix_ownership_selector/codes"
)

// localUnregistered is an unregistered-prefix code as a same-package const,
// referenced by ErrIdentLaunder via an *ast.Ident (not a BasicLit).
const localUnregistered = "ERR_IDENTBOGUS_NOPE"

// ── RED: errcode.Code-typed sentinels with unregistered non-literal values ──

// ErrSelectorLaunder (RED): value is an untyped const SelectorExpr → must be flagged.
var ErrSelectorLaunder errcode.Code = codes.Unregistered

// ErrTypedSelectorLaunder (RED): value is a typed errcode.Code const SelectorExpr → must be flagged.
var ErrTypedSelectorLaunder errcode.Code = codes.UnregisteredTyped

// ErrIdentLaunder (RED): value is a same-package const Ident → must be flagged.
var ErrIdentLaunder errcode.Code = localUnregistered

// ── GREEN: in-scope but registered → must NOT be flagged ──

// ErrRegisteredSelector (GREEN control): value resolves to a registered
// platform code → must NOT be flagged. Proves the typed scan is selective,
// not a blanket flag on every non-literal sentinel (anti-vacuity).
var ErrRegisteredSelector errcode.Code = codes.RegisteredOK

// ── EXCLUDED: exported Err*-named but NOT errcode.Code → type gate must drop ──
// These pass the isExportedErrSentinelName name gate and carry an "ERR_…"
// string, yet must produce NO diagnostic — proving isErrcodeCodeSentinel
// excludes by declared type, not by name/value shape.

// ErrIgnoredStdlib is an `error` sentinel (stdlib errors.New) — not errcode.Code.
var ErrIgnoredStdlib = errors.New("ERR_STDLIBIGNORED_NOPE")

// ErrIgnoredErrcodeNew is an `*errcode.Error` sentinel (errcode.New) — not
// errcode.Code. Its inner mint uses a registered code so Target A stays green.
var ErrIgnoredErrcodeNew = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
	"ignored non-Code sentinel")
