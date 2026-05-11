// invariants:
//   - INVARIANT: OUTBOX-LEASE-ID-CAS-01
//   - INVARIANT: OUTBOX-MARK-RETURNS-BOOL-01
//   - INVARIANT: OUTBOX-METADATA-MAX-BYTES-01
//   - INVARIANT: OUTBOX-PAYLOAD-SIZE-01
//   - INVARIANT: OUTBOX-HANDLERESULT-NO-RECEIPT-FIELD-01
//   - INVARIANT: OUTBOX-RELAY-LOST-METRIC-01
//   - INVARIANT: OUTBOX-SERVICE-01
//   - INVARIANT: OUTBOX-SERVICE-02
//   - INVARIANT: OUTBOX-SERVICE-03
//   - INVARIANT: OUTBOX-SERVICE-04
//   - INVARIANT: OUTBOX-SERVICE-05
//   - INVARIANT: OUTBOX-TOPIC-FAILOPEN-01
//   - INVARIANT: METADATA-LIMITS-SINGLE-SOURCE-01
//
// Package archtest — outbox invariants.
//
// Merged from:
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
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

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
	scope := scanner.DirsScope(root, []string{"runtime/outbox"},
		scanner.MatchRels(func(rel string) bool {
			return strings.HasPrefix(filepath.Base(rel), "relay") &&
				filepath.ToSlash(filepath.Dir(rel)) == "runtime/outbox"
		}),
	)

	wantTargets := map[string]bool{
		"MarkPublished": true,
		"MarkRetry":     true,
		"MarkDead":      true,
	}

	hits := 0
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(_ *testing.T, fc scanner.FileContext) {
		path := fc.AbsPath
		_ = path
		hits++
		scanner.EachInSubtree[ast.AssignStmt](fc.File, func(assign *ast.AssignStmt) {
			if len(assign.Rhs) != 1 || len(assign.Lhs) != 2 {
				return
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return
			}
			if !wantTargets[sel.Sel.Name] {
				return
			}
			if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name == "_" {
				pos := fc.Fset.Position(assign.Pos())
				t.Errorf("OUTBOX-MARK-RETURNS-BOOL-01: %s:%d: %s call discards "+
					"the updated-bool return; bind it and skip stats counting "+
					"on false (B2-A-05 stale-lease guard)",
					fc.Rel, pos.Line, sel.Sel.Name)
			}
		})
	})
	if hits == 0 {
		t.Fatal("OUTBOX-MARK-RETURNS-BOOL-01: no runtime/outbox/relay*.go files found")
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

	scanner.EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Recv == nil || fn.Body == nil {
			return
		}
		_, want := required[fn.Name.Name]
		if !want && fn.Name.Name != "writeBatchChunk" {
			return
		}
		scanner.EachInSubtree[ast.Ident](fn.Body, func(id *ast.Ident) {
			if want && id.Name == "MaxMetadataBytes" {
				required[fn.Name.Name] = true
			}
			if fn.Name.Name == "writeBatchChunk" && id.Name == "encodeBatchEntry" {
				chunkCallsEncoder = true
			}
		})
	})
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
	scanner.EachInSubtree[ast.GenDecl](f, func(gd *ast.GenDecl) {
		if gd.Tok != token.CONST {
			return
		}
		scanner.EachInSubtree[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
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
		})
	})
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
	scanner.EachInSubtree[ast.GenDecl](f, func(gd *ast.GenDecl) {
		if gd.Tok != token.CONST {
			return
		}
		scanner.EachInSubtree[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			for _, name := range vs.Names {
				if name.Name == constName {
					declared = true
				}
			}
		})
	})
	if !declared {
		t.Errorf(
			"OUTBOX-PAYLOAD-SIZE-01-A: kernel/outbox/outbox.go must declare const %s "+
				"(payload byte cap; see roadmap B6, backlog2 B2-A-07 repurposed scope).",
			constName,
		)
	}

	// Walk Entry.Validate body for any reference to MaxPayloadBytes.
	var validate *ast.FuncDecl
	scanner.EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if validate != nil {
			return
		}
		if fd.Name.Name != "Validate" || fd.Recv == nil || len(fd.Recv.List) != 1 {
			return
		}
		// Recv must be `e Entry` (value receiver, type Entry).
		ident, ok := fd.Recv.List[0].Type.(*ast.Ident)
		if !ok || ident.Name != "Entry" {
			return
		}
		validate = fd
	})
	if validate == nil {
		t.Fatalf("OUTBOX-PAYLOAD-SIZE-01-B: cannot locate (Entry).Validate in %s", src)
	}
	// Look specifically for a `len(e.Payload) > MaxPayloadBytes` (or `>=`)
	// comparison. Merely importing / shadowing the const is not enough — it
	// must drive an actual size check, otherwise the cap is decorative
	// ("`_ = MaxPayloadBytes`" would have passed an ident-only scan).
	var compared bool
	scanner.EachInSubtree[ast.BinaryExpr](validate.Body, func(bin *ast.BinaryExpr) {
		if compared {
			return
		}
		if bin.Op != token.GTR && bin.Op != token.GEQ {
			return
		}
		// LHS must be `len(e.Payload)` (or `len(<recv>.Payload)`).
		lenCall, ok := bin.X.(*ast.CallExpr)
		if !ok || len(lenCall.Args) != 1 {
			return
		}
		lenIdent, ok := lenCall.Fun.(*ast.Ident)
		if !ok || lenIdent.Name != "len" {
			return
		}
		sel, ok := lenCall.Args[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Payload" {
			return
		}
		// RHS must reference MaxPayloadBytes by name.
		rhs, ok := bin.Y.(*ast.Ident)
		if !ok || rhs.Name != constName {
			return
		}
		compared = true
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
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Name.Name != "HandleResult" {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
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
	scanner.EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "handleFailedEntry" && fd.Recv != nil {
			handle = fd
		}
	})
	if handle == nil {
		t.Fatalf("OUTBOX-RELAY-LOST-METRIC-01-A: handleFailedEntry not found in %s", src)
	}

	// Inspect every assignment whose RHS is a call ending in MarkRetry or
	// MarkDead. The LHS first ident MUST NOT be `_` — discarding the updated
	// bool collapses the lost-lease branch into an uncounted writeback.
	var violations []token.Pos
	scanner.EachInSubtree[ast.AssignStmt](handle.Body, func(assign *ast.AssignStmt) {
		if len(assign.Rhs) != 1 {
			return
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		switch sel.Sel.Name {
		case "MarkRetry", "MarkDead":
		default:
			return
		}
		if len(assign.Lhs) < 1 {
			return
		}
		first, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || first.Name == "_" {
			violations = append(violations, assign.Pos())
		}
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
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name.Name != "PollCycleResult" {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name.Name == "Lost" {
					hasLost = true
				}
			}
		}
	})
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

	// OUTBOX-SERVICE-01 requires knowing the enclosing FuncDecl to distinguish
	// constructor fail-fast (allowed) from runtime-method nil fallback (forbidden).
	// We use EachInSubtree[ast.FuncDecl] to iterate FuncDecls and check FuncDecl-level
	// violations plus the BinaryExpr within each body. All other node types that
	// appear anywhere in the file (including struct fields, top-level declarations)
	// are scanned on the full file.

	// FuncDecl-level checks and BinaryExpr within each function body.
	scanner.EachInSubtree[ast.FuncDecl](file, func(enclosing *ast.FuncDecl) {
		if isWithOutboxWriterFunc(enclosing) {
			violations = append(violations, outboxServiceViolation{
				Rule:    outboxServiceRuleWriterAdapter,
				File:    rel,
				Line:    fset.Position(enclosing.Pos()).Line,
				Message: "service layer must not define WithOutboxWriter adapter option",
			})
		}
		if isPublisherModeParsingFunc(enclosing) {
			violations = append(violations, outboxServiceViolation{
				Rule:    outboxServiceRulePublisherMode,
				File:    rel,
				Line:    fset.Position(enclosing.Pos()).Line,
				Message: "service layer must not define direct-publisher mode helpers/options",
			})
		}
		// OUTBOX-SERVICE-01: BinaryExpr nil checks scoped within the function body
		// so enclosing context is available for isConstructorFailFast.
		scanner.EachInSubtree[ast.BinaryExpr](enclosing, func(expr *ast.BinaryExpr) {
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
		})
	})
	// File-wide checks for node types that can appear both inside and outside
	// function bodies (struct fields, top-level var/const, expressions).
	scanner.EachInSubtree[ast.CallExpr](file, func(expr *ast.CallExpr) {
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
	})
	scanner.EachInSubtree[ast.SelectorExpr](file, func(expr *ast.SelectorExpr) {
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
	})
	scanner.EachInSubtree[ast.Field](file, func(expr *ast.Field) {
		if hasPublisherModeState(expr.Names) || isPublishFailureModeExpr(expr.Type) {
			violations = append(violations, outboxServiceViolation{
				Rule:    outboxServiceRulePublisherMode,
				File:    rel,
				Line:    fset.Position(expr.Pos()).Line,
				Message: "service layer must not store publisher mode state",
			})
		}
	})
	scanner.EachInSubtree[ast.Ident](file, func(expr *ast.Ident) {
		if isPublisherModeIdent(expr) {
			violations = append(violations, outboxServiceViolation{
				Rule:    outboxServiceRulePublisherMode,
				File:    rel,
				Line:    fset.Position(expr.Pos()).Line,
				Message: "service layer must not define or use direct-publisher mode names",
			})
		}
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
	// Conservatively restricted to bare `error` interface (not pkg-qualified
	// SelectorExpr like *pkg.ErrType). All 12 current outbox-bound services
	// return standard `error`; extend here if a future service returns a
	// pkg-qualified error type that should still trigger the fail-fast check.
	id, ok := last.Type.(*ast.Ident)
	if !ok || id.Name != "error" {
		return false
	}
	if fn.Body == nil {
		return false
	}
	// Top-level Body.List ONLY. Nested IfStmts inside another IfStmt's body do
	// NOT count — a constructor that quietly installs a noop fallback in a nested
	// branch must not be whitelisted just because some inner block contains a
	// fail-fast pattern. EachInChildren visits only direct children of fn.Body.
	// done/matched sentinel: EachInChildren has no early-exit return value;
	// the matched flag skips subsequent matches to preserve "find-first-and-stop"
	// semantics. Intentional GoCell pattern — closure+done family.
	matched := false
	scanner.EachInChildren[ast.IfStmt](fn.Body, func(ifStmt *ast.IfStmt) {
		if matched {
			return
		}
		if isFailFastReturn(ifStmt) {
			matched = true
		}
	})
	return matched
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
	// The body must contain at least one TOP-LEVEL return statement (direct
	// children of stmt.Body only) whose first result is nil and whose second
	// result is any non-nil expression. Nested returns inside another for/if/switch
	// inside stmt.Body are NOT recognized — a return buried inside `for { ... }`
	// is not the unconditional fail-fast pattern we whitelist.
	// EachInChildren visits only direct children of stmt.Body.
	// done/found sentinel: EachInChildren has no early-exit return value;
	// the found flag skips subsequent matches to preserve "find-first-and-stop"
	// semantics. Intentional GoCell pattern — closure+done family.
	found := false
	scanner.EachInChildren[ast.ReturnStmt](stmt.Body, func(ret *ast.ReturnStmt) {
		if found {
			return
		}
		if len(ret.Results) != 2 {
			return
		}
		if isNilIdent(ret.Results[0]) && !isNilIdent(ret.Results[1]) {
			found = true
		}
	})
	return found
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
	scanner.EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
		if !isOutboxEntryLiteral(pkg, lit) {
			return
		}
		policy := extractFailurePolicy(pkg, lit)
		if policy.safe() {
			return
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
		case route.unknown() || !route.present:
			violations = append(violations, outboxTopicViolation{
				Rule:    outboxTopicRuleFailOpen,
				File:    fileLabel,
				Line:    fset.Position(lit.Pos()).Line,
				Message: outboxUnknownRouteViolationMessage(policy),
			})
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
//
// EachInChildren visits only lit's direct children, so a same-named field
// nested inside a sub-struct (e.g. `Spec: SubSpec{Topic:"a"}`) does not
// pollute lit's reading.
func extractStringField(pkg *packages.Package, lit *ast.CompositeLit, fieldName string) outboxTopicFieldValue {
	var result outboxTopicFieldValue
	// done sentinel: EachInChildren has no early-exit return value;
	// the done flag skips subsequent matches to preserve "find-first-and-stop"
	// semantics. Intentional GoCell pattern — closure+done family.
	done := false
	scanner.EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		if done {
			return
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			return
		}
		value, ok := typeseval.EvaluateConstString(pkg.TypesInfo, kv.Value)
		result = outboxTopicFieldValue{present: true, ok: ok, value: value}
		done = true
	})
	return result
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
//
// EachInChildren visits only lit's direct children, so a FailurePolicy buried
// inside a nested struct is not hoisted to lit's level.
func extractFailurePolicy(pkg *packages.Package, lit *ast.CompositeLit) outboxFailurePolicyStatus {
	result := outboxPolicyAbsent
	// done sentinel: EachInChildren has no early-exit return value;
	// the done flag skips subsequent matches to preserve "find-first-and-stop"
	// semantics. Intentional GoCell pattern — closure+done family.
	done := false
	scanner.EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		if done {
			return
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != outboxTopicForbiddenPolicyField {
			return
		}
		if isOutboxFailOpenConst(pkg.TypesInfo, kv.Value) {
			result = outboxPolicyKnownFailOpen
		} else if isKnownOutboxFailurePolicyConst(pkg.TypesInfo, kv.Value) {
			result = outboxPolicyKnownOther
		} else {
			result = outboxPolicyUnknown
		}
		done = true
	})
	return result
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

	// scanner.ModuleScope auto-skips vendor/testdata/worktrees/generated/.git/node_modules.
	// kernel/metautil (canonical home) and tools/archtest/ are excluded via rel-prefix
	// check inside the loop — these are directory prefixes, not exact file paths.
	scope := scanner.ModuleScope(repoRoot)
	allFiles, err := scope.Files()
	require.NoError(t, err, "enumerate repo files")

	for _, path := range allFiles {
		rel, _ := filepath.Rel(repoRoot, path)
		rel = filepath.ToSlash(rel)
		// Skip the canonical home (kernel/metautil) — that is where these consts live.
		if strings.HasPrefix(rel, "kernel/metautil/") {
			continue
		}
		// Skip the archtest package itself (contains these names as string literals).
		if strings.HasPrefix(rel, "tools/archtest/") {
			continue
		}
		fset := token.NewFileSet()
		// Syntactically broken files are out of scope for this rule — gofmt /
		// build invariants own that contract. Discard the parse error.
		file, _ := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if file == nil {
			continue
		}
		scanner.EachInSubtree[ast.GenDecl](file, func(gen *ast.GenDecl) {
			if gen.Tok != token.CONST {
				return
			}
			scanner.EachInSubtree[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
				for _, name := range vs.Names {
					if _, bad := forbidden[name.Name]; !bad {
						continue
					}
					hits = append(hits, hit{
						File:  rel,
						Line:  fset.Position(name.Pos()).Line,
						Const: name.Name,
					})
				}
			})
		})
	}

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

// ---------------------------------------------------------------------------
// OUTBOX-HANDLERESULT-FIELDS-FROZEN-01
// ---------------------------------------------------------------------------

// handleResultAllowedFields is the verbatim field set of kernel/outbox.HandleResult.
// Adding a fifth field requires extending this allowlist deliberately, which
// is the moment to (a) re-read ADR 202605031900-adr-handler-vocabulary-collapse.md,
// (b) decide whether the new field belongs on the EntryHandler return contract
// or in ConsumerBase internal state, and (c) extend the Ack/Requeue/Reject
// factories in kernel/outbox/result.go if the field can be carried by them.
var handleResultAllowedFields = map[string]struct{}{
	"Disposition":         {},
	"Err":                 {},
	"ProcessReason":       {},
	"SettlementObservers": {},
}

// INVARIANT: OUTBOX-HANDLERESULT-FIELDS-FROZEN-01
//
// TestOutboxHandleResultFieldsFrozen enforces OUTBOX-HANDLERESULT-FIELDS-FROZEN-01:
// kernel/outbox.HandleResult must declare exactly the four fields listed in
// handleResultAllowedFields. The factories Ack/Requeue/Reject cover the two
// stable axes (Disposition, Err); ProcessReason and SettlementObservers are
// intentional fallback-literal escape hatches for kernel internal retry
// plumbing and middleware-handler protocol (see eventbus.md "回落字面量").
//
// Drift in this field set silently changes what every cell handler can/must
// produce, so freezing the set keeps the fallback intentional.
//
// Cannot funnel: HandleResult is a kernel-owned type whose literal construction
// is required by consumer_base.go internal plumbing — making fields unexported
// to force factory-only access would break that intra-package construction and
// the typed-test conformance harness. No schema/marker source can express
// "exactly these field names" as a Go compile-time constraint without
// regenerating the type itself, which loses hand-tuned tags and comments.
// Archtest is the minimum-friction gate.
func TestOutboxHandleResultFieldsFrozen(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "outbox", "outbox.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var (
		found   bool
		seen    = make(map[string]struct{})
		unknown []string
	)
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Name.Name != "HandleResult" {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		found = true
		for _, field := range st.Fields.List {
			// Embedded / anonymous field: field.Names is nil. Treat the embedded
			// type as a virtual field — it adds API surface that Ack/Requeue/Reject
			// cannot express, so it must go through allowlist review.
			if len(field.Names) == 0 {
				line := fset.Position(field.Type.Pos()).Line
				unknown = append(unknown, fmt.Sprintf("kernel/outbox/outbox.go:%d: <embedded field>", line))
				continue
			}
			for _, name := range field.Names {
				seen[name.Name] = struct{}{}
				if _, ok := handleResultAllowedFields[name.Name]; !ok {
					line := fset.Position(name.Pos()).Line
					unknown = append(unknown, fmt.Sprintf("kernel/outbox/outbox.go:%d: %s", line, name.Name))
				}
			}
		}
	})

	if !found {
		t.Fatalf("HandleResult struct definition not found in kernel/outbox/outbox.go " +
			"— if the type was relocated to another file in package outbox, update " +
			"this test's hardcoded path along with the move")
	}

	var missing []string
	for k := range handleResultAllowedFields {
		if _, ok := seen[k]; !ok {
			missing = append(missing, k)
		}
	}

	sort.Strings(unknown)
	sort.Strings(missing)
	for _, u := range unknown {
		t.Errorf("OUTBOX-HANDLERESULT-FIELDS-FROZEN-01: %s — field not in allowlist; "+
			"to add a field, update handleResultAllowedFields and review "+
			"ADR 202605031900-adr-handler-vocabulary-collapse.md plus the "+
			"Ack/Requeue/Reject factories in kernel/outbox/result.go", u)
	}
	for _, m := range missing {
		t.Errorf("OUTBOX-HANDLERESULT-FIELDS-FROZEN-01: required field %s missing from "+
			"kernel/outbox.HandleResult — removing a field changes the EntryHandler return "+
			"contract; review ADR 202605031900 and update handleResultAllowedFields "+
			"deliberately if the removal is intentional", m)
	}
}

