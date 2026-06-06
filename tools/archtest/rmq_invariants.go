// INVARIANT: RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
//   - INVARIANT: RMQ-CHANNEL-MAX-PER-CONN-01
//   - INVARIANT: RMQ-PUBLISHER-FAILURE-HANDLING-01
//   - INVARIANT: RMQ-PUBLISHER-RELEASES-CHANNEL-01
//   - INVARIANT: RMQ-STOPINTAKE-INFLIGHT-WAIT-01
//
// rmq_invariants.go — importable RabbitMQ adapter rule logic.
//
// This is the non-test home of the RMQ-* scanner helpers so they can be
// compiled by external Cell repositories through the CellRule pattern (Go
// never compiles a dependency's _test.go, so rule logic that external repos
// must run cannot live in a _test.go file). GoCell's own TestRMQ* functions
// (rmq_invariants_test.go) call the same shared helpers — single source, no
// parallel rule body.
//
// register=no — gocell-internal-layout (scans adapters/rabbitmq), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks; migrated for unified
// PlatformModulePath parameterization + fork-safety, dogfooded via the per-rule
// Tests.
//
// Platform-symbol paths are anchored to [PlatformModulePath] so a module
// rename updates exactly one place and no bare literal appears here. The scan
// SCOPE (./adapters/rabbitmq/...) is the running module's own adapter package;
// this is intentionally gocell-internal-layout and dogfood-only — in an
// external repo with no such adapter tree the rule does not get a clean pass.
//
// # RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
//
// Enforces that every AMQPChannel destruction site in adapters/rabbitmq/ goes
// through Connection.CloseEphemeralChannel. Direct ch.Close() calls outside of
// CloseEphemeralChannel or waitAndClose bypass the inUseChannels.Add(-1)
// decrement and permanently leak MaxChannelsPerConn slots, causing spurious
// ERR_ADAPTER_AMQP_CHANNEL_MAX_EXCEEDED false-positives after enough reconnect
// cycles or subscription teardowns.
//
// AI-robust grading:
//   - Downstream: Medium — go/types receiver classification via
//     types.Implements; naming-immune (renaming the receiver var does not
//     weaken the gate); allowlisted funcs are string-name matched (Soft
//     residual — renaming CloseEphemeralChannel bypasses without scanner
//     update; tracked as known blind spot: function-name allowlist is not
//     type-qualified).
//   - Upstream: N/A — this is a single-package restriction rule, not a
//     funnel-type constraint.
//
// # RMQ-CHANNEL-MAX-PER-CONN-01
//
// Enforces that adapters/rabbitmq.Config declares MaxChannelsPerConn with a
// setDefaults guard (`<= 0`) and AcquireChannel references the inUseChannels
// counter. Without the cap, pool-miss paths can silently exceed broker
// channel_max and cause a connection-level shutdown.
//
// AI-robust grading (all sub-rules: A/B/C):
//   - Medium — pure AST struct-field and selector scan; not type-qualified.
//     Hard upgrade path: generate a typed golden that locks the Config struct
//     field presence and setDefaults assignment form.
//
// # RMQ-PUBLISHER-FAILURE-HANDLING-01
//
// Enforces that Publisher.Publish in adapters/rabbitmq/publisher.go:
//   - references ErrAdapterAMQPNack (distinct from ErrAdapterAMQPConfirmTimeout)
//   - calls slog.Warn at least 3 times (NACK / timeout / confirmCh-closed)
//   - calls RecordPublishFailure at least once
//   - pairs every error-returning if-block with a RecordPublishFailure call
//
// AI-robust grading:
//   - Medium — AST identifier and call-expression scan; string-name matching
//     (renaming slog.Warn or RecordPublishFailure bypasses without scanner
//     update). The -D sub-rule (AllReturnsMustRecord) uses a block-recursive
//     walker that covers ForStmt / RangeStmt / SwitchStmt / TypeSwitchStmt /
//     SelectStmt containers (Wave 4 extension). Blind spot: the string-name
//     gate means a renamed callee bypasses silently (documented in RED fixture).
//
// # RMQ-PUBLISHER-RELEASES-CHANNEL-01
//
// Enforces that Publisher.Publish acquires a channel via AcquireChannel and
// pairs it with a deferred CloseEphemeralChannel or ReleaseChannel call.
// Without this pairing, each Publish leaks one inUseChannels slot; after
// MaxChannelsPerConn (=256) publishes all subsequent calls fail with
// ErrAdapterAMQPChannelMaxExceeded.
//
// AI-robust grading:
//   - Medium — AST DeferStmt + SelectorExpr name scan; not type-qualified.
//
// # RMQ-STOPINTAKE-INFLIGHT-WAIT-01
//
// Enforces that Subscriber.StopIntake waits for in-flight processDelivery
// goroutines before returning, and that drainRemaining uses a detached context
// (context.WithoutCancel) with no bare ctx.Done case. Also enforces that
// StopIntake does NOT call localWg.Wait() directly (Add-after-Wait race).
//
// AI-robust grading:
//   - Medium — AST call-expression and CommClause name scan; not type-qualified.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// rmqAdapterPkgPath is the canonical import path of the rabbitmq adapter
// package — derived from PlatformModulePath so no bare literal appears here.
const rmqAdapterPkgPath = PlatformModulePath + "/adapters/rabbitmq"

