// RMQ-CHANNEL-MAX-PER-CONN-01 — Connection must enforce a configurable
// per-connection channel cap so a runaway publisher/subscriber count cannot
// blow past the broker's `channel_max` (default 2047) and trigger a
// connection-level shutdown.
//
// Gate IDs:
//
//	RMQ-CHANNEL-MAX-PER-CONN-01-A  Config struct must declare MaxChannelsPerConn int.
//	RMQ-CHANNEL-MAX-PER-CONN-01-B  setDefaults must populate MaxChannelsPerConn
//	                                with the documented default constant
//	                                (defaultRMQMaxChannelsPerConn = 256).
//	RMQ-CHANNEL-MAX-PER-CONN-01-C  AcquireChannel must reference an inUseChannels
//	                                counter (the atomic guard that returns
//	                                ErrAdapterAMQPChannelMaxExceeded when the
//	                                cap is reached).
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: rabbitmq/amqp091-go connection.go openTune — broker channel_max negotiation
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

const expectedDefaultMaxChannelsPerConnConst = "defaultRMQMaxChannelsPerConn"

func TestRMQChannelMaxPerConn01_ConfigFieldExists(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "connection.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var hasField bool
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Config" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				for _, name := range field.Names {
					if name.Name == "MaxChannelsPerConn" {
						hasField = true
					}
				}
			}
		}
	}

	if !hasField {
		t.Errorf(
			"RMQ-CHANNEL-MAX-PER-CONN-01-A: rabbitmq.Config must declare " +
				"`MaxChannelsPerConn int` so callers can bound channel allocation per " +
				"physical AMQP connection. Default 256 prevents broker channel_max " +
				"(default 2047) exhaustion.",
		)
	}
}

func TestRMQChannelMaxPerConn01_SetDefaultsPopulatesField(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "connection.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var setDefaults *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "setDefaults" && fd.Recv != nil {
			setDefaults = fd
			break
		}
	}
	if setDefaults == nil {
		t.Fatalf("RMQ-CHANNEL-MAX-PER-CONN-01-B: Config.setDefaults not found in %s", src)
	}

	// Look for an if-statement whose condition is `<recv>.MaxChannelsPerConn <= 0`
	// and whose body assigns MaxChannelsPerConn from the documented default constant.
	//
	// The condition must be <= 0 (not == 0) so that negative values are also
	// treated as "not configured" and receive the fail-closed default.
	// Accepting == 0 only would allow callers to pass -1 and silently skip the cap.
	var assigns bool
	var conditionIsLEQ bool
	ast.Inspect(setDefaults.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// Check if the condition is `<recv>.MaxChannelsPerConn <= 0`.
		bin, ok := ifStmt.Cond.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		if bin.Op != token.LEQ {
			return true
		}
		// LHS must be a selector ending in MaxChannelsPerConn.
		lhsSel, ok := bin.X.(*ast.SelectorExpr)
		if !ok || lhsSel.Sel.Name != "MaxChannelsPerConn" {
			return true
		}
		// RHS must be the literal 0.
		rhs, ok := bin.Y.(*ast.BasicLit)
		if !ok || rhs.Kind != token.INT || rhs.Value != "0" {
			return true
		}
		// Found if MaxChannelsPerConn <= 0 — now verify body assigns default constant.
		conditionIsLEQ = true
		ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
			assign, ok := inner.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 {
				return true
			}
			sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "MaxChannelsPerConn" {
				return true
			}
			if len(assign.Rhs) != 1 {
				return true
			}
			ident, ok := assign.Rhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == expectedDefaultMaxChannelsPerConnConst {
				assigns = true
				return false
			}
			return true
		})
		return true
	})

	if !conditionIsLEQ {
		t.Errorf(
			"RMQ-CHANNEL-MAX-PER-CONN-01-B: Config.setDefaults must guard the " +
				"MaxChannelsPerConn assignment with `<= 0` (not `== 0`). " +
				"A negative value passed by a caller must also fall back to the " +
				"default (256) — accepting only == 0 allows -1 to bypass the cap " +
				"and produce a production outage.",
		)
	}
	if !assigns {
		t.Errorf(
			"RMQ-CHANNEL-MAX-PER-CONN-01-B: Config.setDefaults must assign "+
				"MaxChannelsPerConn from the documented default constant `%s` (=256). "+
				"Hardcoded literals defeat the single-source default and drift from "+
				"the godoc on Config.MaxChannelsPerConn.",
			expectedDefaultMaxChannelsPerConnConst,
		)
	}
}

func TestRMQChannelMaxPerConn01_AcquireChannelGuardsCounter(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "connection.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var acquire *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "AcquireChannel" && fd.Recv != nil {
			acquire = fd
			break
		}
	}
	if acquire == nil {
		t.Fatalf("RMQ-CHANNEL-MAX-PER-CONN-01-C: AcquireChannel method not found in %s", src)
	}

	var refersToCounter bool
	ast.Inspect(acquire.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "inUseChannels" {
			refersToCounter = true
			return false
		}
		return true
	})

	if !refersToCounter {
		t.Errorf(
			"RMQ-CHANNEL-MAX-PER-CONN-01-C: AcquireChannel must reference the " +
				"`inUseChannels` atomic counter to bound new-channel creation against " +
				"Config.MaxChannelsPerConn; current source has no such reference. " +
				"Without the counter, pool-miss paths can silently exceed broker " +
				"channel_max and cause a connection-level shutdown.",
		)
	}
}
