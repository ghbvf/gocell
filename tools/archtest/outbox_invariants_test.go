// Package archtest — outbox invariants.
//
// Merged from:
//   - outbox_cell_test.go         (OUTBOX-CELL-01)
//   - outbox_lease_id_test.go     (OUTBOX-LEASE-ID-CAS-01, OUTBOX-MARK-RETURNS-BOOL-01, OUTBOX-METADATA-MAX-BYTES-01)
//   - outbox_payload_size_test.go (OUTBOX-PAYLOAD-SIZE-01)
//   - outbox_receipt_test.go      (OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01)
//   - outbox_relay_lost_metric_test.go (OUTBOX-RELAY-LOST-METRIC-01)
//   - outbox_service_test.go      (OUTBOX-SERVICE-01..05)
//   - outbox_topic_test.go        (OUTBOX-TOPIC-FAILOPEN-01)
package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/kernel/metadata"
	kerneloutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ---------------------------------------------------------------------------
// OUTBOX-CELL-01
// ---------------------------------------------------------------------------

const (
	outboxCellRuleRawOption            = "OUTBOX-CELL-01_no_raw_publisher_writer_option"
	outboxCellGlobReadablePattern      = "cells/**/cell.go"
	outboxCellForbiddenOptionPublisher = "WithPublisher"
	outboxCellForbiddenOptionWriter    = "WithOutboxWriter"
)

// outboxCellViolation records a rule breach on a Cell file.
type outboxCellViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v outboxCellViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

// INVARIANT: OUTBOX-CELL-01
//
// TestCellsDoNotExposeRawOutboxOptions asserts that Cell packages stop
// exposing exported Option functions that take raw outbox.Publisher /
// outbox.Writer dependencies directly. The Cell-boundary contract
// established by PR-A5c is:
//
//   - WithEmitter(outbox.Emitter) — pre-composed emitter injection.
//   - WithOutboxDeps(pub, writer) — raw deps accumulated and composed
//     at Init() time via cell.ResolveEmitter.
//
// Standalone WithPublisher / WithOutboxWriter options let composition
// roots wire raw dependencies without going through the Cell-boundary
// emitter pipeline, which undoes the archtest guard for service-layer
// rules OUTBOX-SERVICE-01..05.
//
// ref: kubernetes/client-go rest.RESTClientFor — raw config consumed
//
//	by factory; typed client never re-exposes raw fields.
//
// Scope: scans every cells/**/cell.go declared in the repo (production
// cells only — tests and example cells are skipped; see isCellFile).
func TestCellsDoNotExposeRawOutboxOptions(t *testing.T) {
	root := findModuleRoot(t)

	violations := checkCellOutboxOptionRules(t, root)
	byRule := groupOutboxCellViolations(violations)

	if len(violations) > 0 {
		t.Logf("Found %d cell outbox architecture violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	t.Run("OUTBOX-CELL-01_no_raw_publisher_writer_option", func(t *testing.T) {
		assert.Empty(t, byRule[outboxCellRuleRawOption],
			"%s must not export Option functions named %s or %s; use WithEmitter / WithOutboxDeps",
			outboxCellGlobReadablePattern,
			outboxCellForbiddenOptionPublisher,
			outboxCellForbiddenOptionWriter)
	})
}

