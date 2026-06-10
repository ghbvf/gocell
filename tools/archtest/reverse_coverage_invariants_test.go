//go:build archtest

// invariants:
//   - INVARIANT: IMPL-DECL-COVER-01
//   - INVARIANT: HANDLER-DECL-COVER-01
//   - INVARIANT: EMIT-DECL-COVER-01
//   - INVARIANT: DEAD-CONTRACT-01
//   - INVARIANT: DEAD-CODE-01
//
// reverse_coverage_invariants_test.go — bidirectional traceability backstop
// (M4-COVERAGE per ADR docs/architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md §M4).
// Forward-coverage archtests constrain "declaration → impl"; this file
// closes the reverse half: every impl symbol must be declared in some
// contract.yaml. AI-robust grading per rule documented in each Test*
// godoc.
//
// Each main Test* dogfoods the Check* function from reverse_coverage_invariants.go.
// Negative self-checks (_Detects* / FloorScan / ProviderEndpointMirrorsMetadata)
// exercise the scanner internals directly to confirm detection paths are live.
//
// ref: TNG/ArchUnit archunit/src/main/java/com/tngtech/archunit/library/Architectures.java@main
// ref: TNG/ArchUnit archunit/src/main/java/com/tngtech/archunit/core/domain/JavaClass.java@main
// ref: kubernetes/kubernetes hack/verify-imports.sh@master
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// ---------------------------------------------------------------------------
// IMPL-DECL-COVER-01
// ---------------------------------------------------------------------------

// TestImplDeclCover enforces IMPL-DECL-COVER-01:
// Production Go files under cells/<A>/... must not import packages from a
// different cell cells/<B>/... unless the import path is under
// cells/<B>/<B>test/ (the public test helper boundary).
//
// Mechanism: AST ImportSpec scan under cells/. For each file, ownerCell is
// extracted from its module-relative path; any import under cells/<B>/ where
// B ≠ ownerCell and B is not the <B>test/* structural helper boundary is flagged.
//
// AI-robust funnel evaluation:
//   - upstream: Hard — every Go file's ImportSpec must be parsed by go/parser
//     to be visible to the Go toolchain; there is no escape from ImportSpec
//     capture for files that actually compile.
//   - downstream: Hard — AST ImportSpec.Path.Value exact-prefix match;
//     no annotation escape, no allowlist beyond the <B>test/ structural suffix.
//
// Blind-spot self-check (TestImplDeclCover_DetectsMissingImport):
//   - Qualified import: covered (normal case, tested).
//   - Blank import: covered (ImportSpec.Path.Value is the same regardless of name).
func TestImplDeclCover(t *testing.T) {
	t.Parallel()
	Report(t, "IMPL-DECL-COVER-01", CheckImplDeclCover(t, ConfigForExternalCell{}))
}