// rmqRel returns the module-relative slash path of src under root, used as the
// Diagnostic.Rel for the parser-based rmq single-file scans so every diagnostic
// (including the parse-failure and structural-absence branches) is clickable.
func rmqRel(root, src string) string {
	rel, err := filepath.Rel(root, src)
	if err != nil || rel == "" {
		return filepath.ToSlash(src)
	}
	return filepath.ToSlash(rel)
}

// fileAnchorLine returns the line of f's package clause. It is the anchor for
// structural-absence diagnostics (a required method/struct is entirely missing,
// so there is no finer node to point at).
func fileAnchorLine(f *ast.File, fset *token.FileSet) int {
	return fset.Position(f.Package).Line
}

// ---------------------------------------------------------------------------
// RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
// ---------------------------------------------------------------------------

// allowedChannelCloseFuncs is the exhaustive whitelist of function names that
// are permitted to contain a direct ch.Close() call on an AMQPChannel.
// All other sites must call Connection.CloseEphemeralChannel instead.
var allowedChannelCloseFuncs = map[string]bool{
	// CloseEphemeralChannel is the canonical single-path API itself.
	"CloseEphemeralChannel": true,
	// waitAndClose contains a nil-conn guard (r.conn == nil branch) that calls
	// r.ch.Close() directly for unit tests that construct subscriptionRun without
	// a real Connection. The guard is unreachable in production (subscribeOnce
	// always passes s.conn). The archtest whitelist entry is intentional and
	// narrowly scoped to this one function.
	"waitAndClose": true,
}

// lookupInterfaceTypeFromPkg resolves a top-level interface declaration by name
// from a *types.Package and returns its types.Interface. Fail-closed when the
// type vanishes or is no longer an interface.
func lookupInterfaceTypeFromPkg(t *testing.T, pkg *types.Package, name string) *types.Interface {
	t.Helper()
	obj := pkg.Scope().Lookup(name)
	if obj == nil {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s not declared in %s", name, pkg.Path())
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s is not a type", name)
	}
	iface, ok := tn.Type().Underlying().(*types.Interface)
	if !ok {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s is not an interface", name)
	}
	return iface
}

// collectChannelDestructionDiags is the per-file body for
// CheckRMQChannelDestructionViaConn, converting t.Errorf calls into
// []Diagnostic. Extracted to keep CheckRMQChannelDestructionViaConn within
// gocognit ≤15.
func collectChannelDestructionDiags(p *Pass, chanIface *types.Interface, f *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		funcName := fd.Name.Name
		if allowedChannelCloseFuncs[funcName] {
			return
		}
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Close" {
				return
			}
			recvType := p.TypesInfo.TypeOf(sel.X)
			if recvType == nil {
				return
			}
			if !typesutil.ImplementsInterface(recvType, chanIface) {
				return
			}
			pos := p.Fset.Position(call.Pos())
			hint := receiverHint(sel.X)
			out = append(out, diagAt(rel, pos.Line, fmt.Sprintf(
				"RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s() contains direct %s.Close() call (receiver type %s implements AMQPChannel).\n"+
					"  All AMQPChannel destruction MUST go through Connection.CloseEphemeralChannel\n"+
					"  to keep inUseChannels in sync with MaxChannelsPerConn.\n"+
					"  Replace: %s.Close() → conn.CloseEphemeralChannel(%s)",
				funcName, hint, recvType.String(), hint, hint)))
		})
	})
	return out
}

// receiverHint reproduces the source-level receiver expression for use in
// error messages only. Decisions never depend on this string.
func receiverHint(x ast.Expr) string {
	switch e := x.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return receiverHint(e.X) + "." + e.Sel.Name
	}
	return "<expr>"
}

// CheckRMQChannelDestructionViaConn runs the RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
// production scan and returns all diagnostics.
//
// cfg is unused: adapters/rabbitmq has no build-tagged production files, so a
// single default-config scan is complete.
//
// register=no — gocell-internal-layout (scans adapters/rabbitmq), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via TestRMQChannelDestructionViaConn01).
func CheckRMQChannelDestructionViaConn(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false}, []string{"./adapters/rabbitmq/..."}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil {
			return nil
		}
		if p.Pkg.Path() != rmqAdapterPkgPath {
			return nil
		}
		chanIface := lookupInterfaceTypeFromPkg(t, p.Pkg, "AMQPChannel")
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			diags = append(diags, collectChannelDestructionDiags(p, chanIface, file, rel)...)
		}
		return nil
	})
	return diags
}

// ---------------------------------------------------------------------------
// RMQ-CHANNEL-MAX-PER-CONN-01
// ---------------------------------------------------------------------------

const expectedDefaultMaxChannelsPerConnConst = "defaultRMQMaxChannelsPerConn"