func checkCellOutboxOptionRules(t *testing.T, root string) []outboxCellViolation {
	t.Helper()

	files, err := findCellFiles(root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no %s files found", outboxCellGlobReadablePattern)

	var violations []outboxCellViolation
	for _, file := range files {
		fileViolations, err := checkCellOutboxOptionFile(root, file)
		require.NoError(t, err)
		violations = append(violations, fileViolations...)
	}
	return violations
}

// findCellFiles enumerates cell.go files for every cell declared in the
// project's metadata (covering both top-level cells/ and examples/*/cells/).
// Excludes slices/, internal/, vendor, worktrees, testdata, .git.
func findCellFiles(root string) ([]string, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil, err
	}

	var files []string
	for _, c := range project.Cells {
		cellDir := filepath.Join(root, filepath.Dir(c.File))
		walkErr := filepath.WalkDir(cellDir, func(path string, d os.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				switch d.Name() {
				case "vendor", "worktrees", "testdata", ".git", "slices", "internal":
					return filepath.SkipDir
				}
				return nil
			}
			if isCellFile(root, path) {
				files = append(files, path)
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	sort.Strings(files)
	return files, nil
}

// isCellFile matches cells/<cellname>/cell.go exactly (not cell_init.go,
// cell_routes.go, cell_providers.go, or *_test.go).
// Scope: platform cells only (top-level cells/ directory).
// Example cells under examples/ are intentionally excluded — this archtest
// targets production platform cell packages, not example/demo cells.
func isCellFile(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "cells/") {
		return false
	}
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	// Exactly cells/<name>/cell.go (no further subpath).
	parts := strings.Split(rel, "/")
	return len(parts) == 3 && parts[2] == "cell.go"
}

func checkCellOutboxOptionFile(root, path string) ([]outboxCellViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	var violations []outboxCellViolation
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv != nil {
			// Method on a receiver; the rule targets top-level Option
			// factory functions only (no receiver).
			continue
		}
		name := fn.Name.Name
		if name != outboxCellForbiddenOptionPublisher && name != outboxCellForbiddenOptionWriter {
			continue
		}
		if !fn.Name.IsExported() {
			continue
		}
		violations = append(violations, outboxCellViolation{
			Rule:    outboxCellRuleRawOption,
			File:    rel,
			Line:    fset.Position(fn.Pos()).Line,
			Message: fmt.Sprintf("Cell must not export Option %q; use WithEmitter or WithOutboxDeps instead", name),
		})
	}
	return violations, nil
}

func groupOutboxCellViolations(violations []outboxCellViolation) map[string][]string {
	byRule := make(map[string][]string)
	for _, v := range violations {
		byRule[v.Rule] = append(byRule[v.Rule], v.String())
	}
	return byRule
}

// ---------------------------------------------------------------------------
// OUTBOX-LEASE-ID-CAS-01, OUTBOX-MARK-RETURNS-BOOL-01, OUTBOX-METADATA-MAX-BYTES-01
// ---------------------------------------------------------------------------

// INVARIANT: OUTBOX-LEASE-ID-CAS-01
//
// TestOutboxLeaseIDCAS01 enforces OUTBOX-LEASE-ID-CAS-01: the five outbox CAS
// SQL constants in adapters/postgres/outbox_store.go MUST fence on lease_id.
// Without this guard, a future "simplification" PR could revert the fencing
// and re-introduce the B2-A-01 race where an old worker's MarkPublished
// overwrites a new owner's row.
//
// Pattern by query:
//   - claimPendingQuery   — SET ... lease_id = $N
//   - markPublishedQuery  — WHERE ... AND lease_id = $N
//   - markRetryQuery      — WHERE ... AND lease_id = $N (and SET lease_id = NULL)
//   - markDeadQuery       — WHERE ... AND lease_id = $N
//   - reclaimStaleQuery   — SET ... lease_id = ... (clears lease on transition)
//
// ref: graphile/worker complete_job CAS pattern
// ref: jackc/pgxjob worker_id UUID claim CTE
func TestOutboxLeaseIDCAS01(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "adapters", "postgres", "outbox_store.go")

	consts := readSQLConstants(t, path)

	// claim / reclaim — SET clause writes lease_id.
	requireMatch(t, "claimPendingQuery", consts, regexp.MustCompile(`(?i)\blease_id\s*=`),
		"OUTBOX-LEASE-ID-CAS-01: claim must SET lease_id (fencing token)")
	requireMatch(t, "reclaimStaleQuery", consts, regexp.MustCompile(`(?i)\blease_id\s*=`),
		"OUTBOX-LEASE-ID-CAS-01: reclaim must clear/rotate lease_id")

	// mark — WHERE clause checks lease_id.
	whereLeaseRe := regexp.MustCompile(`(?i)AND\s+lease_id\s*=`)
	requireMatch(t, "markPublishedQuery", consts, whereLeaseRe,
		"OUTBOX-LEASE-ID-CAS-01: markPublishedQuery must fence on lease_id")
	requireMatch(t, "markRetryQuery", consts, whereLeaseRe,
		"OUTBOX-LEASE-ID-CAS-01: markRetryQuery must fence on lease_id")
	requireMatch(t, "markDeadQuery", consts, whereLeaseRe,
		"OUTBOX-LEASE-ID-CAS-01: markDeadQuery must fence on lease_id")

	// reclaim — CTE + SKIP LOCKED + write-time status CAS + write-time
	// lease CAS. These four sub-patterns together prevent the regression
	// reported in PR #373 review #1, where an outer UPDATE without the
	// status/lease re-assertion could regress a row that left 'claiming'
	// (or rotated lease) between SELECT and UPDATE back to pending.
	requireMatch(t, "reclaimStaleQuery", consts,
		regexp.MustCompile(`(?is)\bFOR\s+UPDATE\s+SKIP\s+LOCKED\b`),
		"OUTBOX-LEASE-ID-CAS-01: reclaimStaleQuery must use FOR UPDATE SKIP LOCKED in the CTE picker")
	requireMatch(t, "reclaimStaleQuery", consts,
		regexp.MustCompile(`(?is)\bFROM\s+picked\b`),
		"OUTBOX-LEASE-ID-CAS-01: reclaimStaleQuery must use a `picked` CTE for batched id+lease snapshot")
	requireMatch(t, "reclaimStaleQuery", consts,
		regexp.MustCompile(`(?is)\bo\.status\s*=`),
		"OUTBOX-LEASE-ID-CAS-01: reclaimStaleQuery outer UPDATE must re-assert status (write-time CAS)")
	requireMatch(t, "reclaimStaleQuery", consts,
		regexp.MustCompile(`(?is)\bo\.lease_id\s*=\s*picked\.lease_id\b`),
		"OUTBOX-LEASE-ID-CAS-01: reclaimStaleQuery outer UPDATE must re-assert lease_id matches the picked snapshot")
}

// INVARIANT: OUTBOX-MARK-RETURNS-BOOL-01
//
// TestOutboxMarkReturnsBool01 enforces OUTBOX-MARK-RETURNS-BOOL-01: every
// runtime/outbox/relay*.go non-test caller of MarkPublished/MarkRetry/MarkDead
// MUST bind the `updated bool` return to a named identifier. Discarding it via
// `_, err :=` would re-open B2-A-05: stale-lease CAS misses get silently
// miscounted as successes. Glob coverage so future writeBack split (e.g.
// relay_writeback.go) cannot escape this gate by file rename.
func TestOutboxMarkReturnsBool01(t *testing.T) {
	root := findModuleRoot(t)
	matches, err := filepath.Glob(filepath.Join(root, "runtime", "outbox", "relay*.go"))
	if err != nil {
		t.Fatalf("glob relay*.go: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("OUTBOX-MARK-RETURNS-BOOL-01: no runtime/outbox/relay*.go files found")
	}

	wantTargets := map[string]bool{
		"MarkPublished": true,
		"MarkRetry":     true,
		"MarkDead":      true,
	}

	for _, path := range matches {
		// Skip _test.go — they may legitimately mock the interface and
		// the bool is checked elsewhere.
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) != 2 {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			if !wantTargets[sel.Sel.Name] {
				return true
			}
			if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
				pos := fset.Position(assign.Pos())
				t.Errorf("OUTBOX-MARK-RETURNS-BOOL-01: %s:%d: %s call discards "+
					"the updated-bool return; bind it and skip stats counting "+
					"on false (B2-A-05 stale-lease guard)",
					pos.Filename, pos.Line, sel.Sel.Name)
			}
			return true
		})
	}
}