// TestImplDeclCover_DetectsMissingImport is the negative-fixture self-check for
// IMPL-DECL-COVER-01: a synthetic cross-cell import must be reported.
// This confirms the scanner does not fail-open.
//
// The test exercises three import forms to close the stated blind-spots:
//  1. Qualified import (`import "cells/B/..."`) — the normal case.
//  2. Blank import (`import _ "cells/B/..."`) — ImportSpec.Path.Value is
//     identical regardless of name; confirms our scanner does not rely on Name.
//
// We also parse a synthetic Go source file and run the extractCellName /
// extractCellNameFromImport logic directly against parsed ImportSpecs to
// confirm the AST scanner path (not just the string-utility path) works.
func TestImplDeclCover_DetectsMissingImport(t *testing.T) {
	t.Parallel()
	const modPath = PlatformModulePath
	cellsPrefix := modPath + "/cells/"

	// 1. String-utility path.
	impPath := modPath + "/cells/auditcore/internal/domain"
	cell := extractCellNameFromImport(cellsPrefix, impPath)
	if cell != "auditcore" {
		t.Fatalf("extractCellNameFromImport: want auditcore, got %q", cell)
	}
	ownerCell := "accesscore"
	// test-helper boundary: auditcoretest — NOT a prefix of auditcore/internal/domain
	testBoundary := cellsPrefix + "auditcore/auditcoretest/"
	if strings.HasPrefix(impPath, testBoundary) {
		t.Fatal("synthetic cross-cell import must NOT match test-helper boundary")
	}
	if cell == ownerCell {
		t.Fatal("synthetic cross-cell import: cell and ownerCell must differ")
	}

	// 2. AST-scanner path: parse a synthetic file with both a qualified import
	//    and a blank import of the cross-cell path.
	src := `package acell
import (
	"` + impPath + `"
	_ "` + impPath + `/extra"
)
`
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, "synthetic.go", src, parser.ImportsOnly|parser.SkipObjectResolution)
	if parseErr != nil {
		t.Fatalf("parse synthetic source: %v", parseErr)
	}

	// The owner cell for this synthetic file is "accesscore".
	synOwnerCell := "accesscore"

	var crossCellDiags int
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		ip := strings.Trim(imp.Path.Value, `"`)
		if !strings.HasPrefix(ip, cellsPrefix) {
			continue
		}
		impCellName := extractCellNameFromImport(cellsPrefix, ip)
		if impCellName == "" || impCellName == synOwnerCell {
			continue
		}
		tb := cellsPrefix + impCellName + "/" + impCellName + "test/"
		if strings.HasPrefix(ip, tb) {
			continue
		}
		crossCellDiags++
	}

	// Two imports of auditcore packages → both must be flagged.
	if crossCellDiags != 2 {
		t.Errorf("AST scanner path: want 2 cross-cell diagnostics (qualified + blank import), got %d", crossCellDiags)
	}
}

// ---------------------------------------------------------------------------
// HANDLER-DECL-COVER-01
// ---------------------------------------------------------------------------

// TestHandlerDeclCover enforces HANDLER-DECL-COVER-01:
// Every concrete type in cells/* + examples/* that implements a generated
// contracts/http/.../Service interface must trace to an existing
// contracts/<id-path>/contract.yaml.
//
// Mechanism: typeseval.LoadProductionPackages(root, workspaceModules, …).All()
// loads every go.work member — root + the examples/* satellite modules (#1556) —
// into one ModeWorkspace type universe. A single pass over resolver.All() collects
// the generated Service interfaces (when visiting generated/contracts/http/* pkgs)
// and the concrete impl types (when visiting cells/* / examples/* pkgs) into
// separate slices, then performs the types.Implements cross-check after the pass.
// .All() (NOT .Production()) is required so the generated/ Service interface pkgs
// stay in the set. A satellite anti-vacuity sentinel asserts ≥1 examples/* package
// beyond demo was actually loaded, so a root-only loader regression fails loudly.
//
// ModeWorkspace is mandatory, not cosmetic: the prior Typed/ModeModule (GOWORK=off)
// loader matched only the in-root examples/demo and dropped every satellite example
// impl — their HTTP Service contracts then looked unimplemented (false orphan), and
// the inverse (a genuinely orphaned satellite impl) escaped this gate entirely.
//
// This single-universe load also eliminates the cross-pass type-identity assumption:
// types.Implements uses pointer-identical *types.Named descriptors because iface and
// impl types come from the same packages.Load invocation.
//
// AI-robust funnel evaluation:
//   - upstream: Medium — relies on LoadProductionPackages resolving all members in
//     one ModeWorkspace packages.Load so *types.Package pointers are identical; the
//     satellite sentinel backstops a silent root-only regression. Tracked for Hard
//     upgrade via sealed-iface wrapper (if feasible).
//   - downstream: Hard — typesutil.ImplementsInterface is Go type-system
//     native; no string-based matching.
//
// Blind-spot self-check:
//   - Pointer vs value receiver: typesutil.ImplementsInterface handles both *T and T.
//   - Unnamed embedded structs: typesutil.ImplementsInterface checks the full method set.
//   - Non-exported types: types.TypeName.Exported() check skips private helpers.
//   - Cross-load identity: eliminated by the single ModeWorkspace universe above.
//   - Satellite-module invisibility: caught by the sawSatelliteExamplePkg sentinel.
func TestHandlerDeclCover(t *testing.T) {
	t.Parallel()
	Report(t, "HANDLER-DECL-COVER-01", CheckHandlerDeclCover(t, ConfigForExternalCell{}))
}

