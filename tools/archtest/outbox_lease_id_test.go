package archtest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

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
	root := orFindModuleRoot(t)
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

// TestOutboxMarkReturnsBool01 enforces OUTBOX-MARK-RETURNS-BOOL-01: every
// runtime/outbox/relay*.go non-test caller of MarkPublished/MarkRetry/MarkDead
// MUST bind the `updated bool` return to a named identifier. Discarding it via
// `_, err :=` would re-open B2-A-05: stale-lease CAS misses get silently
// miscounted as successes. Glob coverage so future writeBack split (e.g.
// relay_writeback.go) cannot escape this gate by file rename.
func TestOutboxMarkReturnsBool01(t *testing.T) {
	root := orFindModuleRoot(t)
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

// TestOutboxMetadataMaxBytes01 enforces OUTBOX-METADATA-MAX-BYTES-01: the
// adapters/postgres outbox writer must reference the MaxMetadataBytes constant
// in both Write and writeBatchChunk, gating the JSON-marshaled metadata size
// before it reaches the INSERT statement.
func TestOutboxMetadataMaxBytes01(t *testing.T) {
	root := orFindModuleRoot(t)
	path := filepath.Join(root, "adapters", "postgres", "outbox_writer.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	required := map[string]bool{"Write": false, "writeBatchChunk": false}

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Body == nil {
			continue
		}
		if _, want := required[fn.Name.Name]; !want {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if id.Name == "MaxMetadataBytes" {
				required[fn.Name.Name] = true
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