// collectChannelMaxPerConnDiags runs all three sub-checks (A/B/C) for
// RMQ-CHANNEL-MAX-PER-CONN-01 against connection.go. Extracted to keep
// CheckRMQChannelMaxPerConn within gocognit ≤15.
func collectChannelMaxPerConnDiags(root string) []Diagnostic {
	var out []Diagnostic
	src := filepath.Join(root, "adapters", "rabbitmq", "connection.go")
	rel := rmqRel(root, src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		return append(out, diagFile(rel,
			fmt.Sprintf("RMQ-CHANNEL-MAX-PER-CONN-01: parse %s: %v", rel, err)))
	}
	out = append(out, checkChannelMaxConfigField(f, fset, rel)...)
	out = append(out, checkChannelMaxSetDefaults(f, fset, rel)...)
	out = append(out, checkChannelMaxAcquireGuard(f, fset, rel)...)
	return out
}

// checkChannelMaxConfigField enforces RMQ-CHANNEL-MAX-PER-CONN-01-A:
// Config struct must declare MaxChannelsPerConn int.
func checkChannelMaxConfigField(f *ast.File, fset *token.FileSet, rel string) []Diagnostic {
	var hasField bool
	configLine := 0
	EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name.Name != "Config" {
			return
		}
		configLine = fset.Position(ts.Pos()).Line
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "MaxChannelsPerConn" {
					hasField = true
				}
			}
		}
	})
	if hasField {
		return nil
	}
	line := configLine
	if line == 0 {
		line = fileAnchorLine(f, fset)
	}
	return []Diagnostic{diagAt(rel, line,
		"RMQ-CHANNEL-MAX-PER-CONN-01-A: rabbitmq.Config must declare "+
			"`MaxChannelsPerConn int` so callers can bound channel allocation per "+
			"physical AMQP connection. Default 256 prevents broker channel_max "+
			"(default 2047) exhaustion.")}
}

// isMaxChannelsDefaultAssign returns true if assign assigns MaxChannelsPerConn
// from expectedDefaultMaxChannelsPerConnConst. Used by checkChannelMaxSetDefaults.
func isMaxChannelsDefaultAssign(assign *ast.AssignStmt) bool {
	if len(assign.Lhs) != 1 {
		return false
	}
	sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "MaxChannelsPerConn" {
		return false
	}
	if len(assign.Rhs) != 1 {
		return false
	}
	ident, ok := assign.Rhs[0].(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == expectedDefaultMaxChannelsPerConnConst
}

// isMaxChannelsLEQCondition returns true if ifStmt's condition is
// `<recv>.MaxChannelsPerConn <= 0`.
func isMaxChannelsLEQCondition(ifStmt *ast.IfStmt) bool {
	bin, ok := ifStmt.Cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.LEQ {
		return false
	}
	lhsSel, ok := bin.X.(*ast.SelectorExpr)
	if !ok || lhsSel.Sel.Name != "MaxChannelsPerConn" {
		return false
	}
	rhs, ok := bin.Y.(*ast.BasicLit)
	return ok && rhs.Kind == token.INT && rhs.Value == "0"
}

// checkChannelMaxSetDefaults enforces RMQ-CHANNEL-MAX-PER-CONN-01-B:
// setDefaults must guard with <= 0 and assign from the documented default
// constant.
func checkChannelMaxSetDefaults(f *ast.File, fset *token.FileSet, rel string) []Diagnostic {
	var setDefaults *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "setDefaults" && fd.Recv != nil {
			setDefaults = fd
		}
	})
	if setDefaults == nil {
		return []Diagnostic{diagAt(rel, fileAnchorLine(f, fset),
			"RMQ-CHANNEL-MAX-PER-CONN-01-B: Config.setDefaults not found in "+rel)}
	}
	var assigns, conditionIsLEQ bool
	EachInSubtree[ast.IfStmt](setDefaults.Body, func(ifStmt *ast.IfStmt) {
		if !isMaxChannelsLEQCondition(ifStmt) {
			return
		}
		conditionIsLEQ = true
		if !assigns {
			if _, ok := FindFirstInSubtree[ast.AssignStmt](ifStmt.Body, isMaxChannelsDefaultAssign); ok {
				assigns = true
			}
		}
	})
	return buildSetDefaultsDiags(assigns, conditionIsLEQ, rel, fset.Position(setDefaults.Pos()).Line)
}

// buildSetDefaultsDiags converts the boolean scan results of
// checkChannelMaxSetDefaults into []Diagnostic, anchored to the setDefaults
// declaration at rel:line. Extracted to keep checkChannelMaxSetDefaults within
// gocognit ≤15.
func buildSetDefaultsDiags(assigns, conditionIsLEQ bool, rel string, line int) []Diagnostic {
	var out []Diagnostic
	if !conditionIsLEQ {
		out = append(out, diagAt(rel, line,
			"RMQ-CHANNEL-MAX-PER-CONN-01-B: Config.setDefaults must guard the "+
				"MaxChannelsPerConn assignment with `<= 0` (not `== 0`). "+
				"A negative value passed by a caller must also fall back to the "+
				"default (256) — accepting only == 0 allows -1 to bypass the cap "+
				"and produce a production outage."))
	}
	if !assigns {
		out = append(out, diagAt(rel, line, fmt.Sprintf(
			"RMQ-CHANNEL-MAX-PER-CONN-01-B: Config.setDefaults must assign "+
				"MaxChannelsPerConn from the documented default constant `%s` (=256). "+
				"Hardcoded literals defeat the single-source default and drift from "+
				"the godoc on Config.MaxChannelsPerConn.",
			expectedDefaultMaxChannelsPerConnConst)))
	}
	return out
}