// INVARIANT: OUTBOX-METADATA-MAX-BYTES-01
//
// TestOutboxMetadataMaxBytes01 enforces OUTBOX-METADATA-MAX-BYTES-01: the
// adapters/postgres outbox writer must reference the MaxMetadataBytes constant
// on every code path that builds the INSERT — currently Write (single-row
// path) and encodeBatchEntry (per-entry helper called from writeBatchChunk's
// loop). Each must gate the JSON-marshaled metadata size before it reaches
// the INSERT statement.
func TestOutboxMetadataMaxBytes01(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "adapters", "postgres", "outbox_writer.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	required := map[string]bool{"Write": false, "encodeBatchEntry": false}
	// writeBatchChunk no longer references MaxMetadataBytes directly (the
	// per-entry check moved into encodeBatchEntry); instead verify the link
	// is still in place so a future split cannot bypass the cap by skipping
	// the helper call.
	chunkCallsEncoder := false

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		_, want := required[fn.Name.Name]
		if !want && fn.Name.Name != "writeBatchChunk" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			id, idOK := n.(*ast.Ident)
			if !idOK {
				return true
			}
			if want && id.Name == "MaxMetadataBytes" {
				required[fn.Name.Name] = true
			}
			if fn.Name.Name == "writeBatchChunk" && id.Name == "encodeBatchEntry" {
				chunkCallsEncoder = true
			}
			return true
		})
	}
	for name, ok := range required {
		if !ok {
			t.Errorf("OUTBOX-METADATA-MAX-BYTES-01: %s/outbox_writer.go: "+
				"%s() must reference MaxMetadataBytes (B2-A-07)",
				filepath.Dir(path), name)
		}
	}
	if !chunkCallsEncoder {
		t.Errorf("OUTBOX-METADATA-MAX-BYTES-01: %s/outbox_writer.go: writeBatchChunk "+
			"must call encodeBatchEntry so the per-entry MaxMetadataBytes cap is "+
			"reached on the batch INSERT path (B2-A-07)", filepath.Dir(path))
	}
}

// readSQLConstants extracts top-level `const Name = "..."` declarations whose
// value is a string literal, keyed by const name. Returns the underlying
// string (unquoted, including newline content within back-tick literals).
func readSQLConstants(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	consts := make(map[string]string)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				consts[name.Name] = unquoted
			}
		}
	}
	return consts
}

func requireMatch(t *testing.T, name string, consts map[string]string, re *regexp.Regexp, msg string) {
	t.Helper()
	body, ok := consts[name]
	if !ok {
		t.Errorf("%s: const %s missing from outbox_store.go (was the query renamed?)", msg, name)
		return
	}
	if !re.MatchString(body) {
		t.Errorf("%s: const %s does not contain pattern %s\nbody: %s",
			msg, name, re.String(), strings.TrimSpace(body))
	}
}

// ---------------------------------------------------------------------------
// OUTBOX-PAYLOAD-SIZE-01
// ---------------------------------------------------------------------------

// INVARIANT: OUTBOX-PAYLOAD-SIZE-01
//
// TestOutboxPayloadSize01_ConstantDeclaredAndUsedByValidate enforces:
//
//	OUTBOX-PAYLOAD-SIZE-01-A  kernel/outbox/outbox.go declares MaxPayloadBytes
//	OUTBOX-PAYLOAD-SIZE-01-B  kernel/outbox/outbox.go references MaxPayloadBytes
//	                          inside Entry.Validate (so the cap actually fires)
//
// ref: docs/plans/202605011500-029-master-roadmap.md B6 PR-V1-PG-OUTBOX-RELAY-HARDEN
// ref: backlog2.md B2-A-07 (repurposed: metadata cap shipped, payload cap was the
//
//	true unguarded vector).
func TestOutboxPayloadSize01_ConstantDeclaredAndUsedByValidate(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	src := filepath.Join(root, "kernel", "outbox", "outbox.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	const constName = "MaxPayloadBytes"
	var declared bool
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == constName {
					declared = true
				}
			}
		}
	}
	if !declared {
		t.Errorf(
			"OUTBOX-PAYLOAD-SIZE-01-A: kernel/outbox/outbox.go must declare const %s "+
				"(payload byte cap; see roadmap B6, backlog2 B2-A-07 repurposed scope).",
			constName,
		)
	}

	// Walk Entry.Validate body for any reference to MaxPayloadBytes.
	var validate *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name != "Validate" || fd.Recv == nil || len(fd.Recv.List) != 1 {
			continue
		}
		// Recv must be `e Entry` (value receiver, type Entry).
		ident, ok := fd.Recv.List[0].Type.(*ast.Ident)
		if !ok || ident.Name != "Entry" {
			continue
		}
		validate = fd
		break
	}
	if validate == nil {
		t.Fatalf("OUTBOX-PAYLOAD-SIZE-01-B: cannot locate (Entry).Validate in %s", src)
	}
	// Look specifically for a `len(e.Payload) > MaxPayloadBytes` (or `>=`)
	// comparison. Merely importing / shadowing the const is not enough — it
	// must drive an actual size check, otherwise the cap is decorative
	// ("`_ = MaxPayloadBytes`" would have passed an ident-only scan).
	var compared bool
	ast.Inspect(validate.Body, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		if bin.Op != token.GTR && bin.Op != token.GEQ {
			return true
		}
		// LHS must be `len(e.Payload)` (or `len(<recv>.Payload)`).
		lenCall, ok := bin.X.(*ast.CallExpr)
		if !ok || len(lenCall.Args) != 1 {
			return true
		}
		lenIdent, ok := lenCall.Fun.(*ast.Ident)
		if !ok || lenIdent.Name != "len" {
			return true
		}
		sel, ok := lenCall.Args[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Payload" {
			return true
		}
		// RHS must reference MaxPayloadBytes by name.
		rhs, ok := bin.Y.(*ast.Ident)
		if !ok || rhs.Name != constName {
			return true
		}
		compared = true
		return false
	})
	if !compared {
		t.Errorf(
			"OUTBOX-PAYLOAD-SIZE-01-B: (Entry).Validate body must contain a "+
				"`len(<recv>.Payload) > %s` comparison (or >=). A bare reference "+
				"such as `_ = %s` is not sufficient — the comparison is the cap.",
			constName, constName,
		)
	}

	// Sanity: ensure the file's package import path is what we expect, so a
	// future reorganization that moves the file does not silently bypass the
	// gate. (`outbox` is the package name.)
	if f.Name.Name != "outbox" {
		t.Errorf("expected package outbox, got %s", f.Name.Name)
	}
	_ = strings.TrimSpace
}

// ---------------------------------------------------------------------------
// OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01
// ---------------------------------------------------------------------------