// ---------------------------------------------------------------------------
// OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01
// ---------------------------------------------------------------------------

// handleResultLiteralAllowlist lists the production (non-_test.go) files that
// may construct outbox.HandleResult{...} composite literals. Every other
// production file must use the Ack/Requeue/Reject factories from
// kernel/outbox/result.go.
//
// Why these three:
//   - kernel/outbox/result.go         — defines the factories themselves.
//   - kernel/outbox/consumer_base.go  — kernel internal retry/settle plumbing
//     constructs HandleResult with ProcessReason / SettlementObservers, which
//     the factories do not expose (see eventbus.md "回落字面量").
//   - kernel/outbox/outboxtest/conformance.go — shared conformance harness;
//     non-_test.go by package convention but used only from test binaries.
//
// Adding a new entry requires the justification to live **next to the map
// entry below as a Go comment** (not in the file being scanned, since that
// file is the subject of the rule). The goal of FACTORY-PREFERRED-01 is to
// keep the fallback-literal surface intentionally small. New kernel/outbox
// files writing HandleResult literals are rare — extend this list
// deliberately.
var handleResultLiteralAllowlist = map[string]struct{}{
	"kernel/outbox/result.go":                 {},
	"kernel/outbox/consumer_base.go":          {},
	"kernel/outbox/outboxtest/conformance.go": {},
}

