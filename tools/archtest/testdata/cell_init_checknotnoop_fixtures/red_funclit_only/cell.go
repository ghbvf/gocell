//go:build archtest_fixture

// Package redfunclitonly models an L2+ cell whose Init body contains a
// CheckNotNoop call ONLY inside an unexecuted closure (FuncLit) — the
// closure is constructed and discarded, never invoked. Phase B's BFS must
// stop at FuncLit boundaries and emit a diagnostic for this package; if
// it descends into the FuncLit body it would falsely report the cell as
// satisfying the rule.
//
// ref: golang.org/x/tools/go/ast/inspector.Nodes — proceed=false on
//
//	FuncLit push event is the standard library equivalent of this
//	boundary semantic.
package redfunclitonly

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

// RedFuncLitCell is the fake L2+ cell type. The archtest matches it via
// cell.yaml goStructName == "RedFuncLitCell".
type RedFuncLitCell struct{}

// Init defines a closure that calls CheckNotNoop but never invokes it.
// The Init synchronous path returns without calling the guard.
func (c *RedFuncLitCell) Init(ctx context.Context, reg any) error {
	_ = ctx
	_ = reg
	// Closure constructed but never executed. Phase B must NOT count this
	// as a satisfying call.
	guard := func() error {
		return cell.CheckNotNoop(cell.DurabilityDemo, "fixture-red_funclit_only")
	}
	_ = guard // captured-but-not-called; pure dead code from Init's perspective
	return nil
}
