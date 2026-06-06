// invariants:
//   - INVARIANT: RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
//   - INVARIANT: RMQ-CHANNEL-MAX-PER-CONN-01
//   - INVARIANT: RMQ-PUBLISHER-FAILURE-HANDLING-01
//   - INVARIANT: RMQ-PUBLISHER-RELEASES-CHANNEL-01
//   - INVARIANT: RMQ-STOPINTAKE-INFLIGHT-WAIT-01
//
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
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// ---------------------------------------------------------------------------
// RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
// ---------------------------------------------------------------------------

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
	t.Parallel()

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
			checkFileForDirectChannelClose(t, p, file, chanIface, rel)
		}
		return nil
	})
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
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
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
			rt := info.TypeOf(sel.X)
			if rt == nil {
				return
			}
			if typesutil.ImplementsInterface(rt, chanIface) {
				flagged[funcName] = true
			}
		})
	})

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
	EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name.Name != "Config" {
			return
		}
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
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "setDefaults" && fd.Recv != nil {
			setDefaults = fd
		}
	})
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
	EachInSubtree[ast.IfStmt](setDefaults.Body, func(ifStmt *ast.IfStmt) {
		// Check if the condition is `<recv>.MaxChannelsPerConn <= 0`.
		bin, ok := ifStmt.Cond.(*ast.BinaryExpr)
		if !ok {
			return
		}
		if bin.Op != token.LEQ {
			return
		}
		// LHS must be a selector ending in MaxChannelsPerConn.
		lhsSel, ok := bin.X.(*ast.SelectorExpr)
		if !ok || lhsSel.Sel.Name != "MaxChannelsPerConn" {
			return
		}
		// RHS must be the literal 0.
		rhs, ok := bin.Y.(*ast.BasicLit)
		if !ok || rhs.Kind != token.INT || rhs.Value != "0" {
			return
		}
		// Found if MaxChannelsPerConn <= 0 — now verify body assigns default constant.
		conditionIsLEQ = true
		if !assigns {
			if _, ok := FindFirstInSubtree[ast.AssignStmt](ifStmt.Body, func(assign *ast.AssignStmt) bool {
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
			}); ok {
				assigns = true
			}
		}
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
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "AcquireChannel" && fd.Recv != nil {
			acquire = fd
		}
	})
	if acquire == nil {
		t.Fatalf("RMQ-CHANNEL-MAX-PER-CONN-01-C: AcquireChannel method not found in %s", src)
	}

	var refersToCounter bool
	EachInSubtree[ast.SelectorExpr](acquire.Body, func(sel *ast.SelectorExpr) {
		if sel.Sel.Name == "inUseChannels" {
			refersToCounter = true
		}
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
	EachInSubtree[ast.Ident](publish.Body, func(ident *ast.Ident) {
		if ident.Name == "ErrAdapterAMQPNack" {
			found = true
		}
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
	EachInSubtree[ast.CallExpr](publish.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name != "Warn" {
			return
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if ident.Name == "slog" {
			warnCount++
		}
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
	EachInSubtree[ast.CallExpr](publish.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name == "RecordPublishFailure" {
			calls++
		}
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

	violations := scanPublishMissingFailureRecord(publish, fset)
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

// TestRMQPublisherFailureHandling01_ContainerCoverage_Wave4_RED is a RED-step
// regression test (TDD per ai-robust.md) for PR445-FU finding F3.
//
// checkPublishStmtViolations only recurses into IfStmt / SelectStmt /
// BlockStmt. Error returns nested inside ForStmt / RangeStmt / SwitchStmt /
// TypeSwitchStmt or in nested SelectStmt arms are silently skipped, so a
// developer can introduce a publish failure path inside such a container
// without RecordPublishFailure and the rule won't notice.
//
// Each fixture under testdata/rmq_return_container_fixtures/ defines a
// minimal Publish method with an error-returning if-block embedded in one
// of the un-recursed containers. After Wave 4 extends the switch, every
// fixture must yield exactly one violation. Until then, this test fails.
func TestRMQPublisherFailureHandling01_ContainerCoverage_Wave4_RED(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "rmq_return_container_fixtures")

	cases := []struct {
		file string
		want int // expected number of violations after Wave 4
	}{
		{"for_body_return.go", 1},
		{"range_body_return.go", 1},
		{"switch_body_return.go", 1},
		{"type_switch_return.go", 1},
		{"select_case_nested_for_return.go", 1},
		{"nested_select_in_select.go", 1},
	}

	for _, c := range cases {
		c := c
		t.Run(c.file, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(base, c.file)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			publish := findMethod(f, "Publish")
			if publish == nil {
				t.Fatalf("Publish method not found in fixture %s", path)
			}
			got := scanPublishMissingFailureRecord(publish, fset)
			if len(got) != c.want {
				t.Errorf("%s: want %d violations (the error return inside the un-recursed container), got %d: %v",
					c.file, c.want, len(got), got)
			}
		})
	}
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
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if publishMethod == nil && fd.Recv != nil && fd.Name.Name == "Publish" {
			publishMethod = fd
		}
	})
	if publishMethod == nil {
		t.Fatalf("RMQ-PUBLISHER-RELEASES-CHANNEL-01: Publisher.Publish method not found in %s", src)
	}

	// Check that AcquireChannel is called in the method body.
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

	_, hasRelease := FindFirstInSubtree[ast.DeferStmt](publishMethod.Body, func(ds *ast.DeferStmt) bool {
		// Walk the entire defer statement subtree for the release selector.
		_, ok := FindFirstInSubtree[ast.SelectorExpr](ds, func(sel *ast.SelectorExpr) bool {
			return releaseSelectors[sel.Sel.Name]
		})
		return ok
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
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
		}
	})
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
	EachInSubtree[ast.CallExpr](stopIntake.Body, func(call *ast.CallExpr) {
		// Bare identifier call form, e.g. `waitInflightDrain(...)`.
		if id, ok := call.Fun.(*ast.Ident); ok {
			if id.Name == "waitInflightDrain" {
				found = true
			}
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		switch sel.Sel.Name {
		case "Wait":
			// Accept any selector ending in localWg.Wait or inflightWg.Wait.
			inner, ok := sel.X.(*ast.SelectorExpr)
			if !ok {
				return
			}
			if inner.Sel.Name == "localWg" || inner.Sel.Name == "inflightWg" {
				found = true
			}
		case "waitInflight", "waitDrained", "wgDone", "inflightCount":
			found = true
		}
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
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "drainRemaining" && fd.Recv != nil {
			drain = fd
		}
	})
	if drain == nil {
		t.Fatalf("RMQ-STOPINTAKE-INFLIGHT-WAIT-01-B: drainRemaining method not found in %s", src)
	}

	// Reject any select case clause containing `<-ctx.Done()`. Drain must run on
	// a detached context (context.WithoutCancel) so a parent ctx cancel does not
	// silently drop prefetched-but-unacked deliveries.
	var violations []token.Pos
	EachInSubtree[ast.CommClause](drain.Body, func(comm *ast.CommClause) {
		// CommClause.Comm is one of: SendStmt, AssignStmt, ExprStmt (for receive-only).
		// The "case <-ctx.Done():" appears as ExprStmt with UnaryExpr Op=ARROW
		// and X=CallExpr(ctx.Done).
		expr, ok := comm.Comm.(*ast.ExprStmt)
		if !ok {
			return
		}
		unary, ok := expr.X.(*ast.UnaryExpr)
		if !ok || unary.Op != token.ARROW {
			return
		}
		call, ok := unary.X.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if ident.Name == "ctx" && sel.Sel.Name == "Done" {
			violations = append(violations, comm.Pos())
		}
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

	_, found := FindFirstInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) bool {
		if fd.Recv == nil || fd.Body == nil {
			return false
		}
		if fd.Name.Name != "drainRemaining" && fd.Name.Name != "consumeLoop" {
			return false
		}
		return bodyHasWithoutCancel(fd.Body)
	})

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
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "StopIntake" && fd.Recv != nil {
			stopIntake = fd
		}
	})
	if stopIntake == nil {
		t.Fatalf("StopIntake method not found in %s", src)
	}

	rel, _ := filepath.Rel(root, src)
	if rel == "" {
		rel = src
	}
	EachInSubtree[ast.CallExpr](stopIntake.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Wait" {
			return
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if inner.Sel.Name != "localWg" {
			return
		}
		t.Errorf(
			"RMQ-STOPINTAKE-INFLIGHT-WAIT-01: %s:%s — StopIntake body must not call "+
				"localWg.Wait(); poll inflightCount() instead. drainRemaining "+
				"concurrently calls localWg.Add(1) on every prefetched delivery, "+
				"and Wait racing that Add panics with "+
				"\"WaitGroup misuse: Add called concurrently with Wait\".",
			rel, fset.Position(call.Pos()),
		)
	})
}