// INVARIANT: OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01
//
// TestOutboxHandleResultFactoryPreferred enforces
// OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01: production code (non-_test.go)
// must use kernel/outbox factories Ack/Requeue/Reject instead of constructing
// outbox.HandleResult{...} composite literals, except for the files in
// handleResultLiteralAllowlist (factories themselves, kernel internal
// plumbing, shared conformance harness).
//
// Test files (_test.go) are excluded by tests=false in
// typeseval.SharedResolver; vendor/, testdata/ are skipped by go list
// module-load defaults; generated/ is skipped via
// typeseval.IsGeneratedRelPath (NOT by go list — `go list ./...` does
// include generated/contracts/.../v1 packages, so the rule must apply
// the path filter explicitly. Closes PR445-FU finding F4).
//
// Type-aware via go/types: the scanner detects two literal forms via
// pkg.TypesInfo:
//  1. `<alias>.HandleResult{...}` where the SelectorExpr.X resolves to a
//     *types.PkgName whose Imported().Path() is kernel/outbox (renamed
//     imports are handled authoritatively by the type checker).
//  2. Bare `HandleResult{...}` when the file's package itself is the
//     kernel/outbox package (covers any future kernel/outbox/*.go file).
//
// Cannot funnel: ProcessReason and SettlementObservers are runtime-determined
// fields populated by handler code paths and middleware, with no schema /
// marker source from which a literal-vs-factory choice can be derived. Type
// system cannot express "callers in cells/ must use these three function
// names" without unexporting HandleResult itself, which would break the
// kernel-internal literal construction the allowlist exists to permit.
// Archtest enforces the path discipline that no other layer can.
//
// Closes PR445-FU-PACKAGEALIASES-TYPE-AWARE-01 for this rule.
func TestOutboxHandleResultFactoryPreferred(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	resolver, err := typeseval.SharedResolver(root, false, nil, "./...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	const outboxImportPath = "github.com/ghbvf/gocell/kernel/outbox"

	var violations []string
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			// Allowlist (explicit opt-in) is checked first so the kernel
			// internal sites in handleResultLiteralAllowlist remain
			// permitted; generated/ skip (implicit opt-out) is then
			// applied for codegen output that must never be flagged.
			if _, ok := handleResultLiteralAllowlist[rel]; ok {
				continue
			}
			if typeseval.IsGeneratedRelPath(rel) {
				continue
			}
			violations = append(violations,
				scanForHandleResultLiterals(pkg, file, rel, outboxImportPath)...)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01: %s — use outbox.Ack() / "+
			"outbox.Requeue(err) / outbox.Reject(err) instead of constructing the "+
			"struct literal; if you genuinely need ProcessReason or SettlementObservers, "+
			"extend handleResultLiteralAllowlist with a code-comment justification", v)
	}
}

// TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3 anchors
// the load-vs-skip decision contract for the HandleResult factory rule.
//
// Anchor (informational, not a TDD RED): documents that
// `typeseval.SharedResolver(root, false, nil, "./...")` DOES include
// generated/ packages, contradicting the comment block above
// TestOutboxHandleResultFactoryPreferred which claims `go list ./...`
// default-skips generated/. Wave 3 introduces typeseval.IsGeneratedRelPath
// + applies it before scanForHandleResultLiterals so the rule no longer
// scans generated/ paths even though they ARE loaded. The Wave 3 commit
// also adds a fixture-driven sub-test that exercises the skip with a
// synthetic generated/-rel path containing a HandleResult literal.
//
// This test pins the load behavior so a future packages.Load default
// change (or a `cfg.BuildFlags=["-tags=nogen"]` style filter at the
// loader layer) doesn't silently mask the need for IsGeneratedRelPath.
func TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	resolver, err := typeseval.SharedResolver(root, false, nil, "./...")
	if err != nil {
		t.Fatalf("typeseval.SharedResolver: %v", err)
	}

	var generatedFiles []string
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			if typeseval.IsGeneratedRelPath(rel) {
				generatedFiles = append(generatedFiles, rel)
			}
		}
	}

	if len(generatedFiles) == 0 {
		t.Fatalf("anchor invalidated: SharedResolver(./...) loaded 0 generated/ files; " +
			"the rule's outdated comment claiming `go list ./...` default-skips generated/ " +
			"may now be accurate, but verify by running `go list ./... | grep ^github.com/ghbvf/gocell/generated/` " +
			"before removing the IsGeneratedRelPath skip")
	}
	t.Logf("anchor: SharedResolver(./...) loaded %d generated/ files — Wave 3's IsGeneratedRelPath must skip these", len(generatedFiles))
}

