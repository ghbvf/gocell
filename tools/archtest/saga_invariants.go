package archtest

// saga_invariants.go — importable saga-theme rule logic (#1634 M3-PR4).
//
// This is the non-test companion to saga_invariants_test.go. It holds the
// detector logic for the saga-theme archtest rules so that rule functions can
// be compiled and called by an external Cell repository. Go never compiles a
// dependency's _test.go, so any rule an external repo must run cannot live in
// a _test.go.
//
// Two distinct layers — importable surface vs registered subset — do NOT
// coincide; do not conflate them:
//
// Importable Check* functions (all rules that do not constrain GoCell-internal
// source layout):
//
//	CheckSagaStepCompensatePure — SAGA-STEP-COMPENSATE-PURE-01
//	CheckSagaCoordinatorNoHeartbeatLoop — SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01
//	CheckSagaExecutorRandInjected — SAGA-EXECUTOR-RAND-INJECTED-01
//	CheckSagaJournalConformanceEnrollment — SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01
//	CheckSagaGlobalReaderConformanceEnrollment — SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01
//	CheckSagaJournalHolderSeal — SAGA-JOURNAL-HOLDER-SEAL-01
//	CheckSagaDriveBehindLeaderGate — SAGA-DRIVE-BEHIND-LEADER-GATE-01
//	CheckSagaStepRunOutsideTx — SAGA-STEP-RUN-OUTSIDE-TX-01
//	CheckSagaConstructorNilGuard — SAGA-CONSTRUCTOR-NIL-GUARD-01
//	CheckSagaMetricLabelValuesFrozen — SAGA-METRIC-LABEL-VALUES-FROZEN-01
//	CheckSagaSlogInstanceFieldsCaller — SAGA-SLOG-INSTANCE-FIELDS-CALLER-01
//
// Registered in StandardCellRules (portable subset — rules that apply to any
// Cell using the saga engine):
//
//	SAGA-STEP-COMPENSATE-PURE-01 via CheckSagaStepCompensatePure.
//
// The other ten Check* are importable but intentionally NOT registered because
// they constrain GoCell's OWN runtime/saga internals (Coordinator, executor,
// sagalog, journal, etc.) — an external Cell module that merely uses the saga
// engine would have zero runtime/saga/ production files and would see vacuous-
// green or false-red results for those rules.
//
// Two rules remain in saga_invariants_test.go (no importable Check*):
//
//   - SAGA-STATUS-FANOUT-COVERAGE-01: relies on sagacoveragegen + golden files
//     in the GoCell source tree — meaningless outside the monorepo.
//   - SAGA-INVARIANTS-FILE-CONSOLIDATED-01: a meta-rule about where saga INVARIANT
//     comments live in tools/archtest/ — GoCell-specific directory structure.
//
// Platform-symbol paths are anchored to [PlatformModulePath]. See external.go.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

// ─── Platform-path consts (all derived from PlatformModulePath) ─────────────

const (
	// sagaPkgPath is the kernel/saga package path. Merges the two formerly
	// separate consts sagaPkgPath (line 154) and ksagaPkgPath (line 3974).
	sagaPkgPath = PlatformModulePath + "/kernel/saga"

	// sagaJournalPkg is the kernel/saga/journal package path. Merges
	// sagaJournalIfacePkg (line 1443) and journalInterfacePkgPath (line 2336).
	sagaJournalPkg = PlatformModulePath + "/kernel/saga/journal"

	// sagaJournalIfacePkg is a backward-compat alias for sagaJournalPkg,
	// retained so test-side code that still uses the old name compiles.
	sagaJournalIfacePkg = sagaJournalPkg

	// sagaConformancePkg is the sagajournaltest package path.
	sagaConformancePkg = PlatformModulePath + "/kernel/saga/sagajournaltest"

	// sagalogPkgPath is the runtime/saga/internal/sagalog package path.
	sagalogPkgPath = PlatformModulePath + "/runtime/saga/internal/sagalog"

	// sagaRuntimePkg is the runtime/saga package path.
	sagaRuntimePkg = PlatformModulePath + "/runtime/saga"

	// sagaRuntimeExecutorPkg is the runtime/saga/executor package path.
	// Note: sagaExecutorPkg (already used for SAGA-METRIC-LABEL-VALUES-FROZEN-01)
	// and sagaRuntimeExecutorPkg are the same path; they are unified here.
	sagaRuntimeExecutorPkg = PlatformModulePath + "/runtime/saga/executor"

	// sagaOutboxPkg is the kernel/outbox package path.
	sagaOutboxPkg = PlatformModulePath + "/kernel/outbox"

	// sagaPersistencePkg is the kernel/persistence package path.
	sagaPersistencePkg = PlatformModulePath + "/kernel/persistence"

	// sagaValidationPkg is the pkg/validation package path.
	sagaValidationPkg = PlatformModulePath + "/pkg/validation"

	// sagaClockPkg is the kernel/clock package path.
	sagaClockPkg = PlatformModulePath + "/kernel/clock"
)

// ─── Rule IDs and string constants (moved from saga_invariants_test.go) ──────

const (
	// SAGA-STEP-COMPENSATE-PURE-01.
	sagaCompensatePureRuleID = "SAGA-STEP-COMPENSATE-PURE-01"
	sagaCompensateFuncName   = "CompensateFunc"
	sagaCompensateFieldName  = "Compensate"
	sagaFixturesDir          = "saga_compensate_pure_fixtures"
	sagaMaxCompensateStmts   = 10

	// SAGA-CONSTRUCTOR-NIL-GUARD-01.
	sagaConstructorNilGuardRuleID = "SAGA-CONSTRUCTOR-NIL-GUARD-01"
	sagaConstructorFixturesDir    = "saga_constructor_nilguard_fixtures"

	// SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01.
	sagaCoordinatorNoHeartbeatLoopRule = "SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01"
	heartbeatMethodName                = "Heartbeat"
	heartbeaterIdutilPkgName           = "idutil"
	heartbeaterSafeIDType              = "SafeID"

	// SAGA-EXECUTOR-RAND-INJECTED-01.
	sagaRandInjectedRuleID = "SAGA-EXECUTOR-RAND-INJECTED-01"
	executorPkgPrefix      = "runtime/saga/executor/"
	randV1PkgPath          = "math/rand"
	randV2PkgPath          = "math/rand/v2"

	// SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01.
	sagaJournalIfaceName    = "Journal"
	sagaConformanceFuncName = "RunConformanceSuite"

	// SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01.
	sagaGlobalReaderIfaceName       = "GlobalReader"
	sagaGlobalReaderConformanceFunc = "RunGlobalReaderConformance"

	// SAGA-JOURNAL-HOLDER-SEAL-01.
	journalInterfaceTypeName     = "Journal"
	heartbeaterInterfaceTypeName = "Heartbeater"
	journalCoreInterfaceTypeName = "JournalCore"
	allowedSagaJournalHolder     = "Coordinator"
	sagaJournalHolderSealRule    = "SAGA-JOURNAL-HOLDER-SEAL-01"

	// SAGA-DRIVE-BEHIND-LEADER-GATE-01.
	sagaLeaderGateRule        = "SAGA-DRIVE-BEHIND-LEADER-GATE-01"
	sagaLeaderGateFixturesDir = "saga_leader_gate_fixtures"
	driveOneMethodName        = "driveOne"
	acquireLeadMethodName     = "acquireLead"
	tickOnceFuncName          = "tickOnce"

	// SAGA-STEP-RUN-OUTSIDE-TX-01.
	sagaStepRunOutsideTxRule = "SAGA-STEP-RUN-OUTSIDE-TX-01"
	sagaStepRunFixturesDir   = "saga_step_run_outside_tx_fixtures"
	safeRunFuncName          = "safeRun"
	runInTxMethodName        = "RunInTx"
	sagaRuntimePkgPrefix     = "runtime/saga/"
	stepFuncTypeName         = "StepFunc"

	// SAGA-SLOG-INSTANCE-FIELDS-CALLER-01.
	sagaSlogInstanceFieldsRule               = "SAGA-SLOG-INSTANCE-FIELDS-CALLER-01"
	sagaInstanceFieldsFuncName               = "InstanceFields"
	sagaSlogPkgPath                          = "log/slog"
	sagaSlogAttrTypeName                     = "Attr"
	violSagaSlogInstanceFieldsOutsideCarrier = sagaSlogInstanceFieldsRule + ": " +
		`a log/slog Attr constructor with key "instance_id"|"lease_id" outside ` +
		"sagalog.InstanceFields — route every per-instance saga log through " +
		"sagalog.InstanceFields so lease_id is structurally guaranteed (#1266)"

	// sagaExecutorPkg aliases sagaRuntimeExecutorPkg for metric-label helpers.
	sagaExecutorPkg = sagaRuntimeExecutorPkg
)

// randAllowedConstructors is the set of (pkgPath.funcName) keys that are
// permitted package-level rand calls (constructors, not global state reads).
var randAllowedConstructors = map[string]bool{
	randV2PkgPath + ".New":        true,
	randV2PkgPath + ".NewPCG":     true,
	randV2PkgPath + ".NewChaCha8": true,
	randV1PkgPath + ".New":        true,
	randV1PkgPath + ".NewSource":  true,
}

// sagaInstanceFieldsGuardedKeys is the set of slog key strings that must be
// built via sagalog.InstanceFields (not raw slog.String / slog.Attr literals).
var sagaInstanceFieldsGuardedKeys = map[string]struct{}{
	"instance_id": {},
	"lease_id":    {},
}

// sagaLabelEnumWant is the golden value-set for each frozen executor label
// enum type (SAGA-METRIC-LABEL-VALUES-FROZEN-01 A1).
var sagaLabelEnumWant = map[string][]string{
	"HeartbeatFailureReason": {"infra_error", "stale_lease"},
	"TickResult":             {"claimed", "empty", "error"},
	"DriveResult":            {"ok", "error"},
	"LeaderSkipReason":       {"contended", "ctx_canceled", "backend_error"},
}