// checkChannelMaxAcquireGuard enforces RMQ-CHANNEL-MAX-PER-CONN-01-C:
// AcquireChannel must reference the inUseChannels counter.
func checkChannelMaxAcquireGuard(f *ast.File, fset *token.FileSet, rel string) []Diagnostic {
	var acquire *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "AcquireChannel" && fd.Recv != nil {
			acquire = fd
		}
	})
	if acquire == nil {
		return []Diagnostic{diagAt(rel, fileAnchorLine(f, fset),
			"RMQ-CHANNEL-MAX-PER-CONN-01-C: AcquireChannel method not found in "+rel)}
	}
	var refersToCounter bool
	EachInSubtree[ast.SelectorExpr](acquire.Body, func(sel *ast.SelectorExpr) {
		if sel.Sel.Name == "inUseChannels" {
			refersToCounter = true
		}
	})
	if refersToCounter {
		return nil
	}
	return []Diagnostic{diagAt(rel, fset.Position(acquire.Pos()).Line,
		"RMQ-CHANNEL-MAX-PER-CONN-01-C: AcquireChannel must reference the "+
			"`inUseChannels` atomic counter to bound new-channel creation against "+
			"Config.MaxChannelsPerConn; current source has no such reference. "+
			"Without the counter, pool-miss paths can silently exceed broker "+
			"channel_max and cause a connection-level shutdown.")}
}

// CheckRMQChannelMaxPerConn runs the RMQ-CHANNEL-MAX-PER-CONN-01 production
// scan (sub-rules A/B/C) and returns all diagnostics.
//
// cfg is unused: adapters/rabbitmq has no build-tagged production files, so a
// single default-config scan is complete.
//
// register=no — gocell-internal-layout (scans adapters/rabbitmq), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via TestRMQChannelMaxPerConn01_*).
func CheckRMQChannelMaxPerConn(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	return collectChannelMaxPerConnDiags(root)
}

// ---------------------------------------------------------------------------
// RMQ-PUBLISHER-FAILURE-HANDLING-01
// ---------------------------------------------------------------------------

// publishReturnViolation is one RMQ-PUBLISHER-FAILURE-HANDLING-01-D hit: an
// error-returning if-block missing a paired RecordPublishFailure call. Line is
// the 1-based source line of the offending return so the Diagnostic anchors to
// it (Detail no longer embeds the line — it lives in Diagnostic.Line).
type publishReturnViolation struct {
	Line   int
	Detail string
}

// scanPublishMissingFailureRecord walks every if-block and select-case in the
// Publish FuncDecl. For each block with a non-nil return that lacks a
// RecordPublishFailure call, it appends a located violation.
// Exemptions: ctx.Done() cases, nil returns, and the publisher-closed guard.
//
// Extracted to file-level so PR445-FU finding F3's RED sub-test can exercise
// the same checker against fixture files without duplicating the closure
// logic. The behavior is unchanged from the prior in-test closure form;
// Wave 4 extends checkPublishStmtViolations' switch to cover ForStmt,
// RangeStmt, SwitchStmt, TypeSwitchStmt and inlines SelectStmt's CommClause
// iteration to direct-child semantics.
func scanPublishMissingFailureRecord(publish *ast.FuncDecl, fset *token.FileSet) []publishReturnViolation {
	var violations []publishReturnViolation
	for _, stmt := range publish.Body.List {
		checkPublishStmtViolations(stmt, fset, false, &violations)
	}
	return violations
}

// checkPublishStmtViolations recursively walks stmt searching for if-blocks
// whose top-level Body.List contains an error return without a paired
// RecordPublishFailure call. The switch covers every Go statement container
// that may legally contain an *ast.IfStmt: BlockStmt and the explicit
// container forms (For, Range, Switch, TypeSwitch, Select). The block-list and
// case-clause recursion are factored into recursePublishStmtList /
// recursePublishCaseClauses so each arm stays ≤1 statement (no //nolint).
func checkPublishStmtViolations(stmt ast.Stmt, fset *token.FileSet, inCtxDone bool, violations *[]publishReturnViolation) {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		checkPublishIfBlockViolations(s, fset, inCtxDone, violations)
	case *ast.SelectStmt:
		if s.Body != nil {
			EachInChildren[ast.CommClause](s.Body, func(comm *ast.CommClause) {
				recursePublishStmtList(comm.Body, fset, inCtxDone || isCtxDoneCase(comm), violations)
			})
		}
	case *ast.BlockStmt:
		recursePublishStmtList(s.List, fset, inCtxDone, violations)
	case *ast.ForStmt:
		if s.Body != nil {
			recursePublishStmtList(s.Body.List, fset, inCtxDone, violations)
		}
	case *ast.RangeStmt:
		if s.Body != nil {
			recursePublishStmtList(s.Body.List, fset, inCtxDone, violations)
		}
	case *ast.SwitchStmt:
		recursePublishCaseClauses(s.Body, fset, inCtxDone, violations)
	case *ast.TypeSwitchStmt:
		recursePublishCaseClauses(s.Body, fset, inCtxDone, violations)
	}
}

