// OUTBOX-RELAY-LOST-METRIC-01 — handleFailedEntry must observe Mark*'s updated
// return value and route stale-lease writebacks into the lost stat / metric.
//
// The PG outbox uses lease_id fencing: when a worker's lease is rotated by
// ReclaimStale (or a peer-replaced claim), MarkRetry / MarkDead return
// (updated=false, nil). Discarding that bool with `_, err :=` makes stale-lease
// writebacks invisible — they silently inflate stats.dead / stats.retried,
// hiding the real reclaim activity.
//
// Gate IDs:
//
//	OUTBOX-RELAY-LOST-METRIC-01-A  runtime/outbox/relay.go handleFailedEntry must
//	                                read Mark{Retry,Dead}'s first return into a
//	                                named bool (no `_` discard).
//	OUTBOX-RELAY-LOST-METRIC-01-B  PollCycleResult must declare a Lost field, so
//	                                a "lost" outcome is reportable end-to-end.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B6 PR-V1-PG-OUTBOX-RELAY-HARDEN
// ref: backlog2.md B2-A-05 PG-RELAY-FAIL-WRITE-UNHANDLED-ROWS
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestOutboxRelayLostMetric01_HandleFailedEntryReadsUpdated(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "runtime", "outbox", "relay.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var handle *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "handleFailedEntry" && fd.Recv != nil {
			handle = fd
			break
		}
	}
	if handle == nil {
		t.Fatalf("OUTBOX-RELAY-LOST-METRIC-01-A: handleFailedEntry not found in %s", src)
	}

	// Inspect every assignment whose RHS is a call ending in MarkRetry or
	// MarkDead. The LHS first ident MUST NOT be `_` — discarding the updated
	// bool collapses the lost-lease branch into an uncounted writeback.
	var violations []token.Pos
	ast.Inspect(handle.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "MarkRetry", "MarkDead":
		default:
			return true
		}
		if len(assign.Lhs) < 1 {
			return true
		}
		first, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || first.Name == "_" {
			violations = append(violations, assign.Pos())
		}
		return true
	})

	for _, p := range violations {
		t.Errorf(
			"OUTBOX-RELAY-LOST-METRIC-01-A: %s discards Mark{Retry,Dead}'s `updated bool` "+
				"(LHS is `_`); read it as `updated, err := r.store.Mark...(...)` and "+
				"route updated=false to stats.lost so stale leases stay observable.",
			fset.Position(p),
		)
	}
}

func TestOutboxRelayLostMetric01_PollCycleResultHasLostField(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "kernel", "outbox", "relay_metrics.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var hasLost bool
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "PollCycleResult" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if name.Name == "Lost" {
						hasLost = true
					}
				}
			}
		}
	}
	if !hasLost {
		t.Errorf(
			"OUTBOX-RELAY-LOST-METRIC-01-B: PollCycleResult must declare a Lost int field "+
				"(roadmap B6, backlog2 B2-A-05). It travels alongside Published/Retried/"+
				"Dead/Skipped so providerRelayCollector.RecordPollCycle can fire "+
				"`outbox_relayed_total{outcome=\"lost\"}`.",
		)
	}
}