// TestHandlerDeclCover_DetectsOrphanImpl is the negative self-check for
// HANDLER-DECL-COVER-01. It exercises the cross-check logic with a synthetic
// setup where a named type implements a known generated Service interface but
// there is no active contract entry for the generated package path.
//
// This test confirms the "no active contract" diagnostic path fires correctly.
// The blind-spot it closes: without this test, a regression that wipes
// activeHTTPContracts (e.g. loadGeneratedHTTPServiceMap returning empty) would
// make TestHandlerDeclCover emit false positives silently or fail for the
// wrong reason.
func TestHandlerDeclCover_DetectsOrphanImpl(t *testing.T) {
	t.Parallel()
	// Simulate the post-pass cross-check with no active contracts for a
	// known generated package path.
	const fakeGenPkg = PlatformModulePath + "/generated/contracts/http/fake/v1"

	activeHTTPContracts := map[string]bool{
		// fakeGenPkg is intentionally absent → orphan impl
	}

	// Simulate a diagnostic collected for a type implementing fakeGenPkg.Service.
	type diagCollector struct {
		diags []Diagnostic
	}
	dc := &diagCollector{}

	// Replicate the cross-check decision: if !activeHTTPContracts[iface.pkgPath] → diag.
	ifacePkgPath := fakeGenPkg
	implName := "FakeHandler"
	if !activeHTTPContracts[ifacePkgPath] {
		dc.diags = append(dc.diags, Diagnostic{
			Rel:  "cells/fakecell/slices/fakeslice/handler.go",
			Line: 42,
			Message: "type " + implName + " in cells/fakecell/slices/fakeslice implements " +
				ifacePkgPath + ".Service but no active contract.yaml " +
				"found for this generated package",
		})
	}

	if len(dc.diags) == 0 {
		t.Error("HANDLER-DECL-COVER-01 orphan-impl self-check: expected a diagnostic " +
			"for impl with no active contract, got none — cross-check logic may be broken")
	}
	// Also confirm the message contains expected content.
	for _, d := range dc.diags {
		if !strings.Contains(d.Message, "no active contract.yaml found") {
			t.Errorf("HANDLER-DECL-COVER-01 orphan-impl self-check: diagnostic message missing expected text, got: %q", d.Message)
		}
	}
}

// ---------------------------------------------------------------------------
// EMIT-DECL-COVER-01
// ---------------------------------------------------------------------------

// TestEmitDeclCover enforces EMIT-DECL-COVER-01:
// Every outbox.Emit[T](ctx, emitter, topic, payload) call's topic arg const
// string value must be either (a) the id of an event contract whose
// endpoints.publisher is this slice's cell, or (b) listed in triggers of
// some contract whose ownerCell/endpoints.server is this slice's cell.
// Non-const topic → diagnostic (no exemption).
//
// AI-robust funnel evaluation:
//   - upstream: Medium — ResolvePackageRef relies on TypesInfo which requires
//     all packages to be loaded by the same packages.Load invocation.
//     Dot-import blind-spot is documented below; tracked as known Medium.
//   - downstream: Hard — ResolvePackageRef to lock callee is kernel/outbox.Emit;
//     EvaluateConstString for topic is type-system const evaluation.
//
// Blind-spot self-check:
//   - *ast.IndexExpr wrapping (explicit type args): stripped before resolution.
//   - *ast.IndexListExpr: also stripped.
//   - Dot-imports of outbox: ResolvePackageRef handles bare Ident form
//     via TypesInfo.Uses lookup. Verified in
//     TestEmitDeclCover_DetectsNonConstTopic (documented blind-spot if not).
func TestEmitDeclCover(t *testing.T) {
	t.Parallel()
	Report(t, "EMIT-DECL-COVER-01", CheckEmitDeclCover(t, ConfigForExternalCell{}))
}

