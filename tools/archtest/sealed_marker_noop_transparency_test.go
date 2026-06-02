// INVARIANT: SEALED-MARKER-NOOP-TRANSPARENCY-01
//
// SEALED-MARKER-NOOP-TRANSPARENCY-01 — every internalCell* concrete struct
// type anywhere under kernel/... must declare a `Noop() bool` method. Without
// it the sealed wrapper hides the inner Nooper signal from outbox.CheckNotNoop
// / mode_resolver.isNooperDep, letting durable assemblies silently accept demo
// runners/publishers/writers; it also keeps every sealed marker uniformly
// transparency-complete (a future Nooper-capable Emitter must not be hidden by
// the CellEmitter wrapper).
//
// AI-robust 评级：Hard (typed auto-discovery via go/types package scope).
// Run(t, Typed(...)) loads ./kernel/... and the rule walks pass.Pkg.Scope().Names() for
// internalCell*-prefixed struct TypeNames, asserting each carries Noop() bool
// in its method set. There is NO hand-maintained file list: a new sealed-marker
// package or type is discovered automatically, and an internalCell* struct
// without Noop() fails CI. Upgraded from the previous Medium AST-file-list scan
// (PR-A23 / #618 A.7).
//
// 盲区自检（所选工具 go/types scope walk 的声明范围外形态）:
//   - discovery keys on the `internalCell` name prefix (the documented
//     sealed-marker naming convention). A sealed marker struct NOT prefixed
//     internalCell would escape discovery. The Medium→Hard target of A.7 is
//     removing the hand-maintained *file list* (which silently skipped whole
//     files) — that is achieved: any file/package under kernel/ is now covered
//     without manual list maintenance. Fully name-agnostic discovery (by the
//     unexported sealed*() marker method) is a further Hard-ening tracked in
//     ADR 202605101900 amendment, out of scope here.
//   - pointer-receiver Noop: the existing markers all use value receivers, so
//     types.NewMethodSet(named) (value method set) suffices. A pointer-only
//     Noop would be missed; the `found < 3` floor below would NOT catch that,
//     but the per-type Noop assertion is the real guard and value receivers are
//     the established convention (mirrors kernel/persistence + kernel/outbox
//     cell_marker.go).
//
// **行为覆盖分工**：本 archtest 仅守 "method 存在 + 签名"（结构性约束）；
// runtime 透传行为由 unit test 守 —
//   - kernel/persistence/cell_marker_test.go::TestWrapForCell_PreservesNooperPassThrough / _NonNooperReturnsFalse
//   - kernel/outbox/cell_marker_test.go::TestWrapPublisherForCell_* / TestWrapWriterForCell_* /
//     TestWrapEmitterForCell_PreservesNooperPassThrough / _NonNooperReturnsFalse
//
// archtest 加 runtime 行为断言是反模式（static analysis 工具不应内嵌运行时）；
// 双层防线分工已完整。
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1 Noop passthrough
package archtest

import (
	"go/types"
	"strings"
	"testing"
)

// INVARIANT: SEALED-MARKER-NOOP-TRANSPARENCY-01
//
// TestSealedMarkerNoopTransparency01 auto-discovers every `internalCell*`
// concrete struct type under kernel/... and asserts each declares a
// `Noop() bool` method. This prevents a refactor from silently removing the
// Noop pass-through (breaking outbox.CheckNotNoop's durable-mode rejection)
// and removes the prior hand-maintained file list (a new sealed-marker file
// could be silently skipped).
func TestSealedMarkerNoopTransparency01(t *testing.T) {
	t.Parallel()

	found := 0
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./kernel/..."}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		scope := p.Pkg.Scope()
		for _, name := range scope.Names() {
			if !strings.HasPrefix(name, "internalCell") {
				continue
			}
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			if _, isStruct := named.Underlying().(*types.Struct); !isStruct {
				continue
			}
			found++
			if !hasNoopBoolMethod(named) {
				t.Errorf("SEALED-MARKER-NOOP-TRANSPARENCY-01: %s.%s missing Noop() bool method — "+
					"sealed wrapper must expose inner Nooper signal for outbox.CheckNotNoop / isNooperDep",
					p.Pkg.Path(), name)
			}
		}
		return nil
	})

	// Floor guard against a silent discovery break (typed Production scope resolving
	// nothing, prefix typo, etc.): the known sealed markers are
	// internalCellTxManager (kernel/persistence) + internalCellPublisher /
	// internalCellWriter / internalCellEmitter (kernel/outbox) = 4.
	// internalCellCheckpointStore (kernel/projection) was removed with
	// WrapCheckpointStoreForCell when the bootstrap-owned projection harness
	// made the cell-level sealed marker dead code (#1285 / #834). Assert we
	// found at least those so a load regression cannot make this archtest
	// vacuously pass. Bump this floor whenever a sealed marker is added.
	if found < 4 {
		t.Fatalf("SEALED-MARKER-NOOP-TRANSPARENCY-01: discovered only %d internalCell* struct types under "+
			"kernel/...; expected ≥4 (auto-discovery likely broken)", found)
	}
}

// hasNoopBoolMethod reports whether named's value method set contains a
// `Noop() bool` method (no params, single bool result). The sealed markers use
// value receivers, so the value method set suffices.
func hasNoopBoolMethod(named *types.Named) bool {
	ms := types.NewMethodSet(named)
	for i := 0; i < ms.Len(); i++ {
		fn, ok := ms.At(i).Obj().(*types.Func)
		if !ok || fn.Name() != "Noop" {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Params().Len() != 0 || sig.Results().Len() != 1 {
			continue
		}
		if b, ok := sig.Results().At(0).Type().(*types.Basic); ok && b.Kind() == types.Bool {
			return true
		}
	}
	return false
}
