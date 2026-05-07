// Package archtest — RabbitMQ adapter invariants.
//
// Merged from:
//   - rmq_channel_destruction_test.go      (RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01)
//   - rmq_channel_max_per_conn_test.go     (RMQ-CHANNEL-MAX-PER-CONN-01)
//   - rmq_publisher_failure_handling_test.go (RMQ-PUBLISHER-FAILURE-HANDLING-01)
//   - rmq_publisher_releases_channel_test.go (RMQ-PUBLISHER-RELEASES-CHANNEL-01)
//   - rmq_stopintake_inflight_wait_test.go  (RMQ-STOPINTAKE-INFLIGHT-WAIT-01)
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

	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

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

// INVARIANT: RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
//
// TestRMQChannelDestructionViaConn01 enforces RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01:
// every AMQPChannel destruction site in adapters/rabbitmq/ MUST go through
// Connection.CloseEphemeralChannel.
//
// Direct ch.Close() calls outside of CloseEphemeralChannel or waitAndClose
// bypass the inUseChannels.Add(-1) decrement and permanently leak
// MaxChannelsPerConn slots, causing spurious ERR_ADAPTER_AMQP_CHANNEL_MAX_EXCEEDED
// false-positives after enough reconnect cycles or subscription teardowns.
//
// Implementation: go/types-backed receiver classification. The receiver of
// every `Close()` call is resolved via packages.Package.TypesInfo.TypeOf, then
// matched against the AMQPChannel interface declared in the same package via
// types.Implements. This is naming-immune: renaming `ch` to `channel` or
// shuffling field names does not change the verdict.
//
// ref: golang/tools go/analysis/passes/copylock — types.Implements idiom
// ref: golang/tools go/analysis/passes/lostcancel — TypesInfo.TypeOf pipeline
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN P1
// ref: adapters/rabbitmq/doc.go — AMQPChannel destruction contract
func TestRMQChannelDestructionViaConn01(t *testing.T) {
	if testing.Short() {
		t.Skip("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: skipping packages.Load-based archtest in -short mode")
	}
	t.Parallel()

	root := findModuleRoot(t)

	resolver, err := typeseval.SharedResolver(root, false, nil, "github.com/ghbvf/gocell/adapters/rabbitmq")
	if err != nil {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: load adapters/rabbitmq: %v", err)
	}
	pkg := pickPackage(t, resolver.Packages(), "github.com/ghbvf/gocell/adapters/rabbitmq")

	chanIface := lookupInterfaceType(t, pkg, "AMQPChannel")

	for i, file := range pkg.Syntax {
		path := pkg.GoFiles[i]
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		checkFileForDirectChannelClose(t, pkg.Fset, file, pkg.TypesInfo, chanIface, path)
	}
}

// pickPackage returns the loaded package whose import path matches `want`.
// Tests-disabled SharedResolver still returns a slice; the typed package
// lives at the requested path.
func pickPackage(t *testing.T, pkgs []*packages.Package, want string) *packages.Package {
	t.Helper()
	for _, p := range pkgs {
		if p.PkgPath == want {
			if p.TypesInfo == nil || p.Types == nil {
				t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: package %s has no types info", want)
			}
			return p
		}
	}
	t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: package %s not loaded", want)
	return nil
}