// recursePublishStmtList recurses checkPublishStmtViolations over each statement
// in list. Shared by the BlockStmt / ForStmt / RangeStmt / SelectStmt arms.
func recursePublishStmtList(list []ast.Stmt, fset *token.FileSet, inCtxDone bool, violations *[]publishReturnViolation) {
	for _, inner := range list {
		checkPublishStmtViolations(inner, fset, inCtxDone, violations)
	}
}

// recursePublishCaseClauses recurses over every CaseClause body in a switch /
// type-switch block. Nil body is a no-op (matches the original `if s.Body != nil`
// guard). Shared by the SwitchStmt / TypeSwitchStmt arms.
func recursePublishCaseClauses(body *ast.BlockStmt, fset *token.FileSet, inCtxDone bool, violations *[]publishReturnViolation) {
	if body == nil {
		return
	}
	EachInChildren[ast.CaseClause](body, func(cc *ast.CaseClause) {
		recursePublishStmtList(cc.Body, fset, inCtxDone, violations)
	})
}

// checkPublishIfBlockViolations is the per-if-block worker for the
// RMQ-PUBLISHER-FAILURE-HANDLING-01-D scan. See scanPublishMissingFailureRecord.
func checkPublishIfBlockViolations(ifStmt *ast.IfStmt, fset *token.FileSet, inCtxDone bool, violations *[]publishReturnViolation) {
	if ifStmt == nil || ifStmt.Body == nil {
		return
	}
	body := ifStmt.Body.List

	// Does THIS if-block (top-level Body.List only) contain a non-nil
	// return? Nested returns inside an inner for/if/select are this
	// block's child statements' concern — checkStmt will recurse and
	// reach them via the inner if/select being its own checkIfBlock /
	// SelectStmt handler. Counting them here would double-attribute the
	// violation to two ancestor blocks. FindFirstChild visits only direct
	// children of ifStmt.Body (depth-1).
	_, hasNonNilReturn := FindFirstChild[ast.ReturnStmt](ifStmt.Body, func(ret *ast.ReturnStmt) bool {
		return !isNilReturn(ret)
	})

	// Exempt: if-block guarding the "publisher is closed" early exit.
	// This is not a wire-level failure, so no metric is required.
	// Detected by checking if the condition references a field/method named "closed".
	isClosedGuard := ifCondRefersTo(ifStmt.Cond, "closed")

	if hasNonNilReturn && !inCtxDone && !isClosedGuard {
		// Does this block contain a RecordPublishFailure call?
		hasRecord := blockContainsRecordPublishFailure(body)
		if !hasRecord {
			// Report the first top-level non-nil return.
			if ret, found := FindFirstChild[ast.ReturnStmt](ifStmt.Body, func(ret *ast.ReturnStmt) bool {
				return !isNilReturn(ret)
			}); found {
				*violations = append(*violations, publishReturnViolation{
					Line:   fset.Position(ret.Pos()).Line,
					Detail: "if-block with error return has no RecordPublishFailure",
				})
			}
		}
	}

	// Recurse into nested if/select within this block.
	for _, inner := range body {
		checkPublishStmtViolations(inner, fset, inCtxDone, violations)
	}

	// Check else branch.
	if ifStmt.Else != nil {
		checkPublishStmtViolations(ifStmt.Else, fset, inCtxDone, violations)
	}
}

// isCtxDoneCase returns true if the CommClause is a `case <-ctx.Done():` or
// `case v := <-ctx.Done():` arm (both ExprStmt and AssignStmt forms).
func isCtxDoneCase(cc *ast.CommClause) bool {
	if cc.Comm == nil {
		return false
	}
	var unary *ast.UnaryExpr
	switch comm := cc.Comm.(type) {
	case *ast.ExprStmt:
		// case <-ctx.Done():
		u, ok := comm.X.(*ast.UnaryExpr)
		if !ok || u.Op != token.ARROW {
			return false
		}
		unary = u
	case *ast.AssignStmt:
		// case v := <-ctx.Done():
		if len(comm.Rhs) != 1 {
			return false
		}
		u, ok := comm.Rhs[0].(*ast.UnaryExpr)
		if !ok || u.Op != token.ARROW {
			return false
		}
		unary = u
	default:
		return false
	}
	call, ok := unary.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "ctx" && sel.Sel.Name == "Done"
}

// isNilReturn returns true if the ReturnStmt returns a single nil literal.
func isNilReturn(ret *ast.ReturnStmt) bool {
	if len(ret.Results) != 1 {
		return false
	}
	ident, ok := ret.Results[0].(*ast.Ident)
	return ok && ident.Name == "nil"
}

// ifCondRefersTo returns true if the condition expression contains an identifier
// or selector with the given name. Used to detect exempted guard patterns like
// `if p.closed.Load()` without full type resolution.
func ifCondRefersTo(cond ast.Expr, name string) bool {
	var found bool
	EachInSubtree[ast.Ident](cond, func(id *ast.Ident) {
		if id.Name == name {
			found = true
		}
	})
	return found
}