// INVARIANT: OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01
//
// TestOutboxHandleResultNoReceiptField enforces
// OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01: kernel/outbox.HandleResult must
// not declare a Receipt field. The field was removed in 029 K#12 (PR-V1-
// OUTBOX-RECEIPT-EXTRACT) — Settlement is now delivered via SubscriberHandler
// return value, not embedded in HandleResult.
//
// This gate supersedes the prior HANDLER-RECEIPT-WRITE-01 detector (cell
// handlers writing HandleResult.Receipt). The struct field no longer exists,
// so no handler can write it; the structural absence is the stronger guard.
func TestOutboxHandleResultNoReceiptField(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "outbox", "outbox.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var (
		found        bool
		receiptLine  int
		receiptField string
	)
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "HandleResult" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
		}
		found = true
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "Receipt" {
					receiptLine = fset.Position(name.Pos()).Line
					receiptField = name.Name
				}
			}
		}
		return false
	})

	if !found {
		t.Fatal("HandleResult struct definition not found in kernel/outbox/outbox.go")
	}
	if receiptField != "" {
		t.Errorf("OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01: kernel/outbox/outbox.go:%d: "+
			"HandleResult must not declare a %s field — Settlement is delivered "+
			"via SubscriberHandler return value (see 029 K#12)",
			receiptLine, receiptField)
	}
}

// ---------------------------------------------------------------------------
// OUTBOX-RELAY-LOST-METRIC-01
// ---------------------------------------------------------------------------

// INVARIANT: OUTBOX-RELAY-LOST-METRIC-01
//
// OUTBOX-RELAY-LOST-METRIC-01-A  runtime/outbox/relay.go handleFailedEntry must
//
//	read Mark{Retry,Dead}'s first return into a
//	named bool (no `_` discard).
//
// OUTBOX-RELAY-LOST-METRIC-01-B  PollCycleResult must declare a Lost field, so
//
//	a "lost" outcome is reportable end-to-end.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B6 PR-V1-PG-OUTBOX-RELAY-HARDEN
// ref: backlog2.md B2-A-05 PG-RELAY-FAIL-WRITE-UNHANDLED-ROWS
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

// INVARIANT: OUTBOX-RELAY-LOST-METRIC-01-B
//
// PollCycleResult must declare a Lost field so handleFailedEntry can
// route stale-lease writebacks into the lost stat / metric (separately
// from real retries).
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
			"OUTBOX-RELAY-LOST-METRIC-01-B: PollCycleResult must declare a Lost int field " +
				"(roadmap B6, backlog2 B2-A-05). It travels alongside Published/Retried/" +
				"Dead/Skipped so providerRelayCollector.RecordPollCycle can fire " +
				"`outbox_relayed_total{outcome=\"lost\"}`.",
		)
	}
}

// ---------------------------------------------------------------------------
// OUTBOX-SERVICE-01..05
// ---------------------------------------------------------------------------

const (
	outboxServiceRuleTxRunnerNil     = "OUTBOX-SERVICE-01"
	outboxServiceRuleDirectPublish   = "OUTBOX-SERVICE-02"
	outboxServiceRuleRuntimeOutbox   = "OUTBOX-SERVICE-03"
	outboxServiceRulePublisherMode   = "OUTBOX-SERVICE-04"
	outboxServiceRuleWriterAdapter   = "OUTBOX-SERVICE-05_no_writer_adapter_option"
	outboxRuntimeImportRelPath       = "runtime/outbox"
	outboxServiceGlobReadablePattern = "cells/**/slices/**/service.go"
)

type outboxServiceViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v outboxServiceViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

// INVARIANT: OUTBOX-SERVICE-01
// INVARIANT: OUTBOX-SERVICE-02
// INVARIANT: OUTBOX-SERVICE-03
// INVARIANT: OUTBOX-SERVICE-04
// INVARIANT: OUTBOX-SERVICE-05
//
// TestSliceServicesDoNotBypassTransactionalOutbox enforces OUTBOX-SERVICE-01..05
// on cells/**/slices/**/service.go files.
func TestSliceServicesDoNotBypassTransactionalOutbox(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)

	violations := checkSliceServiceOutboxRules(t, root, modPath)
	byRule := groupOutboxServiceViolations(violations)

	if len(violations) > 0 {
		t.Logf("Found %d outbox service architecture violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	t.Run("OUTBOX-SERVICE-01_no_txrunner_nil_mode", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleTxRunnerNil],
			"%s must not branch on txRunner == nil or txRunner != nil", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-02_no_direct_publisher_publish", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleDirectPublish],
			"%s must not call Publisher.Publish directly from the service layer", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-03_no_runtime_outbox_import", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleRuntimeOutbox],
			"%s must not import runtime/outbox", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-04_no_publisher_mode_parsing", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRulePublisherMode],
			"%s must not depend on outbox.Publisher or construct DirectEmitter; Cell boundary owns mode parsing", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-05_no_writer_adapter_option", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleWriterAdapter],
			"%s must not define WithOutboxWriter; service layer owns WithEmitter / WithTxManager only", outboxServiceGlobReadablePattern)
	})
}

func checkSliceServiceOutboxRules(t *testing.T, root, modPath string) []outboxServiceViolation {
	t.Helper()

	files, err := findSliceServiceFiles(root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no %s files found", outboxServiceGlobReadablePattern)

	var violations []outboxServiceViolation
	for _, file := range files {
		fileViolations, err := checkSliceServiceOutboxFile(root, modPath, file)
		require.NoError(t, err)
		violations = append(violations, fileViolations...)
	}
	return violations
}

func findSliceServiceFiles(root string) ([]string, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil, err
	}

	var files []string
	for _, s := range project.Slices {
		sliceDir := filepath.Join(root, filepath.Dir(s.File))
		svc := filepath.Join(sliceDir, "service.go")
		if _, statErr := os.Stat(svc); statErr == nil {
			if isSliceServiceFile(root, svc) {
				files = append(files, svc)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func isSliceServiceFile(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, "cells/") &&
		strings.Contains(rel, "/slices/") &&
		strings.HasSuffix(rel, "/service.go")
}

func checkSliceServiceOutboxFile(root, modPath, path string) ([]outboxServiceViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	var violations []outboxServiceViolation
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if importPath == modPath+"/"+outboxRuntimeImportRelPath {
			violations = append(violations, outboxServiceViolation{
				Rule:    outboxServiceRuleRuntimeOutbox,
				File:    rel,
				Line:    fset.Position(imp.Pos()).Line,
				Message: "service layer must not import runtime/outbox",
			})
		}
	}

	// Track the enclosing FuncDecl while walking so OUTBOX-SERVICE-01 can
	// allow constructor-level fail-fast validation (NewService) while still
	// rejecting runtime-method silent fallback. After 029 #03 ADR Decision 2
	// removed persistence.RunnerOrNoop, constructors fail-fast on nil
	// TxRunner (returning *Service, error) is the explicit replacement for
	// the deleted helper; method-internal fallback remains forbidden.
	var enclosing *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.FuncDecl:
			enclosing = expr
			if isWithOutboxWriterFunc(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRuleWriterAdapter,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not define WithOutboxWriter adapter option",
				})
			}
			if isPublisherModeParsingFunc(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not define direct-publisher mode helpers/options",
				})
			}
		case *ast.BinaryExpr:
			if isTxRunnerNilComparison(expr) && !isConstructorFailFast(enclosing) {
				violations = append(violations, outboxServiceViolation{
					Rule: outboxServiceRuleTxRunnerNil,
					File: rel,
					Line: fset.Position(expr.Pos()).Line,
					Message: "service layer must not branch on txRunner nil mode" +
						" (allowed only in NewService constructor as fail-fast validation returning error)." +
						" To opt in, change NewXxx to NewXxx(...) (*T, error) and add a top-level:" +
						" if txRunner == nil { return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, ...) }",
				})
			}
		case *ast.CallExpr:
			if isDirectPublishCall(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRuleDirectPublish,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not call Publisher.Publish directly",
				})
			}
			if isDirectEmitterConstructor(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not construct DirectEmitter",
				})
			}
		case *ast.SelectorExpr:
			if isOutboxPublisherSelector(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not expose outbox.Publisher dependencies",
				})
			}
			if isOutboxDirectPublishModeSelector(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not reference outbox direct-publish mode types or constants",
				})
			}
		case *ast.Field:
			if hasPublisherModeState(expr.Names) || isPublishFailureModeExpr(expr.Type) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not store publisher mode state",
				})
			}
		case *ast.Ident:
			if isPublisherModeIdent(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not define or use direct-publisher mode names",
				})
			}
		}
		return true
	})

	return violations, nil
}