// lookupInterfaceType resolves a top-level interface declaration by name and
// returns its types.Interface. Fail-closed when the type vanishes or is no
// longer an interface.
func lookupInterfaceType(t *testing.T, pkg *packages.Package, name string) *types.Interface {
	t.Helper()
	obj := pkg.Types.Scope().Lookup(name)
	if obj == nil {
		t.Fatalf("RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s not declared in %s", name, pkg.PkgPath)
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

// checkFileForDirectChannelClose walks every function in f and flags any
// `Close()` call whose receiver type implements AMQPChannel — regardless of
// the receiver variable's name. AMQPConnection close calls slip through
// because AMQPConnection's method set (4 methods) is a strict subset of
// AMQPChannel's (16 methods), so types.Implements rejects it.
func checkFileForDirectChannelClose(
	t *testing.T,
	fset *token.FileSet,
	f *ast.File,
	info *types.Info,
	chanIface *types.Interface,
	src string,
) {
	t.Helper()

	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		funcName := fd.Name.Name
		if allowedChannelCloseFuncs[funcName] {
			continue
		}

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Close" {
				return true
			}
			recvType := info.TypeOf(sel.X)
			if recvType == nil {
				return true
			}
			if !implementsAMQPChannel(recvType, chanIface) {
				return true
			}

			pos := fset.Position(call.Pos())
			rel, _ := filepath.Rel(filepath.Dir(filepath.Dir(src)), src)
			if rel == "" {
				rel = src
			}
			receiverHint := receiverHint(sel.X)
			t.Errorf(
				"RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01: %s:%d: %s() contains direct %s.Close() call (receiver type %s implements AMQPChannel).\n"+
					"  All AMQPChannel destruction MUST go through Connection.CloseEphemeralChannel\n"+
					"  to keep inUseChannels in sync with MaxChannelsPerConn.\n"+
					"  Replace: %s.Close() → conn.CloseEphemeralChannel(%s)",
				rel, pos.Line, funcName, receiverHint, recvType.String(), receiverHint, receiverHint,
			)
			return true
		})
	}
}