// blockContainsRecordPublishFailure returns true if any statement in stmts
// (at any nesting level) is a call to RecordPublishFailure.
func blockContainsRecordPublishFailure(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		var found bool
		EachInSubtree[ast.CallExpr](s, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "RecordPublishFailure" {
				found = true
			}
		})
		if found {
			return true
		}
	}
	return false
}

// findMethod returns the FuncDecl for a method whose name matches `name` and
// which has a non-nil receiver. Returns nil if not found.
func findMethod(f *ast.File, name string) *ast.FuncDecl {
	var result *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if result == nil && fd.Name.Name == name && fd.Recv != nil {
			result = fd
		}
	})
	return result
}

// collectPublisherFailureDiags gathers all sub-rule (A/B/C/D) diagnostics for
// RMQ-PUBLISHER-FAILURE-HANDLING-01 against publisher.go. Extracted to keep
// CheckRMQPublisherFailureHandling within gocognit ≤15.
func collectPublisherFailureDiags(root string) []Diagnostic {
	var out []Diagnostic
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")
	rel := rmqRel(root, src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		return append(out, diagFile(rel,
			fmt.Sprintf("RMQ-PUBLISHER-FAILURE-HANDLING-01: parse %s: %v", rel, err)))
	}
	publish := findMethod(f, "Publish")
	if publish == nil {
		return append(out, diagAt(rel, fileAnchorLine(f, fset),
			"RMQ-PUBLISHER-FAILURE-HANDLING-01: Publish method not found in "+rel))
	}
	publishLine := fset.Position(publish.Pos()).Line
	out = append(out, checkPublisherNackErrcode(publish, rel, publishLine)...)
	out = append(out, checkPublisherWarnCount(publish, rel, publishLine)...)
	out = append(out, checkPublisherRecordsFailureMetric(publish, rel, publishLine)...)
	out = append(out, checkPublisherAllReturnsMustRecord(publish, fset, rel)...)
	return out
}

// checkPublisherNackErrcode enforces RMQ-PUBLISHER-FAILURE-HANDLING-01-A.
func checkPublisherNackErrcode(publish *ast.FuncDecl, rel string, line int) []Diagnostic {
	var found bool
	EachInSubtree[ast.Ident](publish.Body, func(ident *ast.Ident) {
		if ident.Name == "ErrAdapterAMQPNack" {
			found = true
		}
	})
	if found {
		return nil
	}
	return []Diagnostic{diagAt(rel, line,
		"RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish must reference "+
			"ErrAdapterAMQPNack to mark broker-NACK as a distinct error code (vs "+
			"ErrAdapterAMQPConfirmTimeout). Sharing a code makes alerting rules "+
			"unable to tell broker rejection from network timeout.")}
}

// checkPublisherWarnCount enforces RMQ-PUBLISHER-FAILURE-HANDLING-01-B.
func checkPublisherWarnCount(publish *ast.FuncDecl, rel string, line int) []Diagnostic {
	const requiredWarnCalls = 3
	var warnCount int
	EachInSubtree[ast.CallExpr](publish.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Warn" {
			return
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "slog" {
			return
		}
		warnCount++
	})
	if warnCount >= requiredWarnCalls {
		return nil
	}
	return []Diagnostic{diagAt(rel, line, fmt.Sprintf(
		"RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish must call slog.Warn at "+
			"least %d times (NACK / confirm timeout / confirm-channel-closed); "+
			"found %d. Silent failure branches make on-call diagnosis impossible.",
		requiredWarnCalls, warnCount))}
}

// checkPublisherRecordsFailureMetric enforces RMQ-PUBLISHER-FAILURE-HANDLING-01-C.
func checkPublisherRecordsFailureMetric(publish *ast.FuncDecl, rel string, line int) []Diagnostic {
	var calls int
	EachInSubtree[ast.CallExpr](publish.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name == "RecordPublishFailure" {
			calls++
		}
	})
	if calls >= 1 {
		return nil
	}
	return []Diagnostic{diagAt(rel, line,
		"RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish must call "+
			"RecordPublishFailure on the injected PublisherCollector so the failure "+
			"reason is queryable as a metric. Defaulting to NoopPublisherCollector "+
			"keeps the call cheap; production wiring injects the provider-backed "+
			"collector at the composition root.")}
}

// checkPublisherAllReturnsMustRecord enforces RMQ-PUBLISHER-FAILURE-HANDLING-01-D.
func checkPublisherAllReturnsMustRecord(publish *ast.FuncDecl, fset *token.FileSet, rel string) []Diagnostic {
	violations := scanPublishMissingFailureRecord(publish, fset)
	var out []Diagnostic
	for _, v := range violations {
		out = append(out, diagAt(rel, v.Line, fmt.Sprintf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-D: Publish %s. "+
				"All error-returning if-blocks must contain collector.RecordPublishFailure "+
				"so alerting rules can observe the failure reason without log-parsing. "+
				"Exemptions: success `return nil` and returns inside ctx.Done() case.",
			v.Detail)))
	}
	return out
}