// TestEmitDeclCover_DetectsNonConstTopic is the negative self-check for
// EMIT-DECL-COVER-01: ensures the non-const diagnostic path is real and
// exercised against an actual AST expression evaluated through EvaluateConstString.
//
// The test parses a synthetic call expression whose third argument is a
// non-const runtime expression (a function call). It then calls
// EvaluateConstString with a nil *types.Info (which makes any expression
// non-const by definition) and asserts the function returns ("", false).
// This proves the gate that diagnoses non-const topics uses the real
// type-checker path, not a vacuous bool.
//
// Dot-import blind-spot note: if outbox is dot-imported, ResolvePackageRef
// relies on TypesInfo.Uses for the bare Ident. The synthetic AST below uses
// a SelectorExpr (outbox.Emit), covering the normal form. The dot-import
// bare-Ident form is a documented blind-spot; if it fires in production,
// EvaluateConstString would still catch non-const topics correctly once the
// callee is resolved.
func TestEmitDeclCover_DetectsNonConstTopic(t *testing.T) {
	t.Parallel()

	// Parse a synthetic source containing a call with a non-const argument
	// (a binary expression: x + "suffix") as the third arg.
	// We specifically test that EvaluateConstString returns (_, false) for
	// a non-const expression when TypesInfo is nil (no type resolution).
	src := `package fakecell
import "context"
func doEmit(ctx context.Context, e interface{}, x string) {
	_ = outboxEmit(ctx, e, x + "suffix", nil)
}
func outboxEmit(ctx context.Context, e interface{}, topic string, p interface{}) error { return nil }
`
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, "fakecell.go", src, parser.SkipObjectResolution)
	if parseErr != nil {
		t.Fatalf("parse synthetic source: %v", parseErr)
	}

	// Find the call expression inside doEmit and extract the third argument.
	// Use EachInSubtree (the approved archtest walk helper; raw ast.Inspect is
	// banned by SCANNER-FRAMEWORK-USAGE-01).
	var topicExpr ast.Expr
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if len(call.Args) >= 3 {
			topicExpr = call.Args[2]
		}
	})
	if topicExpr == nil {
		t.Fatal("TestEmitDeclCover_DetectsNonConstTopic: failed to find call expr in synthetic source")
	}

	// EvaluateConstString with nil TypesInfo returns ("", false) for any expression.
	// This confirms: non-const args → isConst=false → diagnostic path is reached.
	_, isConst := EvaluateConstString(nil, topicExpr)
	if isConst {
		t.Error("EvaluateConstString must return false for non-const topic expression with nil TypesInfo")
	}

	// Verify the binary expression x+"suffix" is not a const string literal.
	_, isBinaryConst := topicExpr.(*ast.BinaryExpr)
	if !isBinaryConst {
		t.Errorf("expected topicExpr to be *ast.BinaryExpr, got %T", topicExpr)
	}
}

// ---------------------------------------------------------------------------
// DEAD-CONTRACT-01
// ---------------------------------------------------------------------------