// implementsAMQPChannel reports whether t (value or pointer) satisfies the
// AMQPChannel interface. We try both the type itself and a pointer to it,
// matching how `types.Implements` is used in golang/tools copylock.go: a
// type satisfies an interface either directly or via its pointer receiver.
func implementsAMQPChannel(t types.Type, iface *types.Interface) bool {
	if types.Implements(t, iface) {
		return true
	}
	if _, isPtr := t.(*types.Pointer); !isPtr {
		return types.Implements(types.NewPointer(t), iface)
	}
	return false
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

// TestRMQChannelDestructionViaConn01_NamingImmunity is a positive/negative
// fixture pair proving the RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01 type filter
// is naming-immune: violations are flagged based on receiver type implementing
// AMQPChannel, never the receiver variable name. Co-located with the rule so
// renaming the heuristic-era var "ch" downstream cannot regress this gate.
//
// Built on go/types directly (types.Config.Check on an inline AST) so we do
// not need an external fixture module — the test stays self-contained.
func TestRMQChannelDestructionViaConn01_NamingImmunity(t *testing.T) {
	t.Parallel()

	src := `package fixture
type AMQPChannel interface {
	Publish() error
	Consume() error
	Close() error
}
type IOCloser interface {
	Close() error
}
func violatorRenamedVar() {
	var renamed AMQPChannel
	renamed.Close()
}
func violatorShortVar() {
	var ch AMQPChannel
	ch.Close()
}
func violatorAmqpCh() {
	var amqpCh AMQPChannel
	amqpCh.Close()
}
func innocentIOCloser() {
	var ch IOCloser
	ch.Close()
}
func CloseEphemeralChannel() {
	var c AMQPChannel
	c.Close()
}
func waitAndClose() {
	var c AMQPChannel
	c.Close()
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	conf := types.Config{}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	pkg, err := conf.Check("fixture", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("type-check fixture: %v", err)
	}
	chanIface := pkg.Scope().Lookup("AMQPChannel").Type().Underlying().(*types.Interface)

	// Capture errors via a sub-t so we can read them. We use t.Run with a
	// recording test impl pattern: simpler to call the inspection directly
	// and aggregate flagged function names locally.
	flagged := map[string]bool{}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		funcName := fd.Name.Name
		if allowedChannelCloseFuncs[funcName] {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Close" {
				return true
			}
			rt := info.TypeOf(sel.X)
			if rt == nil {
				return true
			}
			if implementsAMQPChannel(rt, chanIface) {
				flagged[funcName] = true
			}
			return true
		})
	}

	wantFlagged := map[string]bool{
		"violatorRenamedVar": true,
		"violatorShortVar":   true,
		"violatorAmqpCh":     true,
	}
	wantInnocent := []string{"innocentIOCloser"}

	for name := range wantFlagged {
		if !flagged[name] {
			t.Errorf("expected %s to be flagged (renaming receiver must not weaken gate)", name)
		}
	}
	for _, name := range wantInnocent {
		if flagged[name] {
			t.Errorf("did not expect %s to be flagged (io.Closer-shaped types implement Close but not AMQPChannel)", name)
		}
	}
	// Whitelisted owners must not appear in flagged map at all.
	for _, owner := range []string{"CloseEphemeralChannel", "waitAndClose"} {
		if flagged[owner] {
			t.Errorf("expected whitelisted %s to be skipped at function-name layer", owner)
		}
	}
}

// ---------------------------------------------------------------------------
// RMQ-CHANNEL-MAX-PER-CONN-01
// ---------------------------------------------------------------------------

const expectedDefaultMaxChannelsPerConnConst = "defaultRMQMaxChannelsPerConn"

// INVARIANT: RMQ-CHANNEL-MAX-PER-CONN-01-A
//
// TestRMQChannelMaxPerConn01_ConfigFieldExists enforces
// RMQ-CHANNEL-MAX-PER-CONN-01-A: Config struct must declare MaxChannelsPerConn int.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: rabbitmq/amqp091-go connection.go openTune — broker channel_max negotiation
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

// INVARIANT: RMQ-CHANNEL-MAX-PER-CONN-01-B
//
// TestRMQChannelMaxPerConn01_SetDefaultsPopulatesField enforces
// RMQ-CHANNEL-MAX-PER-CONN-01-B: setDefaults must populate MaxChannelsPerConn
// with the documented default constant (defaultRMQMaxChannelsPerConn = 256).
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

// INVARIANT: RMQ-CHANNEL-MAX-PER-CONN-01-C
//
// TestRMQChannelMaxPerConn01_AcquireChannelGuardsCounter enforces
// RMQ-CHANNEL-MAX-PER-CONN-01-C: AcquireChannel must reference an inUseChannels
// counter (the atomic guard that returns ErrAdapterAMQPChannelMaxExceeded when
// the cap is reached).
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

// ---------------------------------------------------------------------------
// RMQ-PUBLISHER-FAILURE-HANDLING-01
// ---------------------------------------------------------------------------

// INVARIANT: RMQ-PUBLISHER-FAILURE-HANDLING-01-A
//
// TestRMQPublisherFailureHandling01_NackErrcodeReferenced enforces
// RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish must reference ErrAdapterAMQPNack
// somewhere in its body (NACK errcode is distinct from ErrAdapterAMQPConfirmTimeout).
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: ThreeDotsLabs/watermill-amqp publisher.go — NACK returns hard error
func TestRMQPublisherFailureHandling01_NackErrcodeReferenced(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish method not found in %s", src)
	}

	var found bool
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "ErrAdapterAMQPNack" {
			found = true
			return false
		}
		return true
	})

	if !found {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-A: Publish in %s must reference "+
				"ErrAdapterAMQPNack to mark broker-NACK as a distinct error code (vs "+
				"ErrAdapterAMQPConfirmTimeout). Sharing a code makes alerting rules "+
				"unable to tell broker rejection from network timeout.",
			rel,
		)
	}
}

// INVARIANT: RMQ-PUBLISHER-FAILURE-HANDLING-01-B
//
// TestRMQPublisherFailureHandling01_AllBranchesEmitWarn enforces
// RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish must call slog.Warn at least 3 times
// (one for each of NACK / timeout / confirmCh closed).
func TestRMQPublisherFailureHandling01_AllBranchesEmitWarn(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish method not found in %s", src)
	}

	const requiredWarnCalls = 3
	var warnCount int
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Warn" {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "slog" {
			warnCount++
		}
		return true
	})

	if warnCount < requiredWarnCalls {
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-B: Publish in %s must call slog.Warn at "+
				"least %d times (NACK / confirm timeout / confirm-channel-closed); "+
				"found %d. Silent failure branches make on-call diagnosis impossible.",
			src, requiredWarnCalls, warnCount,
		)
	}
}

// INVARIANT: RMQ-PUBLISHER-FAILURE-HANDLING-01-C
//
// TestRMQPublisherFailureHandling01_RecordsFailureMetric enforces
// RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish must call a publisher-collector
// RecordPublishFailure method at least once so a metric records the failure reason.
func TestRMQPublisherFailureHandling01_RecordsFailureMetric(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish method not found in %s", src)
	}

	var calls int
	ast.Inspect(publish.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "RecordPublishFailure" {
			calls++
		}
		return true
	})

	if calls < 1 {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-C: Publish in %s must call "+
				"RecordPublishFailure on the injected PublisherCollector so the failure "+
				"reason is queryable as a metric. Defaulting to NoopPublisherCollector "+
				"keeps the call cheap; production wiring injects the provider-backed "+
				"collector at the composition root.",
			rel,
		)
	}
}

// INVARIANT: RMQ-PUBLISHER-FAILURE-HANDLING-01-D
//
// TestRMQPublisherFailureHandling01_AllReturnsMustRecord verifies that every
// non-success, non-exempt return in Publish is preceded in its enclosing block
// by a RecordPublishFailure call.
//
// Exemptions (not required to record):
//   - The final success `return nil` (no error, no metric needed)
//   - Any return inside a `ctx.Done()` select case (caller-initiated cancel,
//     documented as not a wire-level failure)
//   - The early "publisher is closed" return (precedes wg.Add; not a wire failure)
//
// This prevents a future developer from adding a new failure branch and
// forgetting to record the failure metric — a regression that would create a
// silent gap in the alerting coverage.
func TestRMQPublisherFailureHandling01_AllReturnsMustRecord(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatalf("RMQ-PUBLISHER-FAILURE-HANDLING-01-D: Publish method not found in %s", src)
	}

	// Walk all if-blocks and select-cases in Publish. For each error-returning
	// block (contains at least one non-nil return), verify it also contains a
	// RecordPublishFailure call somewhere in that block.
	// ctx.Done() cases and nil returns are exempt.
	var violations []string

	var checkIfBlock func(ifStmt *ast.IfStmt, inCtxDone bool)

	var checkStmt func(stmt ast.Stmt, inCtxDone bool)
	checkStmt = func(stmt ast.Stmt, inCtxDone bool) {
		switch s := stmt.(type) {
		case *ast.IfStmt:
			checkIfBlock(s, inCtxDone)
		case *ast.SelectStmt:
			for _, c := range s.Body.List {
				if comm, ok := c.(*ast.CommClause); ok {
					isCtxDone := inCtxDone || isCtxDoneCase(comm)
					for _, cs := range comm.Body {
						checkStmt(cs, isCtxDone)
					}
				}
			}
		case *ast.BlockStmt:
			for _, inner := range s.List {
				checkStmt(inner, inCtxDone)
			}
		}
	}

	checkIfBlock = func(ifStmt *ast.IfStmt, inCtxDone bool) {
		if ifStmt == nil || ifStmt.Body == nil {
			return
		}
		body := ifStmt.Body.List

		// Does this if-block contain a non-nil return?
		hasNonNilReturn := false
		for _, s := range body {
			if ret, ok := s.(*ast.ReturnStmt); ok && !isNilReturn(ret) {
				hasNonNilReturn = true
				break
			}
		}

		// Exempt: if-block guarding the "publisher is closed" early exit.
		// This is not a wire-level failure, so no metric is required.
		// Detected by checking if the condition references a field/method named "closed".
		isClosedGuard := ifCondRefersTo(ifStmt.Cond, "closed")

		if hasNonNilReturn && !inCtxDone && !isClosedGuard {
			// Does this block contain a RecordPublishFailure call?
			hasRecord := blockContainsRecordPublishFailure(body)
			if !hasRecord {
				// Find the first non-nil return for error reporting.
				for _, s := range body {
					if ret, ok := s.(*ast.ReturnStmt); ok && !isNilReturn(ret) {
						pos := fset.Position(ret.Pos())
						violations = append(violations,
							fmt.Sprintf("line %d: if-block with error return has no RecordPublishFailure", pos.Line))
						break
					}
				}
			}
		}

		// Recurse into nested if/select within this block.
		for _, inner := range body {
			checkStmt(inner, inCtxDone)
		}

		// Check else branch.
		if ifStmt.Else != nil {
			checkStmt(ifStmt.Else, inCtxDone)
		}
	}

	// Walk top-level statements of Publish body to find if-blocks.
	for _, stmt := range publish.Body.List {
		checkStmt(stmt, false)
	}

	for _, v := range violations {
		t.Errorf(
			"RMQ-PUBLISHER-FAILURE-HANDLING-01-D: Publish in %s: %s. "+
				"All error-returning if-blocks must contain collector.RecordPublishFailure "+
				"so alerting rules can observe the failure reason without log-parsing. "+
				"Exemptions: success `return nil` and returns inside ctx.Done() case.",
			src, v,
		)
	}
}

// isCtxDoneCase returns true if the CommClause is a `case <-ctx.Done():` arm.
func isCtxDoneCase(cc *ast.CommClause) bool {
	if cc.Comm == nil {
		return false
	}
	// Looking for: case <-ctx.Done():
	// Which is an ExprStmt containing a UnaryExpr (<-) of a CallExpr (ctx.Done()).
	recv, ok := cc.Comm.(*ast.ExprStmt)
	if !ok {
		return false
	}
	unary, ok := recv.X.(*ast.UnaryExpr)
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
	ast.Inspect(cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if id.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// blockContainsRecordPublishFailure returns true if any statement in stmts
// (at any nesting level) is a call to RecordPublishFailure.
func blockContainsRecordPublishFailure(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		var found bool
		ast.Inspect(s, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "RecordPublishFailure" {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// findMethod returns the FuncDecl for a method whose name matches `name` and
// which has a non-nil receiver. Returns nil if not found.
//
//nolint:unparam // name is "Publish" in all callers; kept as param for readability
func findMethod(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == name && fd.Recv != nil {
			return fd
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// RMQ-PUBLISHER-RELEASES-CHANNEL-01
// ---------------------------------------------------------------------------

// INVARIANT: RMQ-PUBLISHER-RELEASES-CHANNEL-01
//
// TestRMQPublisherReleasesChannel01 verifies that Publisher.Publish acquires a
// channel and pairs it with a defer that calls either
// p.conn.CloseEphemeralChannel or p.conn.ReleaseChannel.
//
// Without this pairing, each Publish increments inUseChannels but never
// rolls it back; after MaxChannelsPerConn (default 256) calls all
// subsequent publishes fail with ErrAdapterAMQPChannelMaxExceeded.
//
// AST strategy:
//  1. Parse adapters/rabbitmq/publisher.go.
//  2. Find the Publish method on *Publisher.
//  3. Verify that the method body contains an AcquireChannel call site.
//  4. Verify that the method body contains at least one defer statement whose
//     call expression is p.conn.CloseEphemeralChannel or p.conn.ReleaseChannel.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: adapters/rabbitmq/connection.go CloseEphemeralChannel
func TestRMQPublisherReleasesChannel01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "publisher.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("RMQ-PUBLISHER-RELEASES-CHANNEL-01: parse %s: %v", src, err)
	}

	// Locate Publisher.Publish method.
	var publishMethod *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name.Name != "Publish" {
			continue
		}
		publishMethod = fd
		break
	}
	if publishMethod == nil {
		t.Fatalf("RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish method not found in %s", src)
	}

	// Check that AcquireChannel is called in the method body.
	var hasAcquire bool
	ast.Inspect(publishMethod.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "AcquireChannel" {
			hasAcquire = true
			return false
		}
		return true
	})
	if !hasAcquire {
		t.Errorf(
			"RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish must call " +
				"conn.AcquireChannel to obtain a channel for confirm-mode publish.",
		)
	}

	// Check that there is a defer calling CloseEphemeralChannel or ReleaseChannel.
	//
	// We accept two forms:
	//   defer p.conn.CloseEphemeralChannel(ch)   — direct call expr
	//   defer func() { ... p.conn.CloseEphemeralChannel(ch) ... }()  — closure
	//
	// The AST check inspects all DeferStmt nodes in the method body for a
	// selector expression whose name is CloseEphemeralChannel or ReleaseChannel.
	releaseSelectors := map[string]bool{
		"CloseEphemeralChannel": true,
		"ReleaseChannel":        true,
	}

	var hasRelease bool
	ast.Inspect(publishMethod.Body, func(n ast.Node) bool {
		ds, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		// Walk the entire defer statement subtree for the release selector.
		ast.Inspect(ds, func(inner ast.Node) bool {
			sel, ok := inner.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if releaseSelectors[sel.Sel.Name] {
				hasRelease = true
				return false
			}
			return true
		})
		return !hasRelease
	})

	if !hasRelease {
		t.Errorf(
			"RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish must pair " +
				"AcquireChannel with a deferred p.conn.CloseEphemeralChannel " +
				"(or p.conn.ReleaseChannel) call. Without this pairing every Publish " +
				"leaks one inUseChannels slot; after MaxChannelsPerConn (=256) " +
				"publishes all subsequent calls fail with " +
				"ErrAdapterAMQPChannelMaxExceeded.",
		)
	}
}

// ---------------------------------------------------------------------------
// RMQ-STOPINTAKE-INFLIGHT-WAIT-01
// ---------------------------------------------------------------------------

// INVARIANT: RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A
//
// TestRMQStopIntakeInflightWait01_StopIntakeWaitsForInflight enforces
// RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A: Subscriber.StopIntake must wait for in-flight
// processDelivery goroutines to settle before returning, so callers that follow
// StopIntake with Close() do not race with active broker ack/nack work.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN
// ref: ThreeDotsLabs/watermill subscriber.Close — wg.Wait inside close path
// ref: rabbitmq/amqp091-go channel.go — Cancel→drain→wg.Wait→ch.Close ordering
func TestRMQStopIntakeInflightWait01_StopIntakeWaitsForInflight(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var stopIntake *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
			break
		}
	}
	if stopIntake == nil {
		t.Fatalf("RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A: StopIntake method not found in %s", src)
	}

	// Look for an inflight-wait sentinel in the function body. Accept any of:
	//   - a call ending in `.localWg.Wait()` / `.inflightWg.Wait()` (waitgroup style)
	//   - a call to `run.waitInflight(...)` / `r.waitInflight(...)` helper
	//   - a call to a wgDone() helper on a subscriptionRun
	//   - a call to `inflightCount()` / `r.inflightCount()` (atomic-poll style)
	//   - a call to the package-level `waitInflightDrain(...)` helper
	//
	// The atomic-poll style is the canonical implementation today: it avoids
	// the Add-after-Wait race that direct localWg.Wait suffers when
	// drainRemaining concurrently calls registerDelivery (= Add(1)). The Wait
	// helpers are kept in the accepted set so that future refactors that
	// re-introduce a wait-style API (e.g. behind a sync.Cond) still satisfy
	// the gate without needing to update this test.
	var found bool
	ast.Inspect(stopIntake.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Bare identifier call form, e.g. `waitInflightDrain(...)`.
		if id, ok := call.Fun.(*ast.Ident); ok {
			if id.Name == "waitInflightDrain" {
				found = true
				return false
			}
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Wait":
			// Accept any selector ending in localWg.Wait or inflightWg.Wait.
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if inner.Sel.Name == "localWg" || inner.Sel.Name == "inflightWg" {
				found = true
				return false
			}
		case "waitInflight", "waitDrained", "wgDone", "inflightCount":
			found = true
			return false
		}
		return true
	})

	if !found {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-A: StopIntake in %s must wait for in-flight "+
				"processDelivery goroutines (run.localWg.Wait / run.waitInflight / run.wgDone) "+
				"before returning, otherwise Close() can race with active broker I/O.",
			rel,
		)
	}
}

// INVARIANT: RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B (no parent ctx.Done)
//
// TestRMQStopIntakeInflightWait01_DrainNoParentCtxDone enforces
// RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining body must NOT contain a
// bare `case <-ctx.Done()` arm; drain runs on a detached context bounded by
// currentDrainDeadline so prefetch is fully drained even if the parent ctx
// is canceled mid-shutdown.
func TestRMQStopIntakeInflightWait01_DrainNoParentCtxDone(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var drain *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "drainRemaining" && fd.Recv != nil {
			drain = fd
			break
		}
	}
	if drain == nil {
		t.Fatalf("RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining method not found in %s", src)
	}

	// Reject any select case clause containing `<-ctx.Done()`. Drain must run on
	// a detached context (context.WithoutCancel) so a parent ctx cancel does not
	// silently drop prefetched-but-unacked deliveries.
	var violations []token.Pos
	ast.Inspect(drain.Body, func(n ast.Node) bool {
		comm, ok := n.(*ast.CommClause)
		if !ok {
			return true
		}
		// CommClause.Comm is one of: SendStmt, AssignStmt, ExprStmt (for receive-only).
		// The "case <-ctx.Done():" appears as ExprStmt with UnaryExpr Op=ARROW
		// and X=CallExpr(ctx.Done).
		expr, ok := comm.Comm.(*ast.ExprStmt)
		if !ok {
			return true
		}
		unary, ok := expr.X.(*ast.UnaryExpr)
		if !ok || unary.Op != token.ARROW {
			return true
		}
		call, ok := unary.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "ctx" && sel.Sel.Name == "Done" {
			violations = append(violations, comm.Pos())
		}
		return true
	})

	for _, p := range violations {
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining at %s contains `case <-ctx.Done()`; "+
				"drain MUST run on a detached context (context.WithoutCancel) bounded by "+
				"currentDrainDeadline timer, otherwise parent ctx cancel drops prefetched messages.",
			fset.Position(p),
		)
	}
}

// INVARIANT: RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B (detached context cross-check)
//
// TestRMQStopIntakeInflightWait01_DrainUsesDetachedContext verifies that
// drainRemaining (or consumeLoop) creates a detached context via
// context.WithoutCancel so the test above cannot be satisfied by simply
// removing the ctx.Done arm while still passing the parent ctx unchanged.
func TestRMQStopIntakeInflightWait01_DrainUsesDetachedContext(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	bodyHasWithoutCancel := func(body *ast.BlockStmt) bool {
		var found bool
		ast.Inspect(body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if ident.Name == "context" && sel.Sel.Name == "WithoutCancel" {
				found = true
				return false
			}
			return true
		})
		return found
	}

	var found bool
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil {
			continue
		}
		if fd.Name.Name != "drainRemaining" && fd.Name.Name != "consumeLoop" {
			continue
		}
		if bodyHasWithoutCancel(fd.Body) {
			found = true
			break
		}
	}

	if !found {
		rel, _ := filepath.Rel(root, src)
		if rel == "" {
			rel = src
		}
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining or consumeLoop in %s must "+
				"use `context.WithoutCancel` to derive the drain ctx, so prefetched "+
				"deliveries are processed independently of the parent ctx cancel.",
			strings.TrimPrefix(rel, "./"),
		)
	}
}

// INVARIANT: RMQ-STOPINTAKE-INFLIGHT-WAIT-01 (negative: no localWg.Wait)
//
// TestRMQStopIntakeInflightWait01_StopIntakeAvoidsLocalWgWait reinforces 01-A
// by inverting the assertion: StopIntake's body must NOT contain a textual
// `localWg.Wait()` call. The Add-after-Wait race is fundamentally caused by
// invoking Wait while drainRemaining can still register new deliveries; the
// only correct shape today is to poll inflightCount(). 01-A already accepts
// inflightCount, but a future refactor that adds a Wait alongside the poll
// would silently re-introduce the race without tripping 01-A. This negative
// test closes that loophole.
func TestRMQStopIntakeInflightWait01_StopIntakeAvoidsLocalWgWait(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "adapters", "rabbitmq", "subscriber.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var stopIntake *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
			break
		}
	}
	if stopIntake == nil {
		t.Fatalf("StopIntake method not found in %s", src)
	}

	rel, _ := filepath.Rel(root, src)
	if rel == "" {
		rel = src
	}
	ast.Inspect(stopIntake.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return true
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if inner.Sel.Name != "localWg" {
			return true
		}
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01: %s:%s — StopIntake body must not call "+
				"localWg.Wait(); poll inflightCount() instead. drainRemaining "+
				"concurrently calls localWg.Add(1) on every prefetched delivery, "+
				"and Wait racing that Add panics with "+
				"\"WaitGroup misuse: Add called concurrently with Wait\".",
			rel, fset.Position(call.Pos()),
		)
		return true
	})
}