// sagaBannedReceiverKeys maps "<pkgpath>.<TypeName>" → display label for
// concrete banned types (matched after one pointer deref for *sql.Tx).
// Moved from saga_invariants_test.go with bare literals replaced.
var sagaBannedReceiverKeys = map[string]string{
	"database/sql.Tx":            "*sql.Tx",
	"github.com/jackc/pgx/v5.Tx": "pgx.Tx",
	sagaOutboxPkg + ".Writer":    "outbox.Writer",
	sagaOutboxPkg + ".Emitter":   "outbox.Emitter",
}

// sagaBannedIfaceSpecs lists the (pkgPath, typeName, label) tuples for
// interface types that CompensateFunc bodies must not call methods on.
// Moved from saga_invariants_test.go with bare literals replaced.
var sagaBannedIfaceSpecs = []struct{ path, name, label string }{
	{"github.com/jackc/pgx/v5", "Tx", "pgx.Tx"},
	{sagaOutboxPkg, "Writer", "outbox.Writer"},
	{sagaOutboxPkg, "Emitter", "outbox.Emitter"},
	{sagaPersistencePkg, "TxRunner", "persistence.TxRunner"},
}

// sagaConstructorScopePackages is the set of packages scanned for
// SAGA-CONSTRUCTOR-NIL-GUARD-01.
var sagaConstructorScopePackages = map[string]bool{
	sagaRuntimePkg:         true,
	sagaRuntimeExecutorPkg: true,
}

// sagaGuardKey is a (pkgPath, funcName) pair for a sanctioned nil-guard call.
type sagaGuardKey struct{ pkg, name string }

// sagaGuardFuncKeys is the set of (pkgPath, funcName) pairs that constitute an
// accepted nil-guard call for a constructor parameter.
var sagaGuardFuncKeys = []sagaGuardKey{
	{sagaValidationPkg, "IsNilInterface"},
	{sagaClockPkg, "MustHaveClock"},
}

// ─── SAGA-STEP-COMPENSATE-PURE-01 helpers ────────────────────────────────────

// sagaResolveBannedReceivers resolves the banned interface receiver types from
// pkg's transitive import closure, once per Pass.
func sagaResolveBannedReceivers(pkg *types.Package) []bannedReceiver {
	var out []bannedReceiver
	for _, b := range sagaBannedIfaceSpecs {
		if iface := lookupInterface(pkg, b.path, b.name); iface != nil {
			out = append(out, bannedReceiver{label: b.label, iface: iface})
		}
	}
	return out
}

// sagaBannedReceiverLabel reports the display label when sel.X's static type
// is a banned receiver. It checks:
//  1. Exact concrete match (deref one pointer level for *sql.Tx).
//  2. Implements a banned interface (types.Implements).
func sagaBannedReceiverLabel(info *types.Info, sel *ast.SelectorExpr, ifaces []bannedReceiver) (string, bool) {
	t := info.TypeOf(sel.X)
	if t == nil {
		return "", false
	}
	nt := t
	if ptr, isPtr := nt.(*types.Pointer); isPtr {
		nt = ptr.Elem()
	}
	if named, isNamed := nt.(*types.Named); isNamed && named.Obj() != nil && named.Obj().Pkg() != nil {
		key := named.Obj().Pkg().Path() + "." + named.Obj().Name()
		if label, ok := sagaBannedReceiverKeys[key]; ok {
			return label, true
		}
	}
	for _, b := range ifaces {
		if implementsBannedIface(t, b.iface) {
			return b.label, true
		}
	}
	return "", false
}

// isCompensateFuncType reports whether typ is exactly kernel/saga.CompensateFunc.
func isCompensateFuncType(typ types.Type) bool {
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == sagaPkgPath &&
		obj.Name() == sagaCompensateFuncName
}

// compensateFuncAssignment holds a scanned assignment: either a func literal
// body (for direct scan) or an ident reference to a named function (for
// declared-func scan). Exactly one of Lit / NamedIdent is non-nil.
type compensateFuncAssignment struct {
	Lit        *ast.FuncLit
	NamedIdent *ast.Ident
}

// collectCompensateAssignments walks the AST of file and returns every
// assignment to a kernel/saga.CompensateFunc-typed slot, covering:
//
//  1. var c saga.CompensateFunc = <expr>    (ValueSpec)
//  2. c = <expr> where c is CompensateFunc (AssignStmt)
//  3. saga.Step{Compensate: <expr>}         (CompositeLit KV)
//
// <expr> is either a FuncLit (returned in Lit) or an Ident referencing a
// named function (returned in NamedIdent).
func collectCompensateAssignments(p *Pass, file *ast.File) []compensateFuncAssignment { //nolint:gocognit,lll // archtest AST scanner: three assignment forms (ValueSpec/AssignStmt/CompositeLit) each need distinct handling
	info := p.TypesInfo
	var out []compensateFuncAssignment

	// var c saga.CompensateFunc = <expr>
	EachInSubtree[ast.ValueSpec](file, func(node *ast.ValueSpec) {
		typ := info.TypeOf(node.Type)
		if typ == nil || !isCompensateFuncType(typ) {
			return
		}
		for _, val := range node.Values {
			out = append(out, classifyExpr(val))
		}
	})

	// c = <expr> where c is CompensateFunc
	EachInSubtree[ast.AssignStmt](file, func(node *ast.AssignStmt) {
		for i, lhs := range node.Lhs {
			if i >= len(node.Rhs) {
				break
			}
			lhsType := info.TypeOf(lhs)
			if lhsType == nil || !isCompensateFuncType(lhsType) {
				continue
			}
			out = append(out, classifyExpr(node.Rhs[i]))
		}
	})

	// saga.Step{Compensate: <expr>}
	EachInSubtree[ast.CompositeLit](file, func(node *ast.CompositeLit) {
		EachInChildren[ast.KeyValueExpr](node, func(kv *ast.KeyValueExpr) {
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != sagaCompensateFieldName {
				return
			}
			if !compensateKVFieldIsCompensateFunc(info, node) {
				return
			}
			out = append(out, classifyExpr(kv.Value))
		})
	})

	return out
}

// compensateKVFieldIsCompensateFunc verifies that the struct type of the
// CompositeLit node has a field named sagaCompensateFieldName whose declared
// type is kernel/saga.CompensateFunc.
func compensateKVFieldIsCompensateFunc(info *types.Info, node *ast.CompositeLit) bool {
	structType := info.TypeOf(node)
	if structType == nil {
		return false
	}
	st := structType
	if ptr, ok := st.(*types.Pointer); ok {
		st = ptr.Elem()
	}
	var underlying types.Type
	if named, ok := st.(*types.Named); ok {
		underlying = named.Underlying()
	} else {
		underlying = st.Underlying()
	}
	s, ok := underlying.(*types.Struct)
	if !ok {
		return false
	}
	for fi := 0; fi < s.NumFields(); fi++ {
		f := s.Field(fi)
		if f.Name() == sagaCompensateFieldName && isCompensateFuncType(f.Type()) {
			return true
		}
	}
	return false
}

// classifyExpr returns a compensateFuncAssignment for expr: either a FuncLit
// or a named-function Ident.
func classifyExpr(expr ast.Expr) compensateFuncAssignment {
	switch e := expr.(type) {
	case *ast.FuncLit:
		return compensateFuncAssignment{Lit: e}
	case *ast.Ident:
		return compensateFuncAssignment{NamedIdent: e}
	}
	return compensateFuncAssignment{}
}

// sagaFuncDeclsByObject collects all top-level FuncDecls in the Pass (across
// all files) keyed by the types.Func pointer (via info.ObjectOf).
func sagaFuncDeclsByObject(p *Pass) map[*types.Func]*ast.FuncDecl {
	out := make(map[*types.Func]*ast.FuncDecl)
	for _, f := range p.Files {
		EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			obj, ok := p.TypesInfo.Defs[fd.Name].(*types.Func)
			if !ok {
				return
			}
			out[obj] = fd
		})
	}
	return out
}

// countStmts counts the top-level statements in a BlockStmt.
func countStmts(body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}
	return len(body.List)
}

// sagaDiag constructs a Diagnostic for a saga rule.
func sagaDiag(p *Pass, node ast.Node, rel, msg string) Diagnostic {
	return Diagnostic{Rel: rel, Line: p.Fset.Position(node.Pos()).Line, Message: msg}
}

// scanBodyForForbiddenCalls scans a BlockStmt for calls to banned receivers.
func scanBodyForForbiddenCalls(p *Pass, body *ast.BlockStmt, rel string, ifaces []bannedReceiver) []Diagnostic {
	info := p.TypesInfo
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return
		}
		if label, banned := sagaBannedReceiverLabel(info, sel, ifaces); banned {
			out = append(out, sagaDiag(p, sel, rel,
				sagaCompensatePureRuleID+"-A1: CompensateFunc body must be pure-reverse; "+
					"it calls a method on "+label+
					" (Compensate runs in app domain only — the Coordinator owns the tx layer)"))
		}
	})
	return out
}

// scanCompensatePure implements A1 over one file: collect every CompensateFunc
// assignment (literal or named func) and check the body for forbidden calls.
func scanCompensatePure(p *Pass, file *ast.File, ifaces []bannedReceiver, funcDecls map[*types.Func]*ast.FuncDecl) []Diagnostic {
	rel := p.Rel(file)
	var out []Diagnostic

	assignments := collectCompensateAssignments(p, file)
	for _, a := range assignments {
		switch {
		case a.Lit != nil:
			out = append(out, scanBodyForForbiddenCalls(p, a.Lit.Body, rel, ifaces)...)
		case a.NamedIdent != nil:
			obj, ok := p.TypesInfo.ObjectOf(a.NamedIdent).(*types.Func)
			if !ok {
				continue
			}
			fd, ok := funcDecls[obj]
			if !ok {
				continue
			}
			out = append(out, scanBodyForForbiddenCalls(p, fd.Body, rel, ifaces)...)
		}
	}
	return out
}