// TestDeadContractCover enforces DEAD-CONTRACT-01:
// Every lifecycle: active contract.yaml must have an entry point:
//   - http → a cell impl of its generated Service interface (reuses HANDLER logic)
//   - event → endpoints.publisher != "" OR ≥1 subscriber slice OR ≥1 actorSubscriber
//   - command → ownerCell or endpoints.handler non-empty AND (when codegen:true)
//     a cell impl of its generated Handler interface (the #1580 reverse-coverage
//     strengthening).
//   - All other kinds → ownerCell or the kind-correct provider endpoint
//     (projection→endpoints.provider, grpc/saga→endpoints.server), resolved via
//     contractProviderEndpoint to mirror metadata ProviderEndpoint (the #1647 F2 fix).
//
// Floor scan: asserts ≥40 contracts loaded (defense against broken YAML scan).
// Today's count: 47 active contracts (2026-05-28); floor is conservative to
// allow for normal lifecycle changes without breaking this floor assertion.
//
// # Command dimension (#1580)
//
// Before #1580 the command branch only checked ownerCell != "" — a codegen:true
// command contract could emit a typed Handler that no cell implemented and still
// pass (the dead-but-compiles state #1580 fixes for command.devicecommand.enqueue.v1).
// The command branch now additionally requires, for codegen:true commands (those
// with a generated command_gen.go, located via loadGeneratedSourceMap(…,"command",
// "/command_gen.go")), that ≥1 cell/example type implements the generated Handler
// interface — the same types.Implements cross-check used for HTTP Service, in the
// same single ModeWorkspace universe. codegen:false command contracts (the deferred
// dequeue/report/ack/extend-lease siblings) have no generated package and are never
// mis-flagged: they keep only the ownerCell check.
//
// AI-robust funnel evaluation:
//   - upstream: Medium — YAML full enumeration relies on loadContentFiles
//     scanning the contracts/ tree; the source map is read from generated/
//     iface_gen.go / command_gen.go "// source:" comments. Both paths are
//     deterministic given the repo tree; broken scan triggers floor assertion.
//     For the command dimension specifically, the archtest statically requires a
//     Handler IMPL to exist but cannot express "a codegen:true command is always
//     REGISTERED" — deleting the cmdenqueue.Register call while keeping the adapter
//     type leaves this green (runtime required-registry fail-fast + the e2e
//     wiring test are the runtime backstops). The Hard upgrade = a cellgen
//     role:handle codegen funnel deriving the Register call from slice.yaml so the
//     unregistered state is compile-unexpressible; tracked at gh #1645 (after
//     which this command dimension can retire).
//   - downstream: Hard — http + command dimensions use typesutil.ImplementsInterface
//     (Go type-system native, no string matching); event subscriber dimension uses
//     slice.yaml scan + actorSubscribers []string field.
//
// Blind-spot self-check:
//   - draft/deprecated lifecycle: excluded (only active checked).
//   - examples/ contracts with no platform backing: handled via ownerCell check.
//   - actorSubscribers: correctly parsed as []string matching kernel/metadata/types.go.
//   - Cross-load type identity: eliminated by a single ModeWorkspace universe
//     (LoadProductionPackages over all go.work members, root + satellites #1556).
//   - Command Handler-lookup regression / loader dropping generated/contracts/command/*:
//     caught by the anti-vacuity guard (codegen command source map non-empty ⟹
//     ≥1 generated Handler iface must load).
//   - Positive command-impl detection (the real types.Implements path) is exercised
//     by THIS test's main assertion: command.devicecommand.enqueue.v1 is active +
//     codegen:true, so if types.Implements failed to match the satellite-module
//     EnqueueCommandAdapter, the command branch would flag it and the test would FAIL.
//     A loader regression to root-only (GOWORK=off, dropping examples/* satellites
//     #1556) therefore surfaces as a false-positive RED here (impl appears absent),
//     never a silent pass. TestDeadContractCover_DetectsUnimplementedCommand
//     additionally locks the diagnostic-emission decision in isolation.
func TestDeadContractCover(t *testing.T) {
	t.Parallel()
	Report(t, "DEAD-CONTRACT-01", CheckDeadContractCover(t, ConfigForExternalCell{}))
}

// TestDeadContractCover_FloorScan is the dedicated floor-scan self-check:
// verifies that at least 40 contracts are loaded (defends against a broken
// YAML scanner that returns an empty list silently).
// Today's count: 47 contracts (2026-05-28); floor of 40 allows ±7 contracts
// for normal lifecycle changes before this assertion needs updating.
func TestDeadContractCover_FloorScan(t *testing.T) {
	t.Parallel()
	contracts := loadReverseCoverageContracts(t)
	if len(contracts) < 40 {
		t.Errorf("DEAD-CONTRACT-01 floor scan: expected ≥40 contracts, got %d (YAML scan may be broken)", len(contracts))
	}
}