// CheckRMQPublisherFailureHandling runs the RMQ-PUBLISHER-FAILURE-HANDLING-01
// production scan (sub-rules A/B/C/D) and returns all diagnostics.
//
// cfg is unused: adapters/rabbitmq has no build-tagged production files, so a
// single default-config scan is complete.
//
// register=no — gocell-internal-layout (scans adapters/rabbitmq), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via TestRMQPublisherFailureHandling01_*).
func CheckRMQPublisherFailureHandling(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	return collectPublisherFailureDiags(root)
}

// ---------------------------------------------------------------------------
// RMQ-PUBLISHER-RELEASES-CHANNEL-01
// ---------------------------------------------------------------------------

// collectPublisherReleasesChannelDiags gathers diagnostics for
// RMQ-PUBLISHER-RELEASES-CHANNEL-01. Extracted to keep
// CheckRMQPublisherReleasesChannel within gocognit ≤15.
func collectPublisherReleasesChannelDiags(root string) []Diagnostic {
	var out []Diagnostic
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")
	rel := rmqRel(root, src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		return append(out, diagFile(rel,
			fmt.Sprintf("RMQ-PUBLISHER-RELEASES-CHANNEL-01: parse %s: %v", rel, err)))
	}

	var publishMethod *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if publishMethod == nil && fd.Recv != nil && fd.Name.Name == "Publish" {
			publishMethod = fd
		}
	})
	if publishMethod == nil {
		return append(out, diagAt(rel, fileAnchorLine(f, fset),
			"RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish method not found in "+rel))
	}
	publishLine := fset.Position(publishMethod.Pos()).Line

	var hasAcquire bool
	EachInSubtree[ast.CallExpr](publishMethod.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name == "AcquireChannel" {
			hasAcquire = true
		}
	})
	if !hasAcquire {
		out = append(out, diagAt(rel, publishLine,
			"RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish must call "+
				"conn.AcquireChannel to obtain a channel for confirm-mode publish."))
	}

	releaseSelectors := map[string]bool{
		"CloseEphemeralChannel": true,
		"ReleaseChannel":        true,
	}
	_, hasRelease := FindFirstInSubtree[ast.DeferStmt](publishMethod.Body, func(ds *ast.DeferStmt) bool {
		_, ok := FindFirstInSubtree[ast.SelectorExpr](ds, func(sel *ast.SelectorExpr) bool {
			return releaseSelectors[sel.Sel.Name]
		})
		return ok
	})
	if !hasRelease {
		out = append(out, diagAt(rel, publishLine,
			"RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish must pair "+
				"AcquireChannel with a deferred p.conn.CloseEphemeralChannel "+
				"(or p.conn.ReleaseChannel) call. Without this pairing every Publish "+
				"leaks one inUseChannels slot; after MaxChannelsPerConn (=256) "+
				"publishes all subsequent calls fail with "+
				"ErrAdapterAMQPChannelMaxExceeded."))
	}
	return out
}

// CheckRMQPublisherReleasesChannel runs the RMQ-PUBLISHER-RELEASES-CHANNEL-01
// production scan and returns all diagnostics.
//
// cfg is unused: adapters/rabbitmq has no build-tagged production files, so a
// single default-config scan is complete.
//
// register=no — gocell-internal-layout (scans adapters/rabbitmq), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via TestRMQPublisherReleasesChannel01).
func CheckRMQPublisherReleasesChannel(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	return collectPublisherReleasesChannelDiags(root)
}

// ---------------------------------------------------------------------------
// RMQ-STOPINTAKE-INFLIGHT-WAIT-01
// ---------------------------------------------------------------------------

// collectStopIntakeDiags gathers all sub-rule diagnostics for
// RMQ-STOPINTAKE-INFLIGHT-WAIT-01. Extracted to keep
// CheckRMQStopIntakeInflightWait within gocognit ≤15.
func collectStopIntakeDiags(root string) []Diagnostic {
	var out []Diagnostic
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")
	rel := rmqRel(root, src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		return append(out, diagFile(rel,
			fmt.Sprintf("RMQ-STOPINTAKE-INFLIGHT-WAIT-01: parse %s: %v", rel, err)))
	}
	out = append(out, checkStopIntakeWaitsForInflight(f, fset, rel)...)
	out = append(out, checkDrainNoParentCtxDone(f, fset, rel)...)
	out = append(out, checkDrainUsesDetachedContext(f, fset, rel)...)
	out = append(out, checkStopIntakeAvoidsLocalWgWait(f, fset, rel)...)
	return out
}

// isInflightWaitCall returns true if call is any recognized inflight-wait
// sentinel: waitInflightDrain, <recv>.localWg.Wait, <recv>.inflightWg.Wait,
// or the helper method names waitInflight/waitDrained/wgDone/inflightCount.
func isInflightWaitCall(call *ast.CallExpr) bool {
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name == "waitInflightDrain"
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Wait":
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		return inner.Sel.Name == "localWg" || inner.Sel.Name == "inflightWg"
	case "waitInflight", "waitDrained", "wgDone", "inflightCount":
		return true
	}
	return false
}