// scanCompensateBodyCount implements B1 over one file: every CompensateFunc
// literal must have <= sagaMaxCompensateStmts top-level statements.
func scanCompensateBodyCount(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	var out []Diagnostic

	assignments := collectCompensateAssignments(p, file)
	for _, a := range assignments {
		if a.Lit == nil {
			continue
		}
		n := countStmts(a.Lit.Body)
		if n > sagaMaxCompensateStmts {
			out = append(out, sagaDiag(p, a.Lit, rel,
				sagaCompensatePureRuleID+"-B1: CompensateFunc body has too many statements ("+
					itoa(n)+">"+itoa(sagaMaxCompensateStmts)+"); "+
					"pure-reverse compensates must be short — extract logic to a named helper "+
					"and call it from Compensate (B1 blind-spot discipline)"))
		}
	}
	return out
}

// sagaCompensateFixturePattern returns the relative directory and module-path
// pattern for a named compensate-pure fixture sub-directory.
// Used by Test* functions that drive fixture-based detector self-tests.
func sagaCompensateFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaFixturesDir + "/" + fix
}

// sagaLeaderGateFixturePattern returns the relative directory and module-path
// pattern for a named leader-gate fixture sub-directory.
// Used by Test* functions that drive fixture-based detector self-tests.
func sagaLeaderGateFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaLeaderGateFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaLeaderGateFixturesDir + "/" + fix
}

// sagaStepRunFixturePattern returns the relative directory and module-path
// pattern for a named step-run-outside-tx fixture sub-directory.
// Used by Test* functions that drive fixture-based detector self-tests.
func sagaStepRunFixturePattern(fix string) (dir, pattern string) {
	return filepath.Join("tools", "archtest", "testdata", sagaStepRunFixturesDir, fix),
		"./tools/archtest/testdata/" + sagaStepRunFixturesDir + "/" + fix
}

// CheckSagaStepCompensatePure is the importable, registered form of
// SAGA-STEP-COMPENSATE-PURE-01. It scans the running module's production code
// (with cfg.BuildTags) for CompensateFunc bodies that call forbidden persistent
// interfaces (outbox.Writer, outbox.Emitter, persistence.TxRunner, *sql.Tx,
// pgx.Tx). This rule applies to any Cell that uses the saga engine and assigns
// CompensateFunc values.
//
// Registered in [StandardCellRules].
func CheckSagaStepCompensatePure(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	var all []Diagnostic
	scan := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		ifaces := sagaResolveBannedReceivers(p.Pkg)
		funcDecls := sagaFuncDeclsByObject(p)
		var out []Diagnostic
		for _, file := range p.Files {
			if strings.HasSuffix(filepath.ToSlash(p.Rel(file)), "_test.go") {
				continue
			}
			out = append(out, scanCompensatePure(p, file, ifaces, funcDecls)...)
		}
		return out
	}
	all = append(all, Run(t, Production(TypedOpts{Tags: FlatNonDefaultTags()}), scan)...)
	if len(cfg.BuildTags) > 0 {
		seen := map[string]bool{}
		for _, d := range all {
			seen[d.Rel+":"+itoa(d.Line)] = true
		}
		extra := Run(t, Production(TypedOpts{Tags: cfg.BuildTags}), scan)
		for _, d := range extra {
			if !seen[d.Rel+":"+itoa(d.Line)] {
				all = append(all, d)
			}
		}
	}
	return all
}

// ─── SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 helpers ──────────────────────────

// scanHeartbeatSelectors walks every SelectorExpr whose Sel.Name == "Heartbeat"
// (both inside CallExpr and as a method value) and emits a diagnostic when the
// receiver implements the Heartbeater-shape signature.
func scanHeartbeatSelectors(p *Pass, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if sel.Sel == nil || sel.Sel.Name != heartbeatMethodName {
			return
		}
		fn, ok := ResolveMethodCall(p.TypesInfo, sel)
		if !ok || fn == nil {
			return
		}
		if !methodIsHeartbeaterShape(fn) {
			return
		}
		pos := p.Fset.Position(sel.Pos())
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"%s: forbidden reference to a Heartbeater-shape .Heartbeat method in runtime/saga; "+
					"per-step lease maintenance is funneled through runtime/saga/executor "+
					"(Executor.Execute / Executor.RunWithHeartbeat). If you need a "+
					"non-step heartbeat (e.g. extending lease across an outer operation), "+
					"call Executor.RunWithHeartbeat — do NOT re-introduce a centralized "+
					"heartbeat goroutine.",
				sagaCoordinatorNoHeartbeatLoopRule,
			),
		})
	})
	return out
}

// methodIsHeartbeaterShape reports whether fn has the Heartbeater-shape signature.
func methodIsHeartbeaterShape(fn *types.Func) bool {
	if fn == nil {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || fn.Name() != heartbeatMethodName {
		return false
	}
	return signatureMatchesHeartbeaterShape(sig)
}

// signatureMatchesHeartbeaterShape validates the Heartbeat parameter/result shape.
func signatureMatchesHeartbeaterShape(sig *types.Signature) bool {
	params := sig.Params()
	results := sig.Results()
	if params.Len() != 4 || results.Len() != 2 {
		return false
	}
	// param[0]: context.Context
	if !isContextType(params.At(0).Type()) {
		return false
	}
	// param[1], param[2]: idutil.SafeID
	if !typeIsNamed(params.At(1).Type(), heartbeaterIdutilPkgName, heartbeaterSafeIDType) {
		return false
	}
	if !typeIsNamed(params.At(2).Type(), heartbeaterIdutilPkgName, heartbeaterSafeIDType) {
		return false
	}
	// param[3]: time.Duration
	if !isTimeDurationType(params.At(3).Type()) {
		return false
	}
	// results: (bool, error)
	boolType := types.Typ[types.Bool]
	if !types.Identical(results.At(0).Type(), boolType) {
		return false
	}
	errIface, ok := results.At(1).Type().Underlying().(*types.Interface)
	return ok && errIface.NumMethods() == 1 && errIface.Method(0).Name() == "Error"
}

// typeIsNamed reports whether t resolves to a named type whose enclosing
// package's import-path *suffix* equals pkgSuffix and whose declared name
// equals typeName.
func typeIsNamed(t types.Type, pkgSuffix, typeName string) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Name() != typeName {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == pkgSuffix || strings.HasSuffix(pkg.Path(), "/"+pkgSuffix)
}

// CheckSagaCoordinatorNoHeartbeatLoop is the importable form of
// SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01. It scans runtime/saga/ production
// files (excluding runtime/saga/executor/) for any .Heartbeat SelectorExpr
// whose receiver implements the Heartbeater-shape signature.
// Not registered in StandardCellRules (targets GoCell's own runtime/saga).
func CheckSagaCoordinatorNoHeartbeatLoop(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg
	var out []Diagnostic
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/saga/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			if strings.HasPrefix(rel, "runtime/saga/executor/") {
				continue
			}
			out = append(out, scanHeartbeatSelectors(p, file, rel)...)
		}
		return nil
	})
	return out
}

// ─── SAGA-EXECUTOR-RAND-INJECTED-01 helpers ──────────────────────────────────

// isRandGlobalForbidden reports whether fn is a package-level function in
// math/rand or math/rand/v2 that is NOT in the allowed constructor set.
func isRandGlobalForbidden(fn *types.Func) bool {
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	pkgPath := fn.Pkg().Path()
	if pkgPath != randV1PkgPath && pkgPath != randV2PkgPath {
		return false
	}
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return false
	}
	key := pkgPath + "." + fn.Name()
	return !randAllowedConstructors[key]
}

// scanRandGlobalCalls walks file's AST and returns diagnostics for every
// call to a forbidden package-level global rand function.
func scanRandGlobalCalls(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	info := p.TypesInfo
	var out []Diagnostic
	seen := map[string]bool{}

	record := func(node ast.Node, fnName, pkgPath string) {
		line := p.Fset.Position(node.Pos()).Line
		key := fmt.Sprintf("%s:%d:%s.%s", rel, line, pkgPath, fnName)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				sagaRandInjectedRuleID+": %s.%s — "+
					"must use an injected *rand.Rand source (rand.New(rand.NewPCG(...))) "+
					"instead of the package-level global; "+
					"global rand is not injectable and makes jitter non-deterministic in tests",
				pkgPath, fnName,
			),
		})
	}

	EachInSubtree[ast.SelectorExpr](file, func(e *ast.SelectorExpr) {
		fn, ok := info.ObjectOf(e.Sel).(*types.Func)
		if !ok || !isRandGlobalForbidden(fn) {
			return
		}
		record(e, fn.Name(), fn.Pkg().Path())
	})

	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
		fn, ok := info.ObjectOf(e).(*types.Func)
		if !ok || !isRandGlobalForbidden(fn) {
			return
		}
		record(e, fn.Name(), fn.Pkg().Path())
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// isExecutorProductionFile reports whether rel is a production .go file
// under runtime/saga/executor/ (not _test.go).
func isExecutorProductionFile(rel string) bool {
	return strings.HasPrefix(rel, executorPkgPrefix) && !strings.HasSuffix(rel, "_test.go")
}

// CheckSagaExecutorRandInjected is the importable form of
// SAGA-EXECUTOR-RAND-INJECTED-01. It scans runtime/saga/executor/ production
// files for package-level global rand calls.
// Not registered in StandardCellRules (targets GoCell's own runtime/saga/executor).
func CheckSagaExecutorRandInjected(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg
	var out []Diagnostic
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/saga/executor/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if !isExecutorProductionFile(rel) {
				continue
			}
			out = append(out, scanRandGlobalCalls(p, file)...)
		}
		return nil
	})
	return out
}

// ─── SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 helpers ─────────────────────────