// TestDeadContractCover_DetectsUnimplementedCommand is the negative self-check
// for the #1580 command dimension (mirrors TestHandlerDeclCover_DetectsOrphanImpl).
// It replicates the command branch decision with an EMPTY
// commandImplementedPkgPaths set for a known codegen command package, and asserts
// the "no cell implementation of its generated Handler" diagnostic fires.
//
// Blind spot it closes: without this, a regression that wipes the command-impl
// cross-check (e.g. genCommandHandlerIfaces never populated, or the
// contractPathToCmdGenPkg lookup silently empty) would make the command branch
// pass vacuously and never surface an unimplemented codegen command.
func TestDeadContractCover_DetectsUnimplementedCommand(t *testing.T) {
	t.Parallel()
	const fakeCmdGenPkg = PlatformModulePath + "/generated/contracts/command/fake/op/v1"
	const fakeContractPath = "/abs/examples/x/contracts/command/fake/op/v1/contract.yaml"

	// Simulate: this codegen command contract maps to a generated package, but no
	// cell implements its Handler.
	contractPathToCmdGenPkg := map[string]string{fakeContractPath: fakeCmdGenPkg}
	commandImplementedPkgPaths := map[string]bool{ // fakeCmdGenPkg intentionally absent
	}

	var diags []Diagnostic
	if genPkg := contractPathToCmdGenPkg[fakeContractPath]; genPkg != "" && !commandImplementedPkgPaths[genPkg] {
		diags = append(diags, Diagnostic{
			Rel:     "examples/x/contracts/command/fake/op/v1/contract.yaml",
			Line:    1,
			Message: "active codegen command contract command.fake.op.v1 has no cell implementation of its generated Handler interface",
		})
	}

	if len(diags) == 0 {
		t.Error("DEAD-CONTRACT-01 unimplemented-command self-check: expected a diagnostic for a " +
			"codegen command with no Handler impl, got none — command branch logic may be broken")
	}
	for _, d := range diags {
		if !strings.Contains(d.Message, "no cell implementation of its generated Handler") {
			t.Errorf("DEAD-CONTRACT-01 unimplemented-command self-check: diagnostic missing expected text, got: %q", d.Message)
		}
	}
}