func isWithOutboxWriterFunc(fn *ast.FuncDecl) bool {
	return fn.Name.Name == "WithOutboxWriter"
}

func isPublisherModeParsingFunc(fn *ast.FuncDecl) bool {
	return fn.Name.Name == "WithPublishFailureMode" || fn.Name.Name == "directPublishMode"
}

func isTxRunnerNilComparison(expr *ast.BinaryExpr) bool {
	if expr.Op != token.EQL && expr.Op != token.NEQ {
		return false
	}
	return (isTxRunnerExpr(expr.X) && isNilIdent(expr.Y)) ||
		(isNilIdent(expr.X) && isTxRunnerExpr(expr.Y))
}

// isConstructorFailFast reports whether fn is a service constructor that
// performs explicit fail-fast validation on a nil TxRunner. After 029 #03 ADR
// Decision 2, constructors are allowed to fail-fast on nil TxRunner because
// that is the explicit, error-surfacing replacement for the deleted
// persistence.RunnerOrNoop helper. Method-level nil fallback (e.g. runInTx
// that skips tx when nil) remains forbidden because it silently degrades to
// non-transactional mode.
//
// A function qualifies iff all of the following hold:
//  1. It is a top-level function (no receiver) whose name starts with "New".
//  2. It returns exactly two results, the last of which is "error".
//  3. Its body's top-level statement list (Body.List, not recursively nested)
//     contains at least one statement matching isFailFastReturn — i.e. an
//     if-statement of the form:
//     if <txRunner-expr> == nil { return nil, <non-nil-expr> }
//
// Condition (3) prevents a NewFoo that internally installs a silent noop
// fallback (if s.txRunner == nil { s.txRunner = noopRunner{} }) from being
// whitelisted by the mere presence of a New* signature returning (*T, error).
func isConstructorFailFast(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Recv != nil { // method (has receiver) — not a constructor
		return false
	}
	if !strings.HasPrefix(fn.Name.Name, "New") {
		return false
	}
	if fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 2 {
		return false
	}
	last := fn.Type.Results.List[len(fn.Type.Results.List)-1]
	id, ok := last.Type.(*ast.Ident)
	if !ok || id.Name != "error" {
		return false
	}
	if fn.Body == nil {
		return false
	}
	for _, stmt := range fn.Body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if ok && isFailFastReturn(ifStmt) {
			return true
		}
	}
	return false
}

// isFailFastReturn reports whether stmt is an if-statement of the form:
//
//	if <txRunner-expr> == nil { return nil, <non-nil-expr> }
//
// The else branch is not examined. Only top-level return statements inside
// stmt.Body are checked; nested blocks are not recursed into.
func isFailFastReturn(stmt *ast.IfStmt) bool {
	// Condition must be a binary == expression with one side being a
	// txRunner expression and the other being nil.
	binExpr, ok := stmt.Cond.(*ast.BinaryExpr)
	if !ok || binExpr.Op != token.EQL {
		return false
	}
	if (!isTxRunnerExpr(binExpr.X) || !isNilIdent(binExpr.Y)) &&
		(!isNilIdent(binExpr.X) || !isTxRunnerExpr(binExpr.Y)) {
		return false
	}
	// The body must contain at least one return statement whose first result
	// is nil and whose second result is any non-nil expression.
	for _, bodyStmt := range stmt.Body.List {
		ret, ok := bodyStmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 {
			continue
		}
		firstIsNil := isNilIdent(ret.Results[0])
		secondIsNonNil := !isNilIdent(ret.Results[1])
		if firstIsNil && secondIsNonNil {
			return true
		}
	}
	return false
}

func isTxRunnerExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "txRunner"
	case *ast.SelectorExpr:
		return e.Sel.Name == "txRunner"
	default:
		return false
	}
}

func isNilIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isDirectPublishCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Publish"
}

func isDirectEmitterConstructor(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "NewDirectEmitter"
}

func isOutboxPublisherSelector(expr *ast.SelectorExpr) bool {
	ident, ok := expr.X.(*ast.Ident)
	return ok && ident.Name == "outbox" && expr.Sel.Name == "Publisher"
}

func isOutboxDirectPublishModeSelector(expr *ast.SelectorExpr) bool {
	ident, ok := expr.X.(*ast.Ident)
	if !ok || ident.Name != "outbox" {
		return false
	}
	switch expr.Sel.Name {
	case "DirectPublishFailureMode", "DirectPublishFailOpen", "DirectPublishFailClosed":
		return true
	default:
		return false
	}
}

func hasPublisherModeState(names []*ast.Ident) bool {
	for _, name := range names {
		if name != nil && name.Name == "publishFailureMode" {
			return true
		}
	}
	return false
}

func isPublishFailureModeExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "PublishFailureMode"
	case *ast.SelectorExpr:
		return e.Sel.Name == "PublishFailureMode"
	default:
		return false
	}
}

func isPublisherModeIdent(id *ast.Ident) bool {
	switch id.Name {
	case "WithPublishFailureMode", "directPublishMode", "publishFailureMode":
		return true
	default:
		return false
	}
}

func groupOutboxServiceViolations(violations []outboxServiceViolation) map[string][]string {
	byRule := make(map[string][]string)
	for _, v := range violations {
		byRule[v.Rule] = append(byRule[v.Rule], v.String())
	}
	return byRule
}

// ---------------------------------------------------------------------------
// OUTBOX-TOPIC-FAILOPEN-01
// ---------------------------------------------------------------------------

const (
	outboxTopicRuleFailOpen         = "OUTBOX-TOPIC-FAILOPEN-01_security_topics_must_not_opt_in_fail_open"
	outboxTopicForbiddenPolicyField = "FailurePolicy"
	outboxTopicEntryField           = "Topic"
	outboxTopicEventTypeField       = "EventType"
	outboxEntryTypeName             = "Entry"
	outboxFailurePolicyTypeName     = "FailurePolicy"
	gocellOutboxPackagePath         = "github.com/ghbvf/gocell/kernel/outbox"
	fixtureOutboxPackagePath        = "fixturetest/outbox"
)

var outboxFailOpenConstValues = map[string]int64{
	gocellOutboxPackagePath:  int64(kerneloutbox.FailurePolicyFailOpen),
	fixtureOutboxPackagePath: 1,
}

// outboxSecurityTopicPattern matches topics that carry security or audit-chain
// semantics. Events matching these prefixes must not opt into
// FailurePolicyFailOpen — dropping them silently removes audit/security
// signals from downstream consumers.
//
// ref: kubernetes apiserver/pkg/audit — audit events default to Fail policy;
// operators opt into Ignore per backend, not per event type.
var outboxSecurityTopicPattern = regexp.MustCompile(`^(event\.)?(session|user|role|audit)\.`)

type outboxTopicViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v outboxTopicViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

// INVARIANT: OUTBOX-TOPIC-FAILOPEN-01
//
// TestSecurityTopicsDoNotOptInFailOpen enforces OUTBOX-TOPIC-FAILOPEN-01:
// an outbox.Entry composite literal whose Topic or EventType string constant
// matches one of the security-sensitive prefixes (session.*, user.*, role.*,
// audit.* and their event.* contract forms) must not set FailurePolicy:
// outbox.FailurePolicyFailOpen.
//
// The scanner uses go/types TypesInfo to evaluate Topic/EventType field
// expressions, covering BasicLit, same-package const Idents, and cross-package
// SelectorExprs (e.g. dto.TopicSessionCreated). go/types' built-in constant
// folding provides full intra-module const propagation without manual SSA.
//
// Scope: scans all production non-test .go files via packages.Load.
//
// ref: kubernetes apiserver/pkg/audit Backend.FailurePolicy (Ignore/Fail)
// ref: ThreeDotsLabs/watermill message/router/middleware/retry.go
func TestSecurityTopicsDoNotOptInFailOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode (loads production packages module-wide, ~5-10s)")
	}
	root := findModuleRoot(t)

	violations, err := checkOutboxTopicFailOpenRule(root)
	require.NoError(t, err)

	if len(violations) > 0 {
		t.Logf("Found %d OUTBOX-TOPIC-FAILOPEN-01 violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	assert.Empty(t, violations,
		"security-sensitive topics (session.*, user.*, role.*, audit.*, event.* security contracts) "+
			"must not set FailurePolicy: outbox.FailurePolicyFailOpen on the outbox.Entry "+
			"literal; drop silently = lose audit invariant. Leave FailurePolicy "+
			"unset (= Default, falls through to Cell ctor default = FailClosed).")
}

// checkOutboxTopicFailOpenRule loads module packages with full type info and
// scans production Go files for OUTBOX-TOPIC-FAILOPEN-01 violations.
func checkOutboxTopicFailOpenRule(root string) ([]outboxTopicViolation, error) {
	r, err := typeseval.SharedResolver(root, false, nil, prodscan.Patterns(root)...)
	if err != nil {
		return nil, err
	}
	var violations []outboxTopicViolation
	for _, p := range r.Packages() {
		pkgViolations, err := scanPackage(root, p)
		if err != nil {
			return nil, err
		}
		violations = append(violations, pkgViolations...)
	}
	return violations, nil
}

// scanPackage scans all non-test Go files in a loaded package for violations.
// packages.Package.Syntax is aligned with GoFiles via Fset.Position.
func scanPackage(root string, p *packages.Package) ([]outboxTopicViolation, error) {
	var violations []outboxTopicViolation
	for _, file := range p.Syntax {
		absPath := p.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(absPath, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return nil, fmt.Errorf("filepath.Rel: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if skipOutboxTopicProductionScan(rel) {
			continue
		}
		violations = append(violations, scanOutboxTopicFailOpenAST(p.Fset, file, rel, p)...)
	}
	return violations, nil
}

// scanOutboxTopicFailOpenAST is the core AST-matching routine. Given a parsed
// file, fileset, and the owning package (for TypesInfo lookup), it returns
// every outbox.Entry composite literal that opts into FailurePolicyFailOpen
// with a Topic or EventType matching the security-sensitive prefix regex.
//
// Topic/EventType field values are evaluated via typeseval.EvaluateConstString,
// covering BasicLit, same-package const Ident, and cross-package SelectorExpr.
func scanOutboxTopicFailOpenAST(fset *token.FileSet, file *ast.File, fileLabel string, pkg *packages.Package) []outboxTopicViolation {
	var violations []outboxTopicViolation
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if !isOutboxEntryLiteral(pkg, lit) {
			return true
		}
		policy := extractFailurePolicy(pkg, lit)
		if policy.safe() {
			return true
		}
		topic := extractStringField(pkg, lit, outboxTopicEntryField)
		eventType := extractStringField(pkg, lit, outboxTopicEventTypeField)
		route := effectiveOutboxRoute(topic, eventType)

		switch {
		case route.ok && outboxSecurityTopicPattern.MatchString(route.value):
			violations = append(violations, outboxTopicViolation{
				Rule:    outboxTopicRuleFailOpen,
				File:    fileLabel,
				Line:    fset.Position(lit.Pos()).Line,
				Message: outboxPolicyViolationMessage(policy, route.value),
			})
			return true
		case route.unknown() || !route.present:
			violations = append(violations, outboxTopicViolation{
				Rule:    outboxTopicRuleFailOpen,
				File:    fileLabel,
				Line:    fset.Position(lit.Pos()).Line,
				Message: outboxUnknownRouteViolationMessage(policy),
			})
			return true
		default:
			return true
		}
	})
	return violations
}