// collectSagaJournalImpls collects concrete types in pkg that implement iface,
// populating implSet ("pkg/path.TypeName" → true) and implPkgSet (pkg path → true).
func collectSagaJournalImpls(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) { //nolint:gocognit,lll // archtest: value/pointer receivers + alias forms via typesutil.ImplementsInterface enumeration
	if pkg == nil {
		return
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil {
			continue
		}
		if _, isTypeName := obj.(*types.TypeName); !isTypeName {
			continue
		}
		t := obj.Type()
		// typesutil.ImplementsInterface checks value-or-pointer receivers (the
		// sanctioned funnel for go/types.Implements; TYPESUTIL-IMPLEMENTS-FUNNEL-01).
		if typesutil.ImplementsInterface(t, iface) {
			if named, ok := t.(*types.Named); ok {
				if named.Underlying() != nil {
					if _, isIface := named.Underlying().(*types.Interface); !isIface {
						key := pkg.Path() + "." + name
						implSet[key] = true
						implPkgSet[pkg.Path()] = true
					}
				}
			}
		}
	}
}

// factoryConstructedImpls returns the impl keys ("pkg/path.TypeName") constructed
// inside the factory argument (call.Args[1]) of a conformance call. Resolves three
// factory forms: an inline FuncLit, a named-func Ident (its FuncDecl in the package),
// and a local var bound to a FuncLit. Any other form resolves to no body and credits
// nothing — fail-closed: an unrecognized factory shape flags its impl as UNENROLLED
// (CI-visible) rather than silently crediting it.
func factoryConstructedImpls(call *ast.CallExpr, info *types.Info, files []*ast.File, implSet map[string]bool) []string {
	if len(call.Args) < 2 {
		return nil
	}
	body := factoryBody(call.Args[1], info, files)
	if body == nil {
		return nil
	}
	var out []string
	EachInSubtree[ast.CallExpr](body, func(c *ast.CallExpr) {
		out = append(out, extractEnrolledImpls(c, info, implSet)...)
	})
	return out
}

// extractEnrolledImpls inspects a CallExpr's callee signature and returns
// implKey strings ("pkg/path.TypeName") for every concrete Journal impl that
// the call constructs (return values whose underlying named type is in
// implSet). Pointer wrappers (*T) are unwrapped. Cross-package type identity
// is irrelevant — we compare by string keys, so the iface-pass and test-pass
// loads do not need to share *types.Named instances.
//
// This is the impl-level upgrade of the prior package-level enrollment: a
// test file is now only credited with enrolling impl X if it actually
// constructs X — package co-location is no longer enough.
func extractEnrolledImpls(call *ast.CallExpr, info *types.Info, implSet map[string]bool) []string {
	if info == nil {
		return nil
	}
	calleeType := info.TypeOf(call.Fun)
	if calleeType == nil {
		return nil
	}
	sig, ok := calleeType.Underlying().(*types.Signature)
	if !ok {
		return nil
	}
	var out []string
	results := sig.Results()
	for i := 0; i < results.Len(); i++ {
		rt := results.At(i).Type()
		if ptr, isPtr := rt.(*types.Pointer); isPtr {
			rt = ptr.Elem()
		}
		named, ok := rt.(*types.Named)
		if !ok {
			continue
		}
		obj := named.Obj()
		if obj == nil || obj.Pkg() == nil {
			continue
		}
		key := obj.Pkg().Path() + "." + obj.Name()
		if implSet[key] {
			out = append(out, key)
		}
	}
	return out
}

// creditEnrollmentsFromFactory credits conformance enrollment for impls
// constructed in the factory argument of a RunConformanceSuite /
// RunGlobalReaderConformance call.
func creditEnrollmentsFromFactory(
	info *types.Info,
	files []*ast.File,
	file *ast.File,
	conformanceFuncName string,
	implSet, enrolledImpls map[string]bool,
) {
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != sagaConformancePkg || name != conformanceFuncName {
			return
		}
		for _, key := range factoryConstructedImpls(call, info, files, implSet) {
			enrolledImpls[key] = true
		}
	})
}

// factoryBody resolves a conformance factory argument to the function body that
// constructs the impl: a direct FuncLit, a named-func Ident (its FuncDecl), or a
// local var Ident bound to a FuncLit (`factory := func(){…}`). Returns nil for
// any other form.
func factoryBody(arg ast.Expr, info *types.Info, files []*ast.File) *ast.BlockStmt {
	switch a := arg.(type) {
	case *ast.FuncLit:
		return a.Body
	case *ast.Ident:
		obj := info.ObjectOf(a)
		if obj == nil {
			return nil
		}
		switch obj.(type) {
		case *types.Func:
			if fd := findFuncDeclFor(info, files, obj); fd != nil {
				return fd.Body
			}
		case *types.Var:
			if fl := findVarFuncLit(info, files, obj); fl != nil {
				return fl.Body
			}
		}
	}
	return nil
}

// findFuncDeclFor finds the FuncDecl in files whose name identifier defines obj.
func findFuncDeclFor(info *types.Info, files []*ast.File, obj types.Object) *ast.FuncDecl {
	for _, f := range files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if info.Defs[fd.Name] == obj {
				return fd
			}
		}
	}
	return nil
}

// findVarFuncLit finds the FuncLit a local var (obj) is bound to via
// `v := func(){…}` or `var v = func(){…}`.
func findVarFuncLit(info *types.Info, files []*ast.File, obj types.Object) *ast.FuncLit { //nolint:gocognit,lll // archtest AST scanner: handles AssignStmt (:=) and ValueSpec (var) forms with early-exit across multiple files
	for _, f := range files {
		var found *ast.FuncLit
		// Short variable declarations: f := func(){...}
		EachInSubtree[ast.AssignStmt](f, func(s *ast.AssignStmt) {
			if found != nil || len(s.Lhs) != 1 || len(s.Rhs) != 1 {
				return
			}
			if id, ok := s.Lhs[0].(*ast.Ident); ok && info.Defs[id] == obj {
				if fl, ok := s.Rhs[0].(*ast.FuncLit); ok {
					found = fl
				}
			}
		})
		if found != nil {
			return found
		}
		// Package-level or block-level var declarations: var f = func(){...}
		EachInSubtree[ast.ValueSpec](f, func(vs *ast.ValueSpec) {
			if found != nil {
				return
			}
			for i, name := range vs.Names {
				if info.Defs[name] == obj && i < len(vs.Values) {
					if fl, ok := vs.Values[i].(*ast.FuncLit); ok {
						found = fl
					}
				}
			}
		})
		if found != nil {
			return found
		}
	}
	return nil
}

// CheckSagaJournalConformanceEnrollment is the importable form of
// SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01. It scans the running module for
// concrete journal.Journal implementations that lack a conformance suite call.
// Not registered in StandardCellRules (targets GoCell-internal journal impls).
func CheckSagaJournalConformanceEnrollment(t *testing.T, cfg ConfigForExternalCell) []Diagnostic { //nolint:gocognit,funlen,lll // archtest: mirrors GlobalReader enrollment; journal.Journal target; parallel structure intentional
	t.Helper()
	_ = cfg

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/saga/journal/..."}, prodPatterns...)

	var iface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == sagaJournalPkg {
				if obj := p.Pkg.Scope().Lookup(sagaJournalIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							iface = i.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	if iface == nil {
		return []Diagnostic{{
			Rel:     "kernel/saga/journal",
			Line:    0,
			Message: "SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01: failed to resolve journal.Journal interface; check import path " + sagaJournalPkg,
		}}
	}

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectSagaJournalImpls(pkg, iface, implSet, implPkgSet)
		}
	}

	enrolledImpls := make(map[string]bool)
	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, prodscan.Patterns(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				if !strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				creditEnrollmentsFromFactory(p.TypesInfo, p.Files, f,
					sagaConformanceFuncName, implSet, enrolledImpls)
			}
			return nil
		})

	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: kernel/saga/journal.Journal impl %q not enrolled "+
					"(SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s (or its external _test) that "+
					"both calls sagajournaltest.RunConformanceSuite(t, factory) "+
					"and constructs the impl inside the factory.",
				implKey, pkgPath,
			),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}

// CheckSagaGlobalReaderConformanceEnrollment is the importable form of
// SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01.
// Not registered in StandardCellRules.
func CheckSagaGlobalReaderConformanceEnrollment(t *testing.T, cfg ConfigForExternalCell) []Diagnostic { //nolint:gocognit,funlen,lll // archtest: mirrors Journal enrollment; journal.GlobalReader target; parallel structure intentional
	t.Helper()
	_ = cfg

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)
	ifacePatterns := append([]string{"./kernel/saga/journal/..."}, prodPatterns...)

	var iface *types.Interface
	var implPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, ifacePatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == sagaJournalPkg {
				if obj := p.Pkg.Scope().Lookup(sagaGlobalReaderIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							iface = i.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	if iface == nil {
		return []Diagnostic{{
			Rel:  "kernel/saga/journal",
			Line: 0,
			Message: "SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01: failed to resolve journal.GlobalReader interface; " +
				"check import path " + sagaJournalPkg,
		}}
	}

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range implPkgs {
		if pkg != nil {
			collectSagaJournalImpls(pkg, iface, implSet, implPkgSet)
		}
	}

	enrolledImpls := make(map[string]bool)
	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, prodscan.Patterns(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				if !strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				creditEnrollmentsFromFactory(p.TypesInfo, p.Files, f,
					sagaGlobalReaderConformanceFunc, implSet, enrolledImpls)
			}
			return nil
		})

	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: kernel/saga/journal.GlobalReader impl %q not enrolled "+
					"(SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01). "+
					"Add a _test.go in package %s (or its external _test) that "+
					"both calls sagajournaltest.RunGlobalReaderConformance(t, factory) "+
					"and constructs the impl inside the factory.",
				implKey, pkgPath,
			),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}

// ─── SAGA-JOURNAL-HOLDER-SEAL-01 helpers ─────────────────────────────────────

