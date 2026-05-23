//go:build archtest_fixture

// Package redhyphen is a RED fixture for READYZ-PROBE-NAMING-01. It calls
// kernel/healthz.NewProbe with hyphenated constant probe names, which the rule
// MUST flag — proving both the callee resolution (cross-package selector) and
// the EvaluateConstString-then-hyphen detection (BasicLit and const Ident).
package redhyphen

import (
	"context"

	"github.com/ghbvf/gocell/kernel/healthz"
)

// badConst is a hyphenated probe name bound to a const — exercises the
// EvaluateConstString const-Ident folding path.
const badConst = "widget-repo-ready"

func noop(context.Context) error { return nil }

// runtimeID is a non-constant segment used to build a composed probe name,
// exercising the string-literal-operand path of the rule.
func runtimeID() string { return "widget" }

// Package-level vars keep the calls in production (non-test) AST.
var (
	_ = healthz.NewProbe("bad-literal-ready", noop)       // BasicLit form
	_ = healthz.NewProbe(badConst, noop)                  // const-Ident form
	_ = healthz.NewProbe("prefix-bad_"+runtimeID(), noop) // composed: literal-operand hyphen
)