// isOutboxEntryLiteral matches real kernel/outbox.Entry composite literals by
// type identity. Import aliases and type aliases are resolved by go/types;
// unrelated Entry structs are rejected even when they share field names.
func isOutboxEntryLiteral(pkg *packages.Package, lit *ast.CompositeLit) bool {
	if pkg.TypesInfo == nil || lit.Type == nil {
		return false
	}
	tv, ok := pkg.TypesInfo.Types[lit.Type]
	if !ok {
		return false
	}
	return isOutboxNamedType(tv.Type, outboxEntryTypeName)
}

type outboxTopicFieldValue struct {
	present bool
	ok      bool
	value   string
}

func (f outboxTopicFieldValue) unknown() bool {
	return f.present && !f.ok
}

func effectiveOutboxRoute(topic, eventType outboxTopicFieldValue) outboxTopicFieldValue {
	if topic.present {
		if topic.ok && topic.value == "" {
			return eventType
		}
		return topic
	}
	return eventType
}

// extractStringField returns the compile-time constant string value for the
// named field of a composite literal, evaluated via typeseval.EvaluateConstString.
// Covers BasicLit, same-package const Ident, and cross-package SelectorExpr.
// Returns ok=false when the field is missing or its value is not a constant string.
func extractStringField(pkg *packages.Package, lit *ast.CompositeLit, fieldName string) outboxTopicFieldValue {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			continue
		}
		value, ok := typeseval.EvaluateConstString(pkg.TypesInfo, kv.Value)
		return outboxTopicFieldValue{present: true, ok: ok, value: value}
	}
	return outboxTopicFieldValue{}
}

type outboxFailurePolicyStatus int

const (
	outboxPolicyAbsent outboxFailurePolicyStatus = iota
	outboxPolicyKnownOther
	outboxPolicyKnownFailOpen
	outboxPolicyUnknown
)

func (s outboxFailurePolicyStatus) safe() bool {
	return s == outboxPolicyAbsent || s == outboxPolicyKnownOther
}

func outboxPolicyViolationMessage(policy outboxFailurePolicyStatus, topic string) string {
	if policy == outboxPolicyUnknown {
		return fmt.Sprintf(
			"outbox.Entry for topic %q uses non-constant FailurePolicy;"+
				" security/audit events must statically remain FailClosed", topic)
	}
	return fmt.Sprintf(
		"outbox.Entry for topic %q opts into FailurePolicyFailOpen;"+
			" security/audit events must remain FailClosed (leave FailurePolicy unset)", topic)
}

func outboxUnknownRouteViolationMessage(policy outboxFailurePolicyStatus) string {
	if policy == outboxPolicyUnknown {
		return "outbox.Entry uses non-constant FailurePolicy and Topic/EventType is not statically known;" +
			" security/audit fail-open policy must be statically ruled out"
	}
	return "outbox.Entry opts into FailurePolicyFailOpen but Topic/EventType is not statically known;" +
		" fail-open requires a statically non-security topic"
}

// INVARIANT: OUTBOX-TOPIC-FAILOPEN-01 (regression fixtures)
//
// TestSecurityTopicsDoNotOptInFailOpen_RegressionFixtures asserts that the
// scanner correctly flags (or passes) each fixture scenario. The fixture set
// uses real Go packages under testdata/topic_const_fixtures/ loaded via
// packages.Load to exercise the full type-checking path.
func TestSecurityTopicsDoNotOptInFailOpen_RegressionFixtures(t *testing.T) {
	fixturesRoot := filepath.Join(findArchTestDir(t), "testdata", "topic_const_fixtures")

	cases := []struct {
		pattern   string
		wantMatch bool
	}{
		{"./basicliteral_session_failopen", true},
		{"./basicliteral_event_session_failopen", true},
		{"./samepackage_const_session_failopen", true},
		{"./crosspackage_dto_session_failopen/consumer", true},
		{"./crosspackage_event_dto_session_failopen/consumer", true},
		{"./samepackage_const_session_failclosed", false},
		{"./nonsecurity_metric_failopen_passes", false},
		// Non-session security topics (audit.*, user.*, role.*).
		{"./basicliteral_audit_failopen", true},
		// Non-security topic (config.*) with FailOpen — rule must not fire.
		{"./basicliteral_config_failopen_passes", false},
		// EventType-only path: Topic absent, EventType is a same-package const.
		{"./eventtype_only_const_audit_failopen", true},
		// Cross-package const for a non-session security topic (audit.*).
		{"./crosspackage_audit_dto/consumer", true},
		// Import alias must not affect outbox.Entry identity.
		{"./import_alias_entry_session_failopen", true},
		// Type aliases to outbox.Entry still have outbox.Entry identity.
		{"./entry_type_alias_session_failopen", true},
		// Local and cross-package aliases to the fail-open const are still fail-open.
		{"./local_failopen_alias_session", true},
		{"./crosspackage_failopen_alias/consumer", true},
		// Dynamic policy on a security route is fail-closed.
		{"./dynamic_failopen_policy_session", true},
		// Non-outbox Entry types must not be matched by name alone.
		{"./unrelated_entry_failopen_passes", false},
		// Fail-open entries with dynamic routing topics fail closed.
		{"./dynamic_topic_failopen", true},
		// Empty Topic falls back to EventType, matching outbox.Entry.RoutingTopic.
		{"./topic_empty_event_session_failopen", true},
		// Topic takes precedence over EventType, matching outbox.Entry.RoutingTopic.
		{"./topic_precedence_dynamic_event_passes", false},
	}

	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			r, err := typeseval.SharedResolver(fixturesRoot, false, nil, c.pattern)
			require.NoError(t, err, "load fixture package %s", c.pattern)

			var violations []outboxTopicViolation
			for _, p := range r.Packages() {
				for _, file := range p.Syntax {
					absPath := p.Fset.Position(file.Pos()).Filename
					if strings.HasSuffix(absPath, "_test.go") {
						continue
					}
					rel, err := filepath.Rel(fixturesRoot, absPath)
					require.NoError(t, err)
					rel = filepath.ToSlash(rel)
					violations = append(violations, scanOutboxTopicFailOpenAST(p.Fset, file, rel, p)...)
				}
			}

			if c.wantMatch {
				assert.NotEmpty(t, violations, "fixture %q should trigger OUTBOX-TOPIC-FAILOPEN-01", c.pattern)
			} else {
				assert.Empty(t, violations, "fixture %q should not trigger rule, got: %v", c.pattern, violations)
			}
		})
	}
}