// journalFieldKind classifies a struct field's resolved type against the three
// journal-package interfaces this seal cares about.
type journalFieldKind int

const (
	journalFieldNone        journalFieldKind = iota // not a journal-package interface
	journalFieldFull                                // journal.Journal (Heartbeat-bearing)
	journalFieldHeartbeater                         // journal.Heartbeater (Heartbeat-bearing)
	journalFieldCore                                // journal.JournalCore (Heartbeat-free)
)

// classifyJournalFieldType resolves expr via info.Types to a known
// journal-package interface, returning its kind.
func classifyJournalFieldType(info *types.Info, expr ast.Expr) journalFieldKind {
	if info == nil {
		return journalFieldNone
	}
	tv, ok := info.Types[expr]
	if !ok {
		return journalFieldNone
	}
	return classifyResolvedJournalType(tv.Type)
}

// classifyResolvedJournalType maps a resolved types.Type to a journalFieldKind.
func classifyResolvedJournalType(t types.Type) journalFieldKind {
	if t == nil {
		return journalFieldNone
	}
	// Collapse a top-level alias (`type J = journal.JournalCore`) before the
	// pointer probe, then again after unwrapping a pointer (`*J`, or
	// `type J = *journal.JournalCore`), so every alias⇄pointer ordering resolves.
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok {
		return journalFieldNone
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != sagaJournalPkg {
		return journalFieldNone
	}
	switch obj.Name() {
	case journalInterfaceTypeName:
		return journalFieldFull
	case heartbeaterInterfaceTypeName:
		return journalFieldHeartbeater
	case journalCoreInterfaceTypeName:
		return journalFieldCore
	}
	return journalFieldNone
}

// journalFieldSealDiag emits a diagnostic when a struct field in runtime/saga
// holds a forbidden journal type.
func journalFieldSealDiag(p *Pass, rel, holderName string, field *ast.Field) (Diagnostic, bool) {
	kind := classifyJournalFieldType(p.TypesInfo, field.Type)
	if kind == journalFieldNone {
		return Diagnostic{}, false
	}
	msg := journalFieldSealMessage(kind, holderName)
	if msg == "" {
		return Diagnostic{}, false
	}
	return sagaDiag(p, field, rel, msg), true
}

// journalFieldSealMessage returns the diagnostic message for a forbidden
// journal field, or "" if the field is explicitly allowed.
func journalFieldSealMessage(kind journalFieldKind, holderName string) string {
	switch kind {
	case journalFieldFull:
		return sagaJournalHolderSealRule + "-rule1: runtime/saga struct " + holderName +
			" holds journal.Journal (Heartbeat-bearing); " +
			"only journal.JournalCore may be persisted as a field in runtime/saga " +
			"(Coordinator.journal was narrowed from Journal to JournalCore in #1209 to " +
			"make a centralized heartbeat loop compile-impossible)"
	case journalFieldHeartbeater:
		return sagaJournalHolderSealRule + "-rule1: runtime/saga struct " + holderName +
			" holds journal.Heartbeater (Heartbeat-bearing); " +
			"same restriction as journal.Journal — only JournalCore may be persisted"
	case journalFieldCore:
		if holderName == allowedSagaJournalHolder {
			return "" // allowed: Coordinator.journal is journal.JournalCore
		}
		return sagaJournalHolderSealRule + "-rule2: runtime/saga struct " + holderName +
			" holds journal.JournalCore; only " + allowedSagaJournalHolder +
			" may hold a JournalCore field — other structs must not persist it"
	}
	return ""
}

// heartbeatFuncFieldDiag emits a diagnostic when a struct field in runtime/saga
// has a heartbeat-shaped func signature.
func heartbeatFuncFieldDiag(p *Pass, rel, holderName string, field *ast.Field) (Diagnostic, bool) {
	if p.TypesInfo == nil {
		return Diagnostic{}, false
	}
	tv, ok := p.TypesInfo.Types[field.Type]
	if !ok {
		return Diagnostic{}, false
	}
	// A field typed as a named func type (e.g. `type HBFunc func(...)`) surfaces
	// as *types.Named in go/types; its Underlying() is the *types.Signature.
	// An alias (`type HBFunc = func(...)`) surfaces as *types.Alias in Go 1.23+,
	// so we must Unalias before probing. Then strip a pointer if present.
	ft := types.Unalias(tv.Type)
	if ptr, ok2 := ft.(*types.Pointer); ok2 {
		ft = types.Unalias(ptr.Elem())
	}
	sig, ok := ft.Underlying().(*types.Signature)
	if !ok {
		return Diagnostic{}, false
	}
	if !signatureMatchesHeartbeaterShape(sig) {
		return Diagnostic{}, false
	}
	return sagaDiag(p, field, rel,
			sagaJournalHolderSealRule+"-rule3: runtime/saga struct "+holderName+
				" has a heartbeat-shaped func field "+
				"(func(ctx, instanceID, leaseID SafeID, dur time.Duration) (bool, error)); "+
				"this is a potential bypass path for the heartbeat loop ban "+
				"(see SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01; SAGA-JOURNAL-HOLDER-SEAL-01 rule 3 medium guard)"),
		true
}

// journalPackageLocalNames collects the set of local names that any import in
// file uses for the journal package (sagaJournalPkg).
func journalPackageLocalNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != sagaJournalPkg {
			continue
		}
		if imp.Name != nil {
			names[imp.Name.Name] = true
		} else {
			names["journal"] = true
		}
	}
	return names
}

// journalInterfaceAliasName returns the aliased name if ts is a type alias to
// one of the three journal interfaces, using journalLocalNames for resolution.
// journalInterfaceAliasName reports the journal interface name aliased by ts if
// ts is `type X = <localName>.{Journal,JournalCore,Heartbeater}` where localName
// is any local binding of the journal package (journalLocalNames), else
// ("", false). Resolving via journalLocalNames rather than a hardcoded "journal"
// closes the import-alias evasion: `import sagajournal "…/journal"` followed by
// `type X = sagajournal.JournalCore` is now flagged.
func journalInterfaceAliasName(ts *ast.TypeSpec, journalLocalNames map[string]bool) (string, bool) {
	// An alias has a valid Assign token.
	if !ts.Assign.IsValid() {
		return "", false
	}
	sel, ok := ts.Type.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok || !journalLocalNames[xIdent.Name] {
		return "", false
	}
	switch sel.Sel.Name {
	case journalInterfaceTypeName, journalCoreInterfaceTypeName, heartbeaterInterfaceTypeName:
		return sel.Sel.Name, true
	}
	return "", false
}

// CheckSagaJournalHolderSeal is the importable form of
// SAGA-JOURNAL-HOLDER-SEAL-01.
// Not registered in StandardCellRules.
func CheckSagaJournalHolderSeal(t *testing.T, cfg ConfigForExternalCell) []Diagnostic { //nolint:gocognit,lll // archtest: three sub-rules (Journal/Heartbeater, JournalCore holder, heartbeat func field) require separate branches
	t.Helper()
	_ = cfg
	var out []Diagnostic
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/saga/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") || !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			EachInSubtree[ast.StructType](file, func(st *ast.StructType) {
				// Find the enclosing type declaration name.
				// We walk upward from the StructType, which requires finding
				// the GenDecl/TypeSpec parent. Since we only need the struct
				// name for the diagnostic message, we do best-effort via
				// TypesInfo: find any TypeSpec that is a parent via file walk.
				holderName := sagaStructHolderName(p.TypesInfo, file, st)
				if st.Fields == nil {
					return
				}
				for _, field := range st.Fields.List {
					if d, ok := journalFieldSealDiag(p, rel, holderName, field); ok {
						out = append(out, d)
					}
					if d, ok := heartbeatFuncFieldDiag(p, rel, holderName, field); ok {
						out = append(out, d)
					}
				}
			})
		}
		return nil
	})
	return out
}

// sagaStructHolderName returns the declared type name of the struct that
// contains st, or "unknown" if not found. It iterates TypesInfo.Defs to find
// a TypeName whose declared type's underlying is st.
func sagaStructHolderName(_ *types.Info, file *ast.File, st *ast.StructType) string {
	// Walk GenDecl/TypeSpec in the file to find the enclosing TypeSpec.
	var name string
	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if name != "" {
			return
		}
		structLit, ok := ts.Type.(*ast.StructType)
		if !ok || structLit != st {
			return
		}
		name = ts.Name.Name
	})
	if name == "" {
		return "unknown"
	}
	return name
}

// ─── SAGA-DRIVE-BEHIND-LEADER-GATE-01 helpers ────────────────────────────────

// callIsMethodNamed reports whether call.Fun is a SelectorExpr or Ident with
// the given method name.
func callIsMethodNamed(call *ast.CallExpr, name string) bool {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel != nil && f.Sel.Name == name
	case *ast.Ident:
		return f.Name == name
	}
	return false
}

// checkLeaderGateA1 asserts driveOne calls only appear in tickOnce bodies.
func checkLeaderGateA1(p *Pass, file *ast.File) []Diagnostic {
	rel := filepath.ToSlash(p.Rel(file))
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Body == nil {
			return
		}
		if fd.Name.Name == tickOnceFuncName {
			return // allowed
		}
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if callIsMethodNamed(call, driveOneMethodName) {
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(call.Pos()).Line,
					Message: sagaLeaderGateRule + "-A1: driveOne CallExpr outside tickOnce body — " +
						"driveOne must only be called from tickOnce so the leader-elect gate guards every drive",
				})
			}
		})
	})
	return out
}

// checkLeaderGateA2 asserts tickOnce calls acquireLead.
func checkLeaderGateA2(p *Pass, file *ast.File) []Diagnostic {
	rel := filepath.ToSlash(p.Rel(file))
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Body == nil || fd.Name.Name != tickOnceFuncName {
			return
		}
		if !bodyCallsMethodNamed(fd.Body, acquireLeadMethodName) {
			out = append(out, Diagnostic{
				Rel:     rel,
				Line:    p.Fset.Position(fd.Pos()).Line,
				Message: sagaLeaderGateRule + "-A2: tickOnce body does not call acquireLead — the sole driveOne caller must pass the leader-elect gate",
			})
		}
	})
	return out
}

