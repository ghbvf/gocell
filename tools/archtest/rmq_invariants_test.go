//go:build archtest

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
	"testing"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// ---------------------------------------------------------------------------
// RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01
// ---------------------------------------------------------------------------

// TestRMQChannelDestructionViaConn01 dogfoods CheckRMQChannelDestructionViaConn.
func TestRMQChannelDestructionViaConn01(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-CHANNEL-DESTRUCTION-VIA-CONN-01", CheckRMQChannelDestructionViaConn(t, ConfigForExternalCell{}))
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

// TestRMQChannelMaxPerConn01_ConfigFieldExists dogfoods CheckRMQChannelMaxPerConn (sub-rule A).
func TestRMQChannelMaxPerConn01_ConfigFieldExists(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-CHANNEL-MAX-PER-CONN-01", CheckRMQChannelMaxPerConn(t, ConfigForExternalCell{}))
}

// TestRMQChannelMaxPerConn01_SetDefaultsPopulatesField dogfoods CheckRMQChannelMaxPerConn (sub-rule B).
func TestRMQChannelMaxPerConn01_SetDefaultsPopulatesField(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-CHANNEL-MAX-PER-CONN-01", CheckRMQChannelMaxPerConn(t, ConfigForExternalCell{}))
}

// TestRMQChannelMaxPerConn01_AcquireChannelGuardsCounter dogfoods CheckRMQChannelMaxPerConn (sub-rule C).
func TestRMQChannelMaxPerConn01_AcquireChannelGuardsCounter(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-CHANNEL-MAX-PER-CONN-01", CheckRMQChannelMaxPerConn(t, ConfigForExternalCell{}))
}

// ---------------------------------------------------------------------------
// RMQ-PUBLISHER-FAILURE-HANDLING-01
// ---------------------------------------------------------------------------

// TestRMQPublisherFailureHandling01_NackErrcodeReferenced dogfoods CheckRMQPublisherFailureHandling (sub-rule A).
func TestRMQPublisherFailureHandling01_NackErrcodeReferenced(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-PUBLISHER-FAILURE-HANDLING-01", CheckRMQPublisherFailureHandling(t, ConfigForExternalCell{}))
}

// TestRMQPublisherFailureHandling01_AllBranchesEmitWarn dogfoods CheckRMQPublisherFailureHandling (sub-rule B).
func TestRMQPublisherFailureHandling01_AllBranchesEmitWarn(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-PUBLISHER-FAILURE-HANDLING-01", CheckRMQPublisherFailureHandling(t, ConfigForExternalCell{}))
}

// TestRMQPublisherFailureHandling01_RecordsFailureMetric dogfoods CheckRMQPublisherFailureHandling (sub-rule C).
func TestRMQPublisherFailureHandling01_RecordsFailureMetric(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-PUBLISHER-FAILURE-HANDLING-01", CheckRMQPublisherFailureHandling(t, ConfigForExternalCell{}))
}

// TestRMQPublisherFailureHandling01_AllReturnsMustRecord dogfoods CheckRMQPublisherFailureHandling (sub-rule D).
func TestRMQPublisherFailureHandling01_AllReturnsMustRecord(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-PUBLISHER-FAILURE-HANDLING-01", CheckRMQPublisherFailureHandling(t, ConfigForExternalCell{}))
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

// TestRMQPublisherReleasesChannel01 dogfoods CheckRMQPublisherReleasesChannel.
func TestRMQPublisherReleasesChannel01(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-PUBLISHER-RELEASES-CHANNEL-01", CheckRMQPublisherReleasesChannel(t, ConfigForExternalCell{}))
}

// ---------------------------------------------------------------------------
// RMQ-STOPINTAKE-INFLIGHT-WAIT-01
// ---------------------------------------------------------------------------

// TestRMQStopIntakeInflightWait01_StopIntakeWaitsForInflight dogfoods CheckRMQStopIntakeInflightWait (sub-rule A).
func TestRMQStopIntakeInflightWait01_StopIntakeWaitsForInflight(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-STOPINTAKE-INFLIGHT-WAIT-01", CheckRMQStopIntakeInflightWait(t, ConfigForExternalCell{}))
}

// TestRMQStopIntakeInflightWait01_DrainNoParentCtxDone dogfoods CheckRMQStopIntakeInflightWait (sub-rule B, no ctx.Done).
func TestRMQStopIntakeInflightWait01_DrainNoParentCtxDone(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-STOPINTAKE-INFLIGHT-WAIT-01", CheckRMQStopIntakeInflightWait(t, ConfigForExternalCell{}))
}

// TestRMQStopIntakeInflightWait01_DrainUsesDetachedContext dogfoods CheckRMQStopIntakeInflightWait (sub-rule B, detached ctx).
func TestRMQStopIntakeInflightWait01_DrainUsesDetachedContext(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-STOPINTAKE-INFLIGHT-WAIT-01", CheckRMQStopIntakeInflightWait(t, ConfigForExternalCell{}))
}

// TestRMQStopIntakeInflightWait01_StopIntakeAvoidsLocalWgWait dogfoods CheckRMQStopIntakeInflightWait (negative: no localWg.Wait).
func TestRMQStopIntakeInflightWait01_StopIntakeAvoidsLocalWgWait(t *testing.T) {
	t.Parallel()
	Report(t, "RMQ-STOPINTAKE-INFLIGHT-WAIT-01", CheckRMQStopIntakeInflightWait(t, ConfigForExternalCell{}))
}

// ---------------------------------------------------------------------------
// Diagnostic-location reverse self-check (F1)
// ---------------------------------------------------------------------------

// TestRMQDiagnostics_StructuralAbsenceLocated is the F1 reverse self-check
// (ai-robust 强制反向自检). The structural-absence and violation branches of the
// RMQ checks are never reached by the GREEN dogfood (production satisfies every
// rule), so a regression that drops Diagnostic.Rel/Line — degrading Report to
// ":0:" — would otherwise pass CI undetected. It drives each sub-check with a
// minimal violating in-memory source and asserts every emitted Diagnostic
// carries a clickable Rel and a 1-based Line (and that the violation actually
// fires — fail-closed, not vacuous).
func TestRMQDiagnostics_StructuralAbsenceLocated(t *testing.T) {
	t.Parallel()

	const rel = "adapters/rabbitmq/fixture.go"
	parse := func(src string) (*ast.File, *token.FileSet) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		return f, fset
	}
	assertLocated := func(name string, diags []Diagnostic) {
		if len(diags) == 0 {
			t.Errorf("%s: expected ≥1 diagnostic from violating fixture, got none (vacuous)", name)
		}
		for i, d := range diags {
			if d.Rel != rel {
				t.Errorf("%s[%d]: Rel = %q, want %q (Diagnostic must be clickable, not \":0:\")", name, i, d.Rel, rel)
			}
			if d.Line <= 0 {
				t.Errorf("%s[%d]: Line = %d, want > 0", name, i, d.Line)
			}
		}
	}

	// CHANNEL-MAX-PER-CONN-01: Config without the field; no setDefaults; no AcquireChannel.
	f, fset := parse("package rabbitmq\ntype Config struct{ Other int }\n")
	assertLocated("checkChannelMaxConfigField", checkChannelMaxConfigField(f, fset, rel))
	assertLocated("checkChannelMaxSetDefaults", checkChannelMaxSetDefaults(f, fset, rel))
	assertLocated("checkChannelMaxAcquireGuard", checkChannelMaxAcquireGuard(f, fset, rel))

	// PUBLISHER-FAILURE-HANDLING-01: Publish lacks Nack ref / slog.Warn /
	// RecordPublishFailure and has an unrecorded error return.
	f, fset = parse(`package rabbitmq
type P struct{}
func (p *P) Publish() error {
	if true {
		return errBoom
	}
	return nil
}
`)
	publish := findMethod(f, "Publish")
	if publish == nil {
		t.Fatal("fixture Publish method not found")
	}
	pubLine := fset.Position(publish.Pos()).Line
	assertLocated("checkPublisherNackErrcode", checkPublisherNackErrcode(publish, rel, pubLine))
	assertLocated("checkPublisherWarnCount", checkPublisherWarnCount(publish, rel, pubLine))
	assertLocated("checkPublisherRecordsFailureMetric", checkPublisherRecordsFailureMetric(publish, rel, pubLine))
	assertLocated("checkPublisherAllReturnsMustRecord", checkPublisherAllReturnsMustRecord(publish, fset, rel))

	// STOPINTAKE-INFLIGHT-WAIT-01: no StopIntake; no drainRemaining; no WithoutCancel.
	f, fset = parse("package rabbitmq\ntype S struct{}\nfunc (s *S) Other() {}\n")
	assertLocated("checkStopIntakeWaitsForInflight", checkStopIntakeWaitsForInflight(f, fset, rel))
	assertLocated("checkDrainNoParentCtxDone", checkDrainNoParentCtxDone(f, fset, rel))
	assertLocated("checkDrainUsesDetachedContext", checkDrainUsesDetachedContext(f, fset, rel))
}