// extractFailurePolicy classifies the FailurePolicy field. A dynamic policy is
// treated as unknown, and callers fail closed when the route is security-like or
// not statically known.
func extractFailurePolicy(pkg *packages.Package, lit *ast.CompositeLit) outboxFailurePolicyStatus {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != outboxTopicForbiddenPolicyField {
			continue
		}
		if isOutboxFailOpenConst(pkg.TypesInfo, kv.Value) {
			return outboxPolicyKnownFailOpen
		}
		if isKnownOutboxFailurePolicyConst(pkg.TypesInfo, kv.Value) {
			return outboxPolicyKnownOther
		}
		return outboxPolicyUnknown
	}
	return outboxPolicyAbsent
}

func isOutboxFailOpenConst(info *types.Info, expr ast.Expr) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil {
		return false
	}
	pkgPath, ok := outboxNamedTypePackagePath(tv.Type, outboxFailurePolicyTypeName)
	if !ok {
		return false
	}
	failOpenValue, ok := outboxFailOpenConstValues[pkgPath]
	if !ok {
		return false
	}
	value, exact := constant.Int64Val(constant.ToInt(tv.Value))
	return exact && value == failOpenValue
}

func isKnownOutboxFailurePolicyConst(info *types.Info, expr ast.Expr) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil {
		return false
	}
	_, ok = outboxNamedTypePackagePath(tv.Type, outboxFailurePolicyTypeName)
	return ok
}

func isOutboxNamedType(t types.Type, name string) bool {
	_, ok := outboxNamedTypePackagePath(t, name)
	return ok
}

func outboxNamedTypePackagePath(t types.Type, name string) (string, bool) {
	if t == nil {
		return "", false
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Name() != name || obj.Pkg() == nil {
		return "", false
	}
	pkgPath := obj.Pkg().Path()
	return pkgPath, isOutboxPackagePath(pkgPath)
}

func isOutboxPackagePath(pkgPath string) bool {
	return pkgPath == gocellOutboxPackagePath || pkgPath == fixtureOutboxPackagePath
}

func skipOutboxTopicProductionScan(rel string) bool {
	return strings.HasPrefix(rel, "tools/") ||
		strings.HasPrefix(rel, "tests/") ||
		strings.Contains(rel, "/testdata/") ||
		strings.HasPrefix(rel, "testdata/")
}

// ---------------------------------------------------------------------------
// METADATA-LIMITS-SINGLE-SOURCE-01
//
// kernel/metautil owns the four metadata size constants (MaxMetadataKeys,
// MaxMetadataKeyLen, MaxMetadataValueLen, MaxMetadataTotalSize). Reintroducing
// any of them in kernel/outbox or kernel/command would silently re-fork the
// limits and let the two transports drift again — which is exactly what 030
// review §G-07 (b) flagged as the original bug. Type system cannot enforce
// "constant lives in one package" and codegen has no marker to derive from,
// so we ground the rule in archtest per CLAUDE.md §"新增 invariant 决策原则"
// tier 3.
// ---------------------------------------------------------------------------

// INVARIANT: METADATA-LIMITS-SINGLE-SOURCE-01
//
// TestMetadataLimitsSingleSource enforces that MaxMetadataKeys,
// MaxMetadataKeyLen, MaxMetadataValueLen, and MaxMetadataTotalSize are
// declared exactly once in the repository — under kernel/metautil. Any
// other declaration (notably in kernel/outbox or kernel/command, which
// historically duplicated them) fails the test.
func TestMetadataLimitsSingleSource(t *testing.T) {
	t.Parallel()

	repoRoot := repoRootFromTestPath(t)

	forbidden := map[string]struct{}{
		"MaxMetadataKeys":      {},
		"MaxMetadataKeyLen":    {},
		"MaxMetadataValueLen":  {},
		"MaxMetadataTotalSize": {},
	}

	type hit struct {
		File  string
		Line  int
		Const string
	}
	var hits []hit

	walkErr := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(repoRoot, path)
			if rel == "." {
				return nil
			}
			// Allow the canonical home; skip vendored/generated trees.
			if rel == "kernel/metautil" {
				return filepath.SkipDir
			}
			if strings.HasPrefix(rel, "vendor/") ||
				strings.HasPrefix(rel, "generated/") ||
				strings.HasPrefix(rel, "tools/archtest/") ||
				strings.Contains(rel, "/testdata/") ||
				strings.HasPrefix(rel, "testdata/") ||
				strings.HasPrefix(rel, "worktrees/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		// Syntactically broken files are out of scope for this rule — gofmt /
		// build invariants own that contract. Discard the parse error.
		file, _ := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if file == nil {
			return nil
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if _, bad := forbidden[name.Name]; !bad {
						continue
					}
					rel, _ := filepath.Rel(repoRoot, path)
					hits = append(hits, hit{
						File:  rel,
						Line:  fset.Position(name.Pos()).Line,
						Const: name.Name,
					})
				}
			}
		}
		return nil
	})
	require.NoError(t, walkErr, "walk repo root")

	if len(hits) == 0 {
		return
	}
	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("%s:%d declares %s outside kernel/metautil", h.File, h.Line, h.Const))
	}
	sort.Strings(lines)
	t.Fatalf("METADATA-LIMITS-SINGLE-SOURCE-01: forbidden duplicates of metadata limit constants:\n  %s",
		strings.Join(lines, "\n  "))
}

// repoRootFromTestPath finds the repository root by walking up from the test
// binary's working directory until it sees a go.mod file. Test binaries run
// from the package directory, so we ascend from CWD.
func repoRootFromTestPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("METADATA-LIMITS-SINGLE-SOURCE-01: repo root with go.mod not found above %s", cwd)
		}
		dir = parent
	}
}