// checkLeaderGateA3 asserts the lead result from acquireLead gates driveOne.
func checkLeaderGateA3(p *Pass, file *ast.File) []Diagnostic {
	rel := filepath.ToSlash(p.Rel(file))
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Body == nil || fd.Name.Name != tickOnceFuncName {
			return
		}
		leadVar, ok := acquireLeadResultVar(fd.Body)
		if !ok {
			return // no acquireLead result captured — A2 will report
		}
		if !leadGatesDrive(fd.Body, leadVar) {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(fd.Pos()).Line,
				Message: sagaLeaderGateRule + "-A3: tickOnce calls acquireLead but its lead result does not gate driveOne — " +
					"the gate verdict must guard the drive (e.g. `if !lead { continue }`)",
			})
		}
	})
	return out
}

// bodyCallsMethodNamed reports whether the body contains a direct call to name.
func bodyCallsMethodNamed(body *ast.BlockStmt, name string) bool {
	found := false
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if !found && callIsMethodNamed(call, name) {
			found = true
		}
	})
	return found
}

// acquireLeadResultVar finds the assignment whose RHS is `<expr>.acquireLead(...)`
// and returns the identifier bound to the last LHS element (the lead bool). ok is
// false when acquireLead is not assigned to a tuple of at least 2 elements or the
// lead slot is blank (`_`) — a discarded verdict cannot gate the drive.
//
// acquireLead currently returns 3 values (release, orphan, lead); the last LHS
// element is always the bool gate regardless of tuple arity, so arity ≥ 2 is
// accepted. Fixtures using the historical 2-return form also pass.
func acquireLeadResultVar(body *ast.BlockStmt) (name string, ok bool) {
	as, found := FindFirstInSubtree[ast.AssignStmt](body, func(as *ast.AssignStmt) bool {
		if len(as.Rhs) != 1 || len(as.Lhs) < 2 {
			return false
		}
		call, isCall := as.Rhs[0].(*ast.CallExpr)
		if !isCall || !callIsMethodNamed(call, acquireLeadMethodName) {
			return false
		}
		last := as.Lhs[len(as.Lhs)-1]
		id, isID := last.(*ast.Ident)
		return isID && id.Name != "_"
	})
	if !found {
		return "", false
	}
	last := as.Lhs[len(as.Lhs)-1]
	id := last.(*ast.Ident)
	return id.Name, true
}

// leadGatesDrive reports whether some IfStmt in body has a condition referencing
// leadVar and a body that either early-exits (BranchStmt/ReturnStmt) or contains
// a driveOne call — the two sanctioned gate shapes.
func leadGatesDrive(body *ast.BlockStmt, leadVar string) bool {
	_, ok := FindFirstInSubtree[ast.IfStmt](body, func(ifs *ast.IfStmt) bool {
		if ifs.Cond == nil || !condReferences(ifs.Cond, leadVar) {
			return false
		}
		return ifBodyEarlyExits(ifs.Body) || bodyCallsMethodNamed(ifs.Body, driveOneMethodName)
	})
	return ok
}

// condReferences reports whether expr contains an identifier named want.
func condReferences(expr ast.Expr, want string) bool {
	_, ok := FindFirstInSubtree[ast.Ident](expr, func(id *ast.Ident) bool {
		return id.Name == want
	})
	return ok
}

// ifBodyEarlyExits reports whether body contains a continue/break/return that
// skips the rest of the claim-loop iteration before driveOne runs.
func ifBodyEarlyExits(body *ast.BlockStmt) bool {
	exits := false
	EachInSubtree[ast.BranchStmt](body, func(*ast.BranchStmt) { exits = true })
	if exits {
		return true
	}
	EachInSubtree[ast.ReturnStmt](body, func(*ast.ReturnStmt) { exits = true })
	return exits
}

// CheckSagaDriveBehindLeaderGate is the importable form of
// SAGA-DRIVE-BEHIND-LEADER-GATE-01.
// Not registered in StandardCellRules.
func CheckSagaDriveBehindLeaderGate(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg
	var out []Diagnostic
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/saga/..."}), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") || !strings.HasPrefix(rel, "runtime/saga/") {
				continue
			}
			out = append(out, checkLeaderGateA1(p, file)...)
			out = append(out, checkLeaderGateA2(p, file)...)
			out = append(out, checkLeaderGateA3(p, file)...)
		}
		return nil
	})
	return out
}

// ─── SAGA-STEP-RUN-OUTSIDE-TX-01 helpers ─────────────────────────────────────

// sagaLocalName returns the local identifier used to import kernel/saga in
// file, or "saga" if the package is imported without an alias. Uses
// strconv.Unquote to compare against the derived path constant (no quoted
// literal in non-test code).
func sagaLocalName(file *ast.File) string {
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != sagaPkgPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "saga"
	}
	return ""
}

// isRuntimeSagaProductionFile reports whether rel is a production .go file
// under runtime/saga/ (not _test.go, not runtime/saga/executor/).
func isRuntimeSagaProductionFile(rel string) bool {
	return strings.HasPrefix(rel, "runtime/saga/") &&
		!strings.HasSuffix(rel, "_test.go")
}

// checkA1StepFuncCallsites implements SAGA-STEP-RUN-OUTSIDE-TX-01 A1: every
// kernel/saga.StepFunc call must only appear in safeRun function bodies.
func checkA1StepFuncCallsites(p *Pass, file *ast.File) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	rel := filepath.ToSlash(p.Rel(file))
	sagaStepFuncType := resolveSagaStepFuncType(p.Pkg)
	if sagaStepFuncType == nil {
		return nil
	}

	// Collect safeRun body ranges.
	safeRunRanges := collectFuncBodyRanges(file, safeRunFuncName)

	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !calleeIsSagaStepFunc(p.TypesInfo, call, sagaStepFuncType) {
			return
		}
		if posInRanges(call.Pos(), safeRunRanges) {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(call.Pos()).Line,
			Message: sagaStepRunOutsideTxRule + "-A1: saga.StepFunc CallExpr outside safeRun body — " +
				"StepFunc must only be invoked from safeRun (user step code outside DB tx invariant)",
		})
	})
	return out
}

// resolveSagaStepFuncType resolves the kernel/saga.StepFunc named type from
// pkg's transitive imports.
func resolveSagaStepFuncType(pkg *types.Package) *types.Named {
	if pkg == nil {
		return nil
	}
	sagaPkg := findImportedPkg(pkg, sagaPkgPath)
	if sagaPkg == nil {
		return nil
	}
	obj := sagaPkg.Scope().Lookup(stepFuncTypeName)
	if obj == nil {
		return nil
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return nil
	}
	return named
}

// findImportedPkg traverses the import graph of root to find the package at path.
func findImportedPkg(root *types.Package, path string) *types.Package {
	if root == nil {
		return nil
	}
	seen := map[string]bool{}
	var dfs func(*types.Package) *types.Package
	dfs = func(pkg *types.Package) *types.Package {
		if pkg == nil || seen[pkg.Path()] {
			return nil
		}
		seen[pkg.Path()] = true
		if pkg.Path() == path {
			return pkg
		}
		for _, imp := range pkg.Imports() {
			if found := dfs(imp); found != nil {
				return found
			}
		}
		return nil
	}
	return dfs(root)
}

// calleeIsSagaStepFunc reports whether call's callee resolves to a value of
// type kernel/saga.StepFunc (i.e. a call expression like step.Run(ctx, state)).
func calleeIsSagaStepFunc(info *types.Info, call *ast.CallExpr, sagaStepFuncType *types.Named) bool {
	if sagaStepFuncType == nil {
		return false
	}
	tv, ok := info.Types[call.Fun]
	if !ok {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	return named == sagaStepFuncType
}

// checkA2SafeRunNotInRunInTx implements A2: safeRun must not be called inside
// a TxRunner.RunInTx closure body.
func checkA2SafeRunNotInRunInTx(p *Pass, file *ast.File) []Diagnostic {
	rel := filepath.ToSlash(p.Rel(file))
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(outerCall *ast.CallExpr) {
		if !callIsRunInTx(outerCall) {
			return
		}
		// Find the closure argument (last arg that is a FuncLit).
		var closure *ast.FuncLit
		for i := len(outerCall.Args) - 1; i >= 0; i-- {
			if fl, ok := outerCall.Args[i].(*ast.FuncLit); ok {
				closure = fl
				break
			}
		}
		if closure == nil {
			return
		}
		EachInSubtree[ast.CallExpr](closure.Body, func(inner *ast.CallExpr) {
			if callIsSafeRun(inner) {
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(inner.Pos()).Line,
					Message: sagaStepRunOutsideTxRule + "-A2: safeRun() called inside RunInTx closure body — " +
						"user step code would run with a DB transaction held open",
				})
			}
		})
	})
	return out
}

// callIsRunInTx reports whether call is a RunInTx call.
func callIsRunInTx(call *ast.CallExpr) bool {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel != nil && f.Sel.Name == runInTxMethodName
	case *ast.Ident:
		return f.Name == runInTxMethodName
	}
	return false
}

// callIsSafeRun reports whether call is a safeRun call.
func callIsSafeRun(call *ast.CallExpr) bool {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel != nil && f.Sel.Name == safeRunFuncName
	case *ast.Ident:
		return f.Name == safeRunFuncName
	}
	return false
}