// scanForHandleResultLiterals scans file for HandleResult composite literals.
// Returns "<rel>:<line>" diagnostics. Files that neither import kernel/outbox
// nor declare package outbox produce no hits. Type-aware via pkg.TypesInfo.
func scanForHandleResultLiterals(pkg *packages.Package, file *ast.File, rel, outboxImportPath string) []string {
	inPackageOutbox := pkg.PkgPath == outboxImportPath
	var hits []string
	scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		switch tn := cl.Type.(type) {
		case *ast.SelectorExpr:
			ident, ok := tn.X.(*ast.Ident)
			if !ok || tn.Sel == nil || tn.Sel.Name != "HandleResult" {
				return
			}
			pkgName, isPkg := pkg.TypesInfo.Uses[ident].(*types.PkgName)
			if !isPkg {
				return
			}
			if pkgName.Imported().Path() != outboxImportPath {
				return
			}
			pos := pkg.Fset.Position(cl.Pos())
			hits = append(hits, fmt.Sprintf("%s:%d: %s.HandleResult{} literal", rel, pos.Line, ident.Name))
		case *ast.Ident:
			if !inPackageOutbox || tn.Name != "HandleResult" {
				return
			}
			pos := pkg.Fset.Position(cl.Pos())
			hits = append(hits, fmt.Sprintf("%s:%d: HandleResult{} literal in package outbox", rel, pos.Line))
		}
	})
	return hits
}