// checkStopIntakeWaitsForInflight enforces RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A.
func checkStopIntakeWaitsForInflight(f *ast.File, fset *token.FileSet, rel string) []Diagnostic {
	var stopIntake *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
		}
	})
	if stopIntake == nil {
		return []Diagnostic{diagAt(rel, fileAnchorLine(f, fset),
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A: StopIntake method not found in "+rel)}
	}
	var found bool
	EachInSubtree[ast.CallExpr](stopIntake.Body, func(call *ast.CallExpr) {
		if isInflightWaitCall(call) {
			found = true
		}
	})
	if found {
		return nil
	}
	return []Diagnostic{diagAt(rel, fset.Position(stopIntake.Pos()).Line,
		"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A: StopIntake must wait for in-flight "+
			"processDelivery goroutines (run.localWg.Wait / run.waitInflight / run.wgDone) "+
			"before returning, otherwise Close() can race with active broker I/O.")}
}

// isCtxDoneReceiveCase returns true if comm is a `case <-ctx.Done():` arm
// (ExprStmt form only — assignment form is handled separately by isCtxDoneCase).
func isCtxDoneReceiveCase(comm *ast.CommClause) bool {
	expr, ok := comm.Comm.(*ast.ExprStmt)
	if !ok {
		return false
	}
	unary, ok := expr.X.(*ast.UnaryExpr)
	if !ok || unary.Op != token.ARROW {
		return false
	}
	call, ok := unary.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "ctx" && sel.Sel.Name == "Done"
}

// checkDrainNoParentCtxDone enforces RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B
// (no parent ctx.Done).
func checkDrainNoParentCtxDone(f *ast.File, fset *token.FileSet, rel string) []Diagnostic {
	var drain *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "drainRemaining" && fd.Recv != nil {
			drain = fd
		}
	})
	if drain == nil {
		return []Diagnostic{diagAt(rel, fileAnchorLine(f, fset),
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining method not found in "+rel)}
	}
	var out []Diagnostic
	EachInSubtree[ast.CommClause](drain.Body, func(comm *ast.CommClause) {
		if !isCtxDoneReceiveCase(comm) {
			return
		}
		out = append(out, diagAt(rel, fset.Position(comm.Pos()).Line,
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining contains `case <-ctx.Done()`; "+
				"drain MUST run on a detached context (context.WithoutCancel) bounded by "+
				"currentDrainDeadline timer, otherwise parent ctx cancel drops prefetched messages."))
	})
	return out
}

// checkDrainUsesDetachedContext enforces RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B
// (detached context cross-check).
// bodyHasWithoutCancel reports whether body references context.WithoutCancel.
// Extracted to keep checkDrainUsesDetachedContext within gocognit ≤15.
func bodyHasWithoutCancel(body *ast.BlockStmt) bool {
	var found bool
	EachInSubtree[ast.SelectorExpr](body, func(sel *ast.SelectorExpr) {
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if ident.Name == "context" && sel.Sel.Name == "WithoutCancel" {
			found = true
		}
	})
	return found
}

func checkDrainUsesDetachedContext(f *ast.File, fset *token.FileSet, rel string) []Diagnostic {
	var drainLine int
	_, found := FindFirstInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) bool {
		if fd.Recv == nil || fd.Body == nil {
			return false
		}
		if fd.Name.Name != "drainRemaining" && fd.Name.Name != "consumeLoop" {
			return false
		}
		if fd.Name.Name == "drainRemaining" && drainLine == 0 {
			drainLine = fset.Position(fd.Pos()).Line
		}
		return bodyHasWithoutCancel(fd.Body)
	})
	if found {
		return nil
	}
	line := drainLine
	if line == 0 {
		line = fileAnchorLine(f, fset)
	}
	return []Diagnostic{diagAt(rel, line,
		"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining or consumeLoop must "+
			"use `context.WithoutCancel` to derive the drain ctx, so prefetched "+
			"deliveries are processed independently of the parent ctx cancel.")}
}

// checkStopIntakeAvoidsLocalWgWait enforces the negative invariant for
// RMQ-STOPINTAKE-INFLIGHT-WAIT-01: StopIntake must NOT call localWg.Wait().
func checkStopIntakeAvoidsLocalWgWait(f *ast.File, fset *token.FileSet, rel string) []Diagnostic {
	var stopIntake *ast.FuncDecl
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
		}
	})
	if stopIntake == nil {
		return nil // already caught by checkStopIntakeWaitsForInflight
	}
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](stopIntake.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "localWg" {
			return
		}
		out = append(out, diagAt(rel, fset.Position(call.Pos()).Line,
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01: StopIntake body must not call "+
				"localWg.Wait(); poll inflightCount() instead. drainRemaining "+
				"concurrently calls localWg.Add(1) on every prefetched delivery, "+
				"and Wait racing that Add panics with "+
				"\"WaitGroup misuse: Add called concurrently with Wait\"."))
	})
	return out
}

// CheckRMQStopIntakeInflightWait runs the RMQ-STOPINTAKE-INFLIGHT-WAIT-01
// production scan and returns all diagnostics.
//
// cfg is unused: adapters/rabbitmq has no build-tagged production files, so a
// single default-config scan is complete.
//
// register=no — gocell-internal-layout (scans adapters/rabbitmq), NOT in
// StandardCellRules() and NOT promised to run externally — these dogfood-only
// rules target a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via TestRMQStopIntakeInflightWait01_*).
func CheckRMQStopIntakeInflightWait(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	return collectStopIntakeDiags(root)
}