// checkB1NoStepFuncAlias implements B1: no production file in runtime/saga/
// may declare a type alias for kernel/saga.StepFunc.
func checkB1NoStepFuncAlias(p *Pass, file *ast.File) []Diagnostic {
	local := sagaLocalName(file)
	if local == "" {
		return nil
	}
	rel := filepath.ToSlash(p.Rel(file))
	var out []Diagnostic
	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if !ts.Assign.IsValid() {
			return // not an alias
		}
		sel, ok := ts.Type.(*ast.SelectorExpr)
		if !ok {
			return
		}
		xIdent, ok := sel.X.(*ast.Ident)
		if !ok || xIdent.Name != local || sel.Sel.Name != stepFuncTypeName {
			return
		}
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: p.Fset.Position(ts.Pos()).Line,
			Message: sagaStepRunOutsideTxRule + "-B1: type alias for kernel/saga.StepFunc in runtime/saga; " +
				"aliasing StepFunc could allow calls to escape the safeRun gate (B1 blind spot)",
		})
	})
	return out
}

// CheckSagaStepRunOutsideTx is the importable form of
// SAGA-STEP-RUN-OUTSIDE-TX-01.
// Not registered in StandardCellRules.
func CheckSagaStepRunOutsideTx(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg
	var out []Diagnostic
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/saga/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if !isRuntimeSagaProductionFile(rel) {
				continue
			}
			out = append(out, checkA1StepFuncCallsites(p, file)...)
			out = append(out, checkA2SafeRunNotInRunInTx(p, file)...)
			out = append(out, checkB1NoStepFuncAlias(p, file)...)
		}
		return nil
	})
	return out
}

// ─── SAGA-CONSTRUCTOR-NIL-GUARD-01 helpers ────────────────────────────────────

// scanConstructorNilGuards scans p for top-level New* functions that accept
// non-variadic interface parameters lacking a guard call.
func scanConstructorNilGuards(p *Pass) []Diagnostic { //nolint:gocognit,cyclop,funlen,lll // archtest: enumerates constructor params, resolves interface types, cross-checks guard callsites; inherent to SAGA-CONSTRUCTOR-NIL-GUARD-01
	if p.TypesInfo == nil {
		return nil
	}
	var out []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(filepath.ToSlash(p.Rel(file)), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Recv != nil || fd.Body == nil {
				return
			}
			if !strings.HasPrefix(fd.Name.Name, "New") {
				return
			}
			if fd.Type.Params == nil {
				return
			}
			type ifaceParam struct {
				obj      types.Object
				typeExpr ast.Expr
				name     string
				typStr   string
			}
			var ifaces []ifaceParam
			for _, field := range fd.Type.Params.List {
				if _, isEllipsis := field.Type.(*ast.Ellipsis); isEllipsis {
					continue
				}
				typ := p.TypesInfo.TypeOf(field.Type)
				if typ == nil {
					continue
				}
				if _, isIface := typ.Underlying().(*types.Interface); !isIface {
					continue
				}
				if len(field.Names) == 0 {
					out = append(out, sagaDiag(p, field.Type, rel,
						sagaConstructorNilGuardRuleID+": constructor "+fd.Name.Name+
							" has an unnamed interface parameter (type "+sagaShortType(typ)+
							") that cannot be nil-guarded; give it a name and add "+
							"validation.IsNilInterface(<param>) (or clock.MustHaveClock)"))
					continue
				}
				for _, name := range field.Names {
					obj := p.TypesInfo.Defs[name]
					if name.Name == "_" || obj == nil {
						out = append(out, sagaDiag(p, name, rel,
							sagaConstructorNilGuardRuleID+": constructor "+fd.Name.Name+
								" has a blank-identifier interface parameter (type "+sagaShortType(typ)+
								") that cannot be nil-guarded; give it a name and add "+
								"validation.IsNilInterface(<param>) (or clock.MustHaveClock)"))
						continue
					}
					ifaces = append(ifaces, ifaceParam{
						obj:      obj,
						typeExpr: field.Type,
						name:     name.Name,
						typStr:   sagaShortType(typ),
					})
				}
			}
			if len(ifaces) == 0 {
				return
			}
			for _, ip := range ifaces {
				_, guarded := FindFirstInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) bool {
					if !sagaIsGuardCall(p, call) {
						return false
					}
					if len(call.Args) == 0 {
						return false
					}
					firstArg, ok2 := ast.Unparen(call.Args[0]).(*ast.Ident)
					if !ok2 {
						return false
					}
					return p.TypesInfo.ObjectOf(firstArg) == ip.obj
				})
				if !guarded {
					out = append(out, sagaDiag(p, fd.Name, rel,
						sagaConstructorNilGuardRuleID+": constructor "+fd.Name.Name+
							" has interface parameter "+ip.name+" (type "+ip.typStr+
							") without a guard call (IsNilInterface or clock.MustHaveClock); "+
							"add validation.IsNilInterface("+ip.name+") or clock.MustHaveClock("+ip.name+", ...)"))
				}
			}
		})
	}
	return out
}

// sagaIsGuardCall reports whether call resolves to a sanctioned nil-guard
// function (validation.IsNilInterface or clock.MustHaveClock).
func sagaIsGuardCall(p *Pass, call *ast.CallExpr) bool {
	var calleeFunc *types.Func
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if f, ok := p.TypesInfo.ObjectOf(fun.Sel).(*types.Func); ok {
			calleeFunc = f
		}
	case *ast.Ident:
		if f, ok := p.TypesInfo.ObjectOf(fun).(*types.Func); ok {
			calleeFunc = f
		}
	}
	if calleeFunc == nil || calleeFunc.Pkg() == nil {
		return false
	}
	for _, gk := range sagaGuardFuncKeys {
		if calleeFunc.Pkg().Path() == gk.pkg && calleeFunc.Name() == gk.name {
			return true
		}
	}
	return false
}

// sagaShortType renders t as package.TypeName (short package name).
func sagaShortType(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}

// sagaConstructorIfaceParamObjs returns the set of named, non-variadic
// interface parameter objects of a New* constructor.
func sagaConstructorIfaceParamObjs(p *Pass, fd *ast.FuncDecl) map[types.Object]bool {
	out := map[types.Object]bool{}
	if fd.Type.Params == nil {
		return out
	}
	for _, field := range fd.Type.Params.List {
		if _, isEllipsis := field.Type.(*ast.Ellipsis); isEllipsis {
			continue
		}
		typ := p.TypesInfo.TypeOf(field.Type)
		if typ == nil {
			continue
		}
		if _, isIface := typ.Underlying().(*types.Interface); !isIface {
			continue
		}
		for _, name := range field.Names {
			if obj := p.TypesInfo.Defs[name]; obj != nil {
				out[obj] = true
			}
		}
	}
	return out
}

// CheckSagaConstructorNilGuard is the importable form of
// SAGA-CONSTRUCTOR-NIL-GUARD-01.
// Not registered in StandardCellRules.
func CheckSagaConstructorNilGuard(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg
	var out []Diagnostic
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/saga/...", "./runtime/saga/executor/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Pkg == nil {
			return nil
		}
		if !sagaConstructorScopePackages[p.Pkg.Path()] {
			return nil
		}
		out = append(out, scanConstructorNilGuards(p)...)
		return nil
	})
	return out
}

// ─── SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 helpers ────────────────────────────

// sagaSlogAttrCtorGuardedKey reports whether call is a log/slog package-level
// Attr constructor whose first argument is one of the guarded identity keys.
func sagaSlogAttrCtorGuardedKey(info *types.Info, call *ast.CallExpr) (string, bool) {
	if info == nil || len(call.Args) < 1 {
		return "", false
	}
	pkgPath, _, ok := ResolvePackageRef(info, call.Fun)
	if !ok || pkgPath != sagaSlogPkgPath {
		return "", false
	}
	if !sagaCalleeReturnsSlogAttr(info, call.Fun) {
		return "", false
	}
	key, ok := EvaluateConstString(info, call.Args[0])
	if !ok {
		return "", false
	}
	if _, guarded := sagaInstanceFieldsGuardedKeys[key]; !guarded {
		return "", false
	}
	return key, true
}

// sagaCalleeReturnsSlogAttr reports whether fun resolves to a function whose
// single result is log/slog.Attr.
func sagaCalleeReturnsSlogAttr(info *types.Info, fun ast.Expr) bool {
	tv, ok := info.Types[fun]
	if !ok {
		return false
	}
	sig, ok := tv.Type.(*types.Signature)
	if !ok || sig.Results().Len() != 1 {
		return false
	}
	named, ok := sig.Results().At(0).Type().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == sagaSlogAttrTypeName &&
		obj.Pkg() != nil && obj.Pkg().Path() == sagaSlogPkgPath
}

// scanSagaSlogInstanceFieldsFile is the A1 core: flag every log/slog Attr
// constructor call carrying a guarded identity key outside an InstanceFields body.
func scanSagaSlogInstanceFieldsFile(p *Pass, file *ast.File) []Diagnostic {
	rel := filepath.ToSlash(p.Rel(file))
	carrierRanges := collectFuncBodyRanges(file, sagaInstanceFieldsFuncName)
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		key, ok := sagaSlogAttrCtorGuardedKey(p.TypesInfo, call)
		if !ok {
			return
		}
		if posInRanges(call.Pos(), carrierRanges) {
			return
		}
		pos := p.Fset.Position(call.Pos())
		ds = append(ds, Diagnostic{
			Rel:     rel,
			Line:    pos.Line,
			Message: violSagaSlogInstanceFieldsOutsideCarrier + ` (key="` + key + `")`,
		})
	})
	return ds
}

// scanSagaSlogAttrLiteralsFile flags every log/slog.Attr composite literal
// (keyed or unkeyed) carrying a guarded identity key.
func scanSagaSlogAttrLiteralsFile(p *Pass, file *ast.File) []Diagnostic {
	rel := filepath.ToSlash(p.Rel(file))
	var ds []Diagnostic
	EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		key, ok := sagaSlogAttrLiteralGuardedKey(p.TypesInfo, cl)
		if !ok {
			return
		}
		pos := p.Fset.Position(cl.Pos())
		ds = append(ds, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: sagaSlogInstanceFieldsRule + ": slog.Attr{…} literal with key \"" + key +
				"\" — build identity attrs via sagalog.InstanceFields, not a raw slog.Attr literal (#1266)",
		})
	})
	return ds
}