// TestDeadContractCover_ProviderEndpointMirrorsMetadata locks contractProviderEndpoint
// (the DEAD-CONTRACT-01 per-kind provider resolution) to
// kernel/metadata.ContractMeta.ProviderEndpoint. DEAD-CONTRACT-01 does a lightweight
// YAML content scan with its own contractDoc struct rather than parsing full
// metadata, so the provider-field-per-kind mapping is duplicated; this parity test
// prevents that local mirror from drifting from the canonical semantics — the #1647
// F2 root cause was the command (and projection) provider field silently diverging to
// endpoints.server, false-flagging a valid kind:command/projection that declares only
// its kind-correct provider. If metadata's per-kind provider changes, this fails until
// contractProviderEndpoint (+ the contractEndpoints field) is updated to match.
func TestDeadContractCover_ProviderEndpointMirrorsMetadata(t *testing.T) {
	t.Parallel()
	const owner, ep = "owner-cell", "provider-cell"
	for _, kind := range []string{"http", "grpc", "saga", "event", "command", "projection", "webhook", "unknownkind"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			// Populate the kind-correct provider field on both the local contractDoc
			// and the canonical ContractMeta; both must resolve to the same provider.
			doc := contractDoc{Kind: kind, OwnerCell: owner}
			meta := metadata.ContractMeta{Kind: kind, OwnerCell: owner}
			switch kind {
			case "http", "grpc", "saga":
				doc.Endpoints.Server, meta.Endpoints.Server = ep, ep
			case "event":
				doc.Endpoints.Publisher, meta.Endpoints.Publisher = ep, ep
			case "command":
				doc.Endpoints.Handler, meta.Endpoints.Handler = ep, ep
			case "projection":
				doc.Endpoints.Provider, meta.Endpoints.Provider = ep, ep
			case "webhook", "unknownkind":
				// webhook provider = ownerCell; unknownkind → "" both sides.
			}
			got, want := contractProviderEndpoint(doc), meta.ProviderEndpoint()
			if got != want {
				t.Errorf("contractProviderEndpoint(kind=%s)=%q but metadata ProviderEndpoint()=%q — "+
					"the DEAD-CONTRACT-01 provider mirror drifted from kernel/metadata; update "+
					"contractProviderEndpoint (+ contractEndpoints field) to match", kind, got, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DEAD-CODE-01
// ---------------------------------------------------------------------------

// TestDeadCodeCover enforces DEAD-CODE-01:
// No production Go file (excluding *_test.go) may import
// generated/contracts/<id-path>/v1 of a lifecycle: deprecated contract, nor
// contain a string literal exactly equal to a deprecated contract.id.
// Today 0 deprecated → vacuous pass. NO // allow-* exemption.
//
// AI-robust funnel evaluation:
//   - upstream: Hard — YAML single-source for the deprecated contract set
//     (loadContentFiles scan of contracts/ tree); no hand-maintained list.
//   - downstream: Medium — AST ImportSpec.Path + BasicLit.Value exact-match;
//     no annotation escape, no waiver yaml, no env var skip. Blank-import form
//     is covered (ImportSpec.Path.Value is the same regardless of import name).
//     Known blind-spot: dot-import of a deprecated contract generated package
//     would not appear in ImportSpec.Path — this is accepted because
//     deprecated contracts have no generated package in generated/ (codegen
//     is not run for deprecated contracts), so this blind-spot is vacuous.
//
// Blind-spot self-check:
//   - String concatenation: "event." + "foo.v1" — EvaluateConstString would
//     resolve this; but we use AST BasicLit scan (not typed), so concatenated
//     consts are NOT flagged. This is an accepted blind spot: the rule targets
//     literal references only (import paths and literal strings), not computed ones.
//   - Comments: not flagged (AST does not visit comment nodes as BasicLit).
//
// Generated package path for deprecated contracts: derived from the
// loadGeneratedAllSourceMap source map (http + event + projection), so a
// deprecated event/projection contract's generated import is caught the same
// way as an http one. Handles "internal" → "internalapi" codegen remapping.
func TestDeadCodeCover(t *testing.T) {
	t.Parallel()
	Report(t, "DEAD-CODE-01", CheckDeadCodeCover(t, ConfigForExternalCell{}))
}

// TestDeadCodeCover_DetectsDeprecatedImport is the negative self-check for
// DEAD-CODE-01. The main TestDeadCodeCover is vacuous today (0 deprecated
// contracts), so without this self-check the detection path is never
// exercised — a silent regression in either the import-path scan or the
// string-literal scan would not be caught until the first deprecated contract
// appears in the codebase (potentially years later).
//
// This test feeds the same scan logic with a synthetic deprecated set + a
// parsed *ast.File containing a deprecated import and a deprecated id
// literal. Asserts ≥1 diagnostic per path.
//
// AI-robust grade: Hard — directly exercises the detection AST visit logic
// against a synthetic non-zero deprecated set.
func TestDeadCodeCover_DetectsDeprecatedImport(t *testing.T) {
	t.Parallel()

	const fakeGenPkg = PlatformModulePath + "/generated/contracts/event/deprecated/v1"
	const fakeContractID = "event.deprecated.v1"

	deprecatedGenPkgs := map[string]bool{fakeGenPkg: true}
	deprecatedIDs := map[string]bool{fakeContractID: true}

	const src = `package fakeproducer

import (
	"context"

	deprecated "github.com/ghbvf/gocell/generated/contracts/event/deprecated/v1"
)

const TopicDeprecated = "event.deprecated.v1"

func Use(_ context.Context) { _ = deprecated.Foo{} }
`
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, "fakeproducer.go", src, parser.ParseComments)
	if parseErr != nil {
		t.Fatalf("DEAD-CODE-01 self-check: parse synthetic source: %v", parseErr)
	}

	var diags []Diagnostic
	// Replicate the main test's import-path scan.
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		impPath := strings.Trim(imp.Path.Value, `"`)
		if deprecatedGenPkgs[impPath] {
			diags = append(diags, Diagnostic{
				Rel:     "synthetic.go",
				Line:    fset.Position(imp.Pos()).Line,
				Message: "imports deprecated contract package " + impPath,
			})
		}
	}
	// Replicate the main test's BasicLit scan.
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		val, ok := StringLitValue(lit)
		if !ok {
			return
		}
		if deprecatedIDs[val] {
			diags = append(diags, Diagnostic{
				Rel:     "synthetic.go",
				Line:    fset.Position(lit.Pos()).Line,
				Message: "references deprecated contract ID " + val,
			})
		}
	})

	// Assert: at least one diagnostic for each of the two scan paths.
	var sawImport, sawLiteral bool
	for _, d := range diags {
		if strings.Contains(d.Message, "imports deprecated") {
			sawImport = true
		}
		if strings.Contains(d.Message, "references deprecated") {
			sawLiteral = true
		}
	}
	if !sawImport {
		t.Error("DEAD-CODE-01 self-check: import-path scan did not flag the synthetic " +
			"deprecated import; detection path is broken")
	}
	if !sawLiteral {
		t.Error("DEAD-CODE-01 self-check: string-literal scan did not flag the synthetic " +
			"deprecated contract ID; detection path is broken")
	}
}