// sagaSlogAttrLiteralGuardedKey reports whether cl is a log/slog.Attr
// composite literal whose Key field is one of the guarded identity keys.
func sagaSlogAttrLiteralGuardedKey(info *types.Info, cl *ast.CompositeLit) (string, bool) { //nolint:gocognit,lll // archtest: handles slog.Attr keyed/keyless forms and resolves type identity for guarded identity keys
	if info == nil {
		return "", false
	}
	named, ok := info.TypeOf(cl).(*types.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Name() != sagaSlogAttrTypeName || obj.Pkg() == nil || obj.Pkg().Path() != sagaSlogPkgPath {
		return "", false
	}
	if len(cl.Elts) == 0 {
		return "", false
	}
	// Keyed form.
	if _, isKV := cl.Elts[0].(*ast.KeyValueExpr); isKV {
		var foundKey string
		var foundOK bool
		EachInSubtree[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
			if foundOK {
				return
			}
			ident, isIdent := kv.Key.(*ast.Ident)
			if !isIdent || ident.Name != "Key" {
				return
			}
			k, guarded := sagaGuardedKeyFromExpr(info, kv.Value)
			if guarded {
				foundKey = k
				foundOK = true
			}
		})
		if foundOK {
			return foundKey, true
		}
		return "", false
	}
	// Unkeyed (positional) form.
	return sagaGuardedKeyFromExpr(info, cl.Elts[0])
}

// sagaGuardedKeyFromExpr evaluates expr to a const string and returns it when
// it is one of the guarded identity keys.
func sagaGuardedKeyFromExpr(info *types.Info, expr ast.Expr) (string, bool) {
	key, ok := EvaluateConstString(info, expr)
	if !ok {
		return "", false
	}
	if _, guarded := sagaInstanceFieldsGuardedKeys[key]; guarded {
		return key, true
	}
	return "", false
}

// CheckSagaSlogInstanceFieldsCaller is the importable form of
// SAGA-SLOG-INSTANCE-FIELDS-CALLER-01.
// Not registered in StandardCellRules.
func CheckSagaSlogInstanceFieldsCaller(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg
	var out []Diagnostic
	Run(t, Typed(TypedOpts{}, []string{"./runtime/saga/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, file := range p.Files {
			if !isRuntimeSagaProductionFile(filepath.ToSlash(p.Rel(file))) {
				continue
			}
			out = append(out, scanSagaSlogInstanceFieldsFile(p, file)...)
		}
		return nil
	})
	return out
}

// ─── SAGA-METRIC-LABEL-VALUES-FROZEN-01 helpers ──────────────────────────────

// sagaEnumTypeName returns the enum type name if t is one of the frozen
// executor label-enum named types, else "".
func sagaEnumTypeName(t types.Type) string {
	named, ok := t.(*types.Named)
	if !ok {
		return ""
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != sagaExecutorPkg {
		return ""
	}
	if _, frozen := sagaLabelEnumWant[obj.Name()]; frozen {
		return obj.Name()
	}
	return ""
}

// collectSagaEnumConsts enumerates, per frozen enum type, the string constant
// values declared in the executor package.
func collectSagaEnumConsts(p *Pass) map[string][]string {
	if p.Pkg == nil {
		return nil
	}
	got := make(map[string][]string)
	scope := p.Pkg.Scope()
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		typeName := sagaEnumTypeName(c.Type())
		if typeName == "" {
			continue
		}
		got[typeName] = append(got[typeName], strings.Trim(c.Val().ExactString(), `"`))
	}
	return got
}

// sagaValueSetDiff returns "" when got and want hold the same set, else a
// human-readable description.
func sagaValueSetDiff(got, want []string) string {
	gs, ws := slices.Clone(got), slices.Clone(want)
	slices.Sort(gs)
	slices.Sort(ws)
	if slices.Equal(gs, ws) {
		return ""
	}
	wantSet := make(map[string]struct{}, len(want))
	for _, k := range want {
		wantSet[k] = struct{}{}
	}
	gotSet := make(map[string]struct{}, len(got))
	var extra, missing []string
	for _, k := range got {
		gotSet[k] = struct{}{}
		if _, ok := wantSet[k]; !ok {
			extra = append(extra, k)
		}
	}
	for _, k := range want {
		if _, ok := gotSet[k]; !ok {
			missing = append(missing, k)
		}
	}
	slices.Sort(extra)
	slices.Sort(missing)
	return "  extra:   " + sliceOrNone(extra) + "\n  missing: " + sliceOrNone(missing)
}

// isSagaEnumConversion reports whether call is a type conversion to one of the
// frozen executor label enums.
func isSagaEnumConversion(info *types.Info, call *ast.CallExpr) bool {
	var sel *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.Ident:
		sel = f
	case *ast.SelectorExpr:
		sel = f.Sel
	default:
		return false
	}
	tn, ok := info.ObjectOf(sel).(*types.TypeName)
	if !ok {
		return false
	}
	return sagaEnumTypeName(tn.Type()) != ""
}

// isSagaDeclaredConstRef reports whether arg is a bare reference to a const
// declared in the runtime/saga/executor package AND typed as one of the frozen
// executor label enums.
func isSagaDeclaredConstRef(info *types.Info, arg ast.Expr) bool {
	var obj types.Object
	switch e := arg.(type) {
	case *ast.Ident:
		obj = info.ObjectOf(e)
	case *ast.SelectorExpr:
		obj = info.ObjectOf(e.Sel)
	default:
		return false
	}
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	if c.Pkg() == nil || c.Pkg().Path() != sagaExecutorPkg {
		return false
	}
	return sagaEnumTypeName(c.Type()) != ""
}

// sagaEnumConstViolation reports whether expr is an inline enum-typed constant
// that is NOT a valid frozen executor declared-const reference.
func sagaEnumConstViolation(info *types.Info, expr ast.Expr) string {
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil {
		return ""
	}
	typeName := sagaEnumTypeName(tv.Type)
	if typeName == "" {
		return ""
	}
	if isSagaDeclaredConstRef(info, expr) {
		return ""
	}
	return typeName
}

// scanSagaEnumLabelCallsites flags any CallExpr argument whose go/types type is
// a frozen executor label-enum AND is a compile-time constant that is not a
// valid frozen executor declared-const reference.
func scanSagaEnumLabelCallsites(p *Pass) []Diagnostic {
	info := p.TypesInfo
	if info == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			if isSagaEnumConversion(info, call) {
				return
			}
			for _, arg := range call.Args {
				typeName := sagaEnumConstViolation(info, arg)
				if typeName == "" {
					continue
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(arg.Pos()).Line,
					Message: "inline constant of saga label enum " + typeName +
						" reaches a metric label — pass a declared executor." + typeName +
						" const (or a classify() result), not a string literal or " +
						typeName + "(...) conversion (SAGA-METRIC-LABEL-VALUES-FROZEN-01 callsite guard)",
				})
			}
		})
	}
	return diags
}

// scanSagaEnumLabelAssignments flags variable declarations/assignments whose
// LHS has a frozen executor label-enum type and RHS is an inline constant.
func scanSagaEnumLabelAssignments(p *Pass) []Diagnostic { //nolint:gocognit,cyclop,lll // archtest: handles AssignStmt/ValueSpec/ReturnStmt for four executor enum types; inherent to SAGA-METRIC-LABEL-VALUES-FROZEN-01 A2
	info := p.TypesInfo
	if info == nil {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		rel := p.Rel(file)
		EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
			if gd.Tok != token.VAR {
				return
			}
			EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
				for i, val := range vs.Values {
					if i >= len(vs.Names) {
						continue
					}
					lhsTV, ok := info.Types[vs.Names[i]]
					if !ok {
						if vs.Type == nil {
							continue
						}
						lhsTV, ok = info.Types[vs.Type]
						if !ok {
							continue
						}
					}
					if sagaEnumTypeName(lhsTV.Type) == "" {
						continue
					}
					typeName := sagaEnumConstViolation(info, val)
					if typeName == "" {
						continue
					}
					diags = append(diags, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(val.Pos()).Line,
						Message: "variable of saga label enum " + typeName +
							" initialized from an inline constant — use a declared executor." +
							typeName + " const or a classify() result to avoid laundering " +
							"an arbitrary value into the frozen set (SAGA-METRIC-LABEL-VALUES-FROZEN-01 assignment guard)",
					})
				}
			})
		})
		EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
			for i, rhs := range as.Rhs {
				if i >= len(as.Lhs) {
					continue
				}
				lhsTV, ok := info.Types[as.Lhs[i]]
				if !ok {
					continue
				}
				if sagaEnumTypeName(lhsTV.Type) == "" {
					continue
				}
				typeName := sagaEnumConstViolation(info, rhs)
				if typeName == "" {
					continue
				}
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(rhs.Pos()).Line,
					Message: "variable of saga label enum " + typeName +
						" assigned from an inline constant — use a declared executor." +
						typeName + " const or a classify() result to avoid laundering " +
						"an arbitrary value into the frozen set (SAGA-METRIC-LABEL-VALUES-FROZEN-01 assignment guard)",
				})
			}
		})
	}
	return diags
}

// scanSagaEnumLabelAll runs both callsite and assignment guards on one Pass.
func scanSagaEnumLabelAll(p *Pass) []Diagnostic {
	return append(scanSagaEnumLabelCallsites(p), scanSagaEnumLabelAssignments(p)...)
}

// CheckSagaMetricLabelValuesFrozen is the importable form of
// SAGA-METRIC-LABEL-VALUES-FROZEN-01.
// Not registered in StandardCellRules.
func CheckSagaMetricLabelValuesFrozen(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg
	var all []Diagnostic
	Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		all = append(all, scanSagaEnumLabelAll(p)...)
		return nil
	})
	return all
}
