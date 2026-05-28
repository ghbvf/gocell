package archtest

// errcode_invariants_test.go consolidates errcode-theme invariants:
//   - INVARIANT: ERRCODE-KIND-LITERAL-01
//   - INVARIANT: ERRCODE-CARVEOUT-ADR-CONSISTENCY-01
//   - INVARIANT: MESSAGE-CONST-LITERAL-01
//   - INVARIANT: ERROR-FIRST-API-01
//   - INVARIANT: ERROR-FIRST-TYPED-NIL-01
//   - INVARIANT: EXPORTED-ERROR-NEW-01
//   - INVARIANT: DETAILS-SEALED-FIELD-FROZEN-01
//
// DETAILS-SLOG-ATTR-01 retired by PR #1035: sealed PublicDetail newtype
// (pkg/errcode/details.go) makes wire-unsafe construction inexpressible
// in Go's type system. See ADR docs/architecture/202605051730-adr-errcode-message-pii-safety.md.
// DETAILS-SEALED-FIELD-FROZEN-01 is the reflect-based field lock that
// guards the same invariant against in-package drift (re-exporting either
// the carrier struct fields or the publicValue marker interface).

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ─── errcode_constructor constants ───────────────────────────────────────────

// errcodeImportPath is the canonical import path of the errcode package
// (used by errcodeImportNames for unquoted import-path comparison).
const errcodeImportPath = "github.com/ghbvf/gocell/pkg/errcode"

// ─── errcode_message_const constants ─────────────────────────────────────────

const ruleMessageConstLiteral01 = "MESSAGE-CONST-LITERAL-01"

// errcodeMessageAllowlist exempts pkg/errcode/ from the gate: the package
// defines New / Wrap and tests them with non-literal messages, and Assertion
// deliberately formats runtime context into Message (recorded in the
// constructor's godoc as a documented exception).
const errcodeMessageAllowlist = "pkg/errcode/"

// errcodeMessageTestdataAllowlist exempts archtest fixtures (where
// violations are intentional regression cases).
const errcodeMessageTestdataAllowlist = "tools/archtest/testdata/"

// errcodePackagePath is the canonical import path of the constructors
// targeted by this gate.
const errcodePackagePath = "github.com/ghbvf/gocell/pkg/errcode"

// httputilPackagePath / ctxcancelPackagePath are the additional helpers
// gated by this rule. Each helper accepts a caller-supplied message that
// flows directly into errcode.Error.Message; PR #391 review (P2) noted
// the prior carve-outs in their bodies (struct literal, no errcode.New
// involvement) created a static blind spot that this extension closes.
const (
	httputilPackagePath  = "github.com/ghbvf/gocell/pkg/httputil"
	ctxcancelPackagePath = "github.com/ghbvf/gocell/pkg/ctxcancel"
)

// gatedCallee describes one message-receiving entry point checked by the
// rule. messageArgIndex is the position of the message string in the
// argument list:
//   - errcode.New(kind, code, message, opts...)            → 2
//   - errcode.Wrap(kind, code, message, cause, opts...)    → 2
//   - httputil.WritePublic(ctx, w, kind, code, message)    → 4
//   - ctxcancel.WrapOrInfra(err, op, id, code, fallbackMsg) → 4
type gatedCallee struct {
	pkgPath         string
	name            string
	messageArgIndex int
	displayName     string // shown in violation messages, e.g. "httputil.WritePublic"
}

var messageGatedCallees = []gatedCallee{
	{pkgPath: errcodePackagePath, name: "New", messageArgIndex: 2, displayName: "errcode.New"},
	{pkgPath: errcodePackagePath, name: "Wrap", messageArgIndex: 2, displayName: "errcode.Wrap"},
	{pkgPath: httputilPackagePath, name: "WritePublic", messageArgIndex: 4, displayName: "httputil.WritePublic"},
	{pkgPath: ctxcancelPackagePath, name: "WrapOrInfra", messageArgIndex: 4, displayName: "ctxcancel.WrapOrInfra"},
}

// fixtureASTPackageNames maps the local-import name a fixture file uses
// (selector.X.Name) back to the canonical gatedCallee's displayName, for
// fixture-mode AST scanning where TypesInfo is unavailable. Each fixture
// imports the helper as the top-level package name.
var fixtureASTPackageNames = map[string]struct{}{
	"errcode":   {},
	"httputil":  {},
	"ctxcancel": {},
}

// ─── error_first constants ────────────────────────────────────────────────────

const (
	ruleErrorFirstAPI01      = "ERROR-FIRST-API-01"
	ruleErrorFirstTypedNil01 = "ERROR-FIRST-TYPED-NIL-01"
)

// errorFirstEnforcedFiles are the relative paths (from module root) of files
// whose declarations must satisfy ERROR-FIRST-API-01. Slash-separated for
// portability; converted with filepath.FromSlash before stat.
var errorFirstEnforcedFiles = []string{
	"kernel/wrapper/handler.go",
	"kernel/wrapper/consumer.go",
	"kernel/contractspec/spec.go",
	"kernel/wrapper/lifecycle.go",
	"kernel/auth/auth_plan.go",
	"kernel/outbox/entry_id.go",
	"kernel/outbox/envelope.go",
	"kernel/idempotency/inmem.go",
	"kernel/worker/worker.go",
	"runtime/eventrouter/router.go",
	"runtime/eventrouter/contract_tracing_subscriber.go",
	"runtime/auth/route.go",
	"runtime/worker/worker.go",
	"runtime/distlock/locker.go",
	"runtime/auth/refresh/memstore/store.go",
	"runtime/http/middleware/circuit_breaker.go",
	"runtime/http/health/health.go",
	"runtime/http/router/router.go",
	"kernel/persistence/tx.go",
	"cells/accesscore/slices/sessionlogin/service.go",
	"cells/accesscore/slices/sessionrefresh/service.go",
	"cells/accesscore/slices/sessionlogout/service.go",
	"adapters/postgres/refresh_store.go",
}

// This list is functionally a subset of ADR 202604270030 §4.1 C-class re-throw sites.
// ERROR-FIRST-API-01 needs it because those four sites are inside error-less functions
// (recover defers) and must be permitted to panic; PANIC-REGISTERED-01 handles the
// panic-shape concern orthogonally via panicregister.Approved wrap.
//
// errorFirstPanicWhitelist exempts ADR-approved C-class re-throw functions
// from ERROR-FIRST-API-01. These are error-less functions that contain a
// panic() as a deliberate re-throw of a recovered value; they are exempt from
// the "error-less function must not panic" rule but still require
// panicregister.Approved wrap (enforced by PANIC-REGISTERED-01).
//
// Key format: "<rel-path>::<funcName>".
var errorFirstPanicWhitelist = map[string]struct{}{
	"kernel/wrapper/lifecycle.go::recoverAndFinish":                          {},
	"runtime/http/middleware/circuit_breaker.go::repanicAfterBreakerFailure": {},
	"adapters/postgres/tx_manager.go::repanicAfterTopLevelTxRollback":        {},
	"adapters/postgres/tx_manager.go::repanicAfterSavepointRollback":         {},
}

// ─── errcode_kind_literal carve-out registry ─────────────────────────────────

// carveOut identifies a single function-level carve-out for ERRCODE-KIND-LITERAL-01.
// The key format mirrors errorFirstPanicWhitelist: "<rel-path>::<funcName>".
// Keyed as a struct so map lookup is typed and cannot accidentally match
// on a partial path or name.
type carveOut struct{ rel, fn string }

// errcodeKindLiteralCarveOuts is the authoritative code-side list of functions
// permitted to construct errcode.Error{} struct literals directly.
//
// This map must be kept in strict equality with the CARVEOUT-REGISTRY table in
// docs/architecture/202605121800-adr-archtest-carveout-narrow.md.
// Any drift (code-only or ADR-only entry) is detected by
// ERRCODE-CARVEOUT-ADR-CONSISTENCY-01 and causes CI to turn red.
//
// To add or remove a carve-out, update BOTH this map AND the ADR registry
// table in the same PR. Attempting either in isolation will fail CI.
var errcodeKindLiteralCarveOuts = map[carveOut]struct{}{
	{rel: "pkg/ctxcancel/ctxcancel.go", fn: "WrapOrInfra"}: {},
	{rel: "pkg/httputil/response.go", fn: "WritePublic"}:   {},
}

// ─── exported_error_new constants ────────────────────────────────────────────

const ruleExportedErrorNew01 = "EXPORTED-ERROR-NEW-01"

// errcodeAllowlistPath is the canonical home of low-level sentinel errors;
// the gate exempts it because pkg/errcode is the migration destination and
// itself wraps errors.New internally.
const errcodeAllowlistPath = "pkg/errcode/"

// INVARIANT: ERRCODE-KIND-LITERAL-01
//
// TestErrcodeLiteralConstructionBanned seals the Kind-based error model:
// callers outside pkg/errcode must use errcode.New/Wrap so every error chooses
// a transport Kind explicitly.
//
// Carve-outs are function-level only (see errcodeKindLiteralCarveOuts).
// File-level skips are intentionally removed — any new errcode.Error{} literal
// outside a carved function, even in the same file, is a violation.
func TestErrcodeLiteralConstructionBanned(t *testing.T) {
	root := findModuleRoot(t)

	// Package-level scope skip (NOT a carve-out): only pkg/errcode/ itself.
	// Function-level carve-outs are applied inside findErrcodeErrorLiteralsPass
	// via errcodeKindLiteralCarveOuts.
	errcodeKindAllowedRel := func(rel string) bool {
		// pkg/errcode/ is the definition site of errcode.Error; its New/Wrap
		// implementation legitimately constructs the struct literal. This is a
		// package-level skip, not a carve-out. Everything else outside the
		// function-level errcodeKindLiteralCarveOuts entries is a violation.
		return strings.HasPrefix(rel, "pkg/errcode/")
	}

	diags := Run(t, ModuleScope(root, MatchRels(func(rel string) bool {
		return !errcodeKindAllowedRel(rel)
	})), findErrcodeErrorLiteralsPass)

	Report(t, "ERRCODE-KIND-LITERAL-01", diags)
}

// findErrcodeErrorLiteralsPass reports errcode.Error composite literals
// constructed outside the function-level carve-outs declared in
// errcodeKindLiteralCarveOuts. It delegates to scanErrcodeErrorLiteralsInAST
// so the production scan and the TestFindErrcodeErrorLiteralsFunctionLevel
// red-fixture proof share one carve-out-aware core — a single funnel with no
// file-level allowlist and no parallel disk-read path.
func findErrcodeErrorLiteralsPass(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		for _, h := range scanErrcodeErrorLiteralsInAST(p.Fset, file, rel, errcodeKindLiteralCarveOuts) {
			out = append(out, Diagnostic{
				Rel:     rel,
				Line:    h.line,
				Message: fmt.Sprintf("%s constructs errcode.Error directly; use errcode.New/Wrap", rel),
			})
		}
	}
	return out
}

// errcodeErrorHit records a detected errcode.Error{} literal with its line number.
type errcodeErrorHit struct {
	line int
}

// scanErrcodeErrorLiteralsInAST is the unit-testable core of the scanner.
// It takes a parsed *ast.File, the module-relative path (used only for
// carve-out lookup), and the carve-out map, and returns hits for every
// errcode.Error composite literal that is NOT in a carved function.
//
// AST forms OUTSIDE the declared detection range (pure-AST isErrcodeErrorType):
//
//	(a) Aliased errcode import: `import ec "github.com/.../pkg/errcode"` + `ec.Error{}`
//	    — errcodeImportNames collects the alias, so aliased imports ARE detected.
//	    The blind spot is `ec.Error{}` when the alias is the same as another pkg;
//	    this cannot happen in practice (import-path uniqueness).
//	(b) Dot-import: `import . "github.com/.../pkg/errcode"` + `Error{}`
//	    — errcodeImportNames explicitly skips "." names (line: `imp.Name.Name != "."`).
//	    A dot-import `Error{}` will NOT be detected. errcodeDotImported (a
//	    pure-AST reverse self-check, no source-text prefilter) asserts no
//	    production file uses dot-import of pkg/errcode.
//	(c) Cross-package type-alias re-export: `type Error = errcode.Error` in a
//	    third package + `thirdpkg.Error{}` — the SelectorExpr.X.Name would be
//	    the third-package alias, not in errcodeNames; NOT detected.
//	    errcodeErrorAliasReexports (pure-AST, resolves the qualifier through
//	    errcodeImportNames so an aliased import is caught too) asserts no
//	    production file re-exports errcode.Error as a type alias.
func scanErrcodeErrorLiteralsInAST(fset *token.FileSet, f *ast.File, rel string, carveOuts map[carveOut]struct{}) []errcodeErrorHit {
	errcodeNames := errcodeImportNames(f)
	if len(errcodeNames) == 0 {
		return nil
	}

	var hits []errcodeErrorHit

	// Collect carved function body position ranges so we can skip literals inside.
	type posRange struct{ lo, hi token.Pos }
	var carvedRanges []posRange
	scanner.EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		// Carve-outs are package-level functions only. The ADR registry has
		// no receiver dimension; a method sharing a carved function's name
		// (e.g. `func (T) WritePublic`) must NOT inherit the exemption.
		if fd.Recv != nil {
			return
		}
		key := carveOut{rel: rel, fn: fd.Name.Name}
		if _, ok := carveOuts[key]; ok {
			carvedRanges = append(carvedRanges, posRange{fd.Body.Pos(), fd.Body.End()})
		}
	})

	inCarvedRange := func(pos token.Pos) bool {
		for _, r := range carvedRanges {
			if pos >= r.lo && pos < r.hi {
				return true
			}
		}
		return false
	}

	scanner.EachInSubtree[ast.CompositeLit](f, func(lit *ast.CompositeLit) {
		if !isErrcodeErrorType(lit.Type, errcodeNames) {
			return
		}
		if inCarvedRange(lit.Pos()) {
			return
		}
		hits = append(hits, errcodeErrorHit{fset.Position(lit.Pos()).Line})
	})
	return hits
}

// INVARIANT: ERRCODE-CARVEOUT-ADR-CONSISTENCY-01
//
// TestErrcodeCarveOutADRConsistency enforces strict equality between the
// code-side errcodeKindLiteralCarveOuts map and the CARVEOUT-REGISTRY table
// in docs/architecture/202605121800-adr-archtest-carveout-narrow.md.
//
// Parser contract: locate <!-- CARVEOUT-REGISTRY:BEGIN --> and
// <!-- CARVEOUT-REGISTRY:END --> anchors; between them skip the header row
// (| Rule | File | Function |...) and the |---|...| separator row; for each
// remaining |-delimited row, trim-space col 2 (File) and col 3 (Function).
//
// Failure message lists code-only and ADR-only entries for readability.
func TestErrcodeCarveOutADRConsistency(t *testing.T) {
	root := findModuleRoot(t)
	adrPath := filepath.Join(root, "docs", "architecture", "202605121800-adr-archtest-carveout-narrow.md")

	adrSet, err := parseCarveOutADRRegistry(adrPath)
	require.NoError(t, err, "parse carve-out ADR registry")

	// Build code-side set.
	codeSet := make(map[carveOut]struct{}, len(errcodeKindLiteralCarveOuts))
	for k := range errcodeKindLiteralCarveOuts {
		codeSet[k] = struct{}{}
	}

	// Find ADR-only entries.
	var adrOnly []string
	for k := range adrSet {
		if _, ok := codeSet[k]; !ok {
			adrOnly = append(adrOnly, fmt.Sprintf("  ADR has {%s::%s} but code map does not", k.rel, k.fn))
		}
	}
	sort.Strings(adrOnly)

	// Find code-only entries.
	var codeOnly []string
	for k := range codeSet {
		if _, ok := adrSet[k]; !ok {
			codeOnly = append(codeOnly, fmt.Sprintf("  code map has {%s::%s} but ADR does not", k.rel, k.fn))
		}
	}
	sort.Strings(codeOnly)

	var msgs []string
	msgs = append(msgs, adrOnly...)
	msgs = append(msgs, codeOnly...)
	for _, m := range msgs {
		t.Log(m)
	}
	assert.Empty(t, msgs,
		"ERRCODE-CARVEOUT-ADR-CONSISTENCY-01: errcodeKindLiteralCarveOuts and the ADR "+
			"registry table in docs/architecture/202605121800-adr-archtest-carveout-narrow.md "+
			"must be in strict equality. Update BOTH in the same PR.")
}

// parseCarveOutADRRegistry reads the ADR file at path and extracts the
// (File, Function) pairs from the CARVEOUT-REGISTRY table.
//
// It requires the <!-- CARVEOUT-REGISTRY:BEGIN --> and <!-- CARVEOUT-REGISTRY:END -->
// markers to be present. Between them the first in-table line must be the header row
// (starting with "| Rule") and the second must be the separator row (starting with
// "|---"). Missing or mismatched structural rows are hard errors with diagnostics.
func parseCarveOutADRRegistry(path string) (map[carveOut]struct{}, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open ADR: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file; close error is not actionable

	const beginMarker = "<!-- CARVEOUT-REGISTRY:BEGIN -->"
	const endMarker = "<!-- CARVEOUT-REGISTRY:END -->"

	result := make(map[carveOut]struct{})
	inTable := false
	beginSeen := false
	endSeen := false
	headerValidated := false
	separatorValidated := false

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == beginMarker {
			inTable = true
			beginSeen = true
			continue
		}
		if strings.TrimSpace(line) == endMarker {
			endSeen = true
			break
		}
		if !inTable {
			continue
		}
		// Validate and skip header row: | Rule | File | Function | ...
		if !headerValidated {
			if !strings.HasPrefix(line, "| Rule") {
				return nil, fmt.Errorf(
					"parseCarveOutADRRegistry: %s: unexpected registry table structure at %q;"+
						" expected header row then |---| separator", path, line)
			}
			headerValidated = true
			continue
		}
		// Validate and skip separator row: |---|---|...
		if !separatorValidated {
			if !strings.HasPrefix(line, "|---") {
				return nil, fmt.Errorf(
					"parseCarveOutADRRegistry: %s: unexpected registry table structure at %q;"+
						" expected header row then |---| separator", path, line)
			}
			separatorValidated = true
			continue
		}
		// Parse data row: | Rule | File | Function | Reason |
		cols := strings.Split(line, "|")
		// cols[0] is empty (before first |), cols[1]=Rule, cols[2]=File, cols[3]=Function, cols[4]=Reason, cols[5]=empty
		if len(cols) < 5 {
			continue
		}
		rel := strings.TrimSpace(cols[2])
		fn := strings.TrimSpace(cols[3])
		if rel == "" || fn == "" {
			continue
		}
		result[carveOut{rel: rel, fn: fn}] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan ADR: %w", err)
	}
	if !beginSeen {
		return nil, fmt.Errorf("parseCarveOutADRRegistry: %s: CARVEOUT-REGISTRY:BEGIN marker not found", path)
	}
	if !endSeen {
		return nil, fmt.Errorf("parseCarveOutADRRegistry: %s: CARVEOUT-REGISTRY:END marker not found", path)
	}
	return result, nil
}

// TestParseCarveOutADRRegistry is a table-driven unit test for parseCarveOutADRRegistry.
// It verifies: well-formed table → entries parsed; missing BEGIN → error;
// blank line where header expected → error; missing END marker → error.
func TestParseCarveOutADRRegistry(t *testing.T) {
	wellFormed := `# ADR
<!-- CARVEOUT-REGISTRY:BEGIN -->
| Rule | File | Function | Reason |
|---|---|---|---|
| R1 | pkg/a/a.go | FuncA | reason A |
| R2 | pkg/b/b.go | FuncB | reason B |
<!-- CARVEOUT-REGISTRY:END -->
`
	tests := []struct {
		name     string
		content  string
		wantErr  string
		wantKeys []carveOut
	}{
		{
			name:    "well-formed table: 2 rows parsed",
			content: wellFormed,
			wantKeys: []carveOut{
				{rel: "pkg/a/a.go", fn: "FuncA"},
				{rel: "pkg/b/b.go", fn: "FuncB"},
			},
		},
		{
			name: "missing BEGIN marker: error",
			content: `# ADR
| Rule | File | Function | Reason |
|---|---|---|---|
<!-- CARVEOUT-REGISTRY:END -->
`,
			wantErr: "CARVEOUT-REGISTRY:BEGIN marker not found",
		},
		{
			name: "blank line where header expected: error",
			content: `# ADR
<!-- CARVEOUT-REGISTRY:BEGIN -->

| Rule | File | Function | Reason |
|---|---|---|---|
<!-- CARVEOUT-REGISTRY:END -->
`,
			wantErr: "unexpected registry table structure",
		},
		{
			name: "missing END marker: error",
			content: `# ADR
<!-- CARVEOUT-REGISTRY:BEGIN -->
| Rule | File | Function | Reason |
|---|---|---|---|
| R1 | pkg/a/a.go | FuncA | reason A |
`,
			wantErr: "CARVEOUT-REGISTRY:END marker not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Write content to a temp file so parseCarveOutADRRegistry can open it.
			tmp, err := os.CreateTemp(t.TempDir(), "adr-*.md")
			require.NoError(t, err)
			_, err = tmp.WriteString(tc.content)
			require.NoError(t, err)
			require.NoError(t, tmp.Close())

			got, err := parseCarveOutADRRegistry(tmp.Name())
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			gotSet := make(map[carveOut]struct{}, len(got))
			for k := range got {
				gotSet[k] = struct{}{}
			}
			for _, k := range tc.wantKeys {
				assert.Contains(t, gotSet, k, "expected key %v in parsed result", k)
			}
			assert.Len(t, got, len(tc.wantKeys))
		})
	}
}

// TestFindErrcodeErrorLiteralsFunctionLevel is a table-driven unit test for
// scanErrcodeErrorLiteralsInAST. It uses in-memory source strings to verify:
//   - errcode.Error{} inside a carved func is NOT reported
//   - errcode.Error{} inside a non-carved func in the same file IS reported
//   - empty carve-out map → carved-func site IS reported (proves guard is active)
//   - package-level (GenDecl) errcode.Error{} is ALWAYS reported (never carved)
//
// Blind-spot self-check: AST forms outside isErrcodeErrorType's declared range:
//
//	(a) Aliased import + construction: `import ec "...errcode"` + `ec.Error{}`
//	    — IS detected because errcodeImportNames collects aliases.
//	(b) Dot-import: `import . "...errcode"` + `Error{}`
//	    — NOT detected (errcodeImportNames skips "." alias).
//	    Reverse self-check (pure-AST, errcodeDotImported): assert no
//	    production file dot-imports errcode.
//	(c) Cross-pkg type-alias: `type MyErr = errcode.Error` in pkg B + `B.MyErr{}`
//	    — NOT detected (SelectorExpr.X.Name is B, not in errcodeNames).
//	    Reverse self-check (pure-AST, errcodeErrorAliasReexports; resolves
//	    aliased imports too): assert no production file re-exports it as alias.
func TestFindErrcodeErrorLiteralsFunctionLevel(t *testing.T) {
	const errcodeImport = `"github.com/ghbvf/gocell/pkg/errcode"`

	makeSrc := func(body string) string {
		return `package p
import ` + errcodeImport + `
` + body
	}

	tests := []struct {
		name      string
		src       string
		rel       string
		carveOuts map[carveOut]struct{}
		wantLines []int // nil or empty = expect no hits
	}{
		{
			name: "carved func: errcode.Error literal suppressed",
			src: makeSrc(`func WrapOrInfra() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/ctxcancel/ctxcancel.go",
			carveOuts: map[carveOut]struct{}{{rel: "pkg/ctxcancel/ctxcancel.go", fn: "WrapOrInfra"}: {}},
			wantLines: nil,
		},
		{
			name: "non-carved func in same file: errcode.Error literal reported",
			src: makeSrc(`func WrapOrInfra() {
	_ = errcode.Error{}
}
func OtherFunc() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/ctxcancel/ctxcancel.go",
			carveOuts: map[carveOut]struct{}{{rel: "pkg/ctxcancel/ctxcancel.go", fn: "WrapOrInfra"}: {}},
			// package p=1, import=2, WrapOrInfra decl=3, carved literal=4, }=5, OtherFunc decl=6, literal=7
			wantLines: []int{7},
		},
		{
			name: "empty carve-out map: WrapOrInfra site IS reported",
			src: makeSrc(`func WrapOrInfra() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/ctxcancel/ctxcancel.go",
			carveOuts: map[carveOut]struct{}{},
			wantLines: []int{4}, // package p=1, import=2, func decl=3, literal=4
		},
		{
			name:      "package-level errcode.Error literal: always reported (never carved)",
			src:       makeSrc(`var _ = errcode.Error{}`),
			rel:       "some/file.go",
			carveOuts: map[carveOut]struct{}{{rel: "some/file.go", fn: "someFunc"}: {}},
			wantLines: []int{3},
		},
		{
			name:      "no errcode import: no hits",
			src:       `package p; func F() {}`,
			rel:       "some/file.go",
			carveOuts: map[carveOut]struct{}{},
			wantLines: nil,
		},
		{
			// Finding F2 regression: a method whose name collides with a
			// carved package-level function must NOT inherit the carve-out.
			name: "method sharing carve-out name: NOT exempt (function-level only)",
			src: makeSrc(`func WritePublic() {
	_ = errcode.Error{}
}
type T struct{}
func (T) WritePublic() {
	_ = errcode.Error{}
}`),
			rel:       "pkg/httputil/response.go",
			carveOuts: map[carveOut]struct{}{{rel: "pkg/httputil/response.go", fn: "WritePublic"}: {}},
			// p=1 import=2 funcWritePublic=3 carvedLit=4 }=5 typeT=6 method=7 lit=8 }=9
			wantLines: []int{8},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, tc.rel, tc.src, parser.SkipObjectResolution)
			require.NoError(t, err)

			hits := scanErrcodeErrorLiteralsInAST(fset, f, tc.rel, tc.carveOuts)
			var gotLines []int
			for _, h := range hits {
				gotLines = append(gotLines, h.line)
			}
			assert.Equal(t, tc.wantLines, gotLines)
		})
	}

	// ── Reverse self-checks for declared blind spots (b) dot-import and
	//    (c) cross-pkg type-alias re-export. Pure-AST, no string prefilter:
	//    a prefilter anchored on usage-site text (`= errcode.Error`) is blind
	//    to aliased imports (`import ec ".../errcode"; type X = ec.Error`),
	//    which is exactly the form the AST logic must catch. One walk; the
	//    AST visit is gated cheaply by errcodeImportNames inside each helper.
	t.Run("reverse-self-check: no dot-import / type-alias re-export of pkg/errcode in production", func(t *testing.T) {
		root := findModuleRoot(t)
		files, err := collectGoFiles(root)
		require.NoError(t, err)

		var dotImports, aliasReexports []string
		for _, file := range files {
			rel, _ := filepath.Rel(root, file)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "pkg/errcode/") {
				continue
			}
			if !fileroles.IsProductionCode(rel) {
				continue
			}
			data, err := os.ReadFile(filepath.Clean(file))
			require.NoError(t, err)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, data, parser.SkipObjectResolution)
			require.NoError(t, err, "parse %s", rel)
			if errcodeDotImported(f) {
				dotImports = append(dotImports, rel)
			}
			for _, name := range errcodeErrorAliasReexports(f) {
				aliasReexports = append(aliasReexports,
					fmt.Sprintf("%s: type %s = errcode.Error", rel, name))
			}
		}
		assert.Empty(t, dotImports,
			"production files must not dot-import pkg/errcode "+
				"(blind spot: dot-import escapes errcodeImportNames detection)")
		assert.Empty(t, aliasReexports,
			"production files must not re-export errcode.Error as a type alias "+
				"(blind spot: cross-pkg alias escapes pure-AST detection)")
	})
}

// TestErrcodeBlindSpotHelpers is the regression coverage for Finding F1:
// the dot-import and type-alias blind-spot detectors must work purely from
// the AST, with no source-text prefilter. The decisive case is
// "aliased import + alias re-export" — a `= errcode.Error` string prefilter
// would have skipped the AST parse entirely and missed it.
func TestErrcodeBlindSpotHelpers(t *testing.T) {
	parse := func(t *testing.T, src string) *ast.File {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
		require.NoError(t, err)
		return f
	}

	t.Run("errcodeDotImported", func(t *testing.T) {
		tests := []struct {
			name string
			src  string
			want bool
		}{
			{"dot-import detected", "package p\nimport . \"github.com/ghbvf/gocell/pkg/errcode\"\n", true},
			{"normal import not flagged", "package p\nimport \"github.com/ghbvf/gocell/pkg/errcode\"\n", false},
			{"aliased import not flagged", "package p\nimport ec \"github.com/ghbvf/gocell/pkg/errcode\"\nvar _ = ec.Error{}\n", false},
			{"no errcode import", "package p\nimport \"fmt\"\n", false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, errcodeDotImported(parse(t, tc.src)))
			})
		}
	})

	t.Run("errcodeErrorAliasReexports", func(t *testing.T) {
		tests := []struct {
			name string
			src  string
			want []string
		}{
			{
				name: "default import name re-export detected",
				src:  "package p\nimport \"github.com/ghbvf/gocell/pkg/errcode\"\ntype MyErr = errcode.Error\n",
				want: []string{"MyErr"},
			},
			{
				// Finding F1 lock: aliased import + alias re-export. A
				// `= errcode.Error` source prefilter never matches `= ec.Error`.
				name: "aliased import re-export detected",
				src:  "package p\nimport ec \"github.com/ghbvf/gocell/pkg/errcode\"\ntype MyErr = ec.Error\n",
				want: []string{"MyErr"},
			},
			{
				name: "type definition (not alias) not flagged",
				src:  "package p\nimport \"github.com/ghbvf/gocell/pkg/errcode\"\ntype MyErr errcode.Error\n",
				want: nil,
			},
			{
				name: "alias to other type not flagged",
				src:  "package p\nimport \"github.com/ghbvf/gocell/pkg/errcode\"\ntype C = errcode.Code\n",
				want: nil,
			},
			{
				name: "no errcode import: short-circuit, nil",
				src:  "package p\ntype Error = struct{}\n",
				want: nil,
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, errcodeErrorAliasReexports(parse(t, tc.src)))
			})
		}
	})
}

func errcodeImportNames(f *ast.File) map[string]struct{} {
	names := map[string]struct{}{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != errcodeImportPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				names[imp.Name.Name] = struct{}{}
			}
			continue
		}
		names["errcode"] = struct{}{}
	}
	return names
}

func isErrcodeErrorType(expr ast.Expr, errcodeNames map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Error" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = errcodeNames[pkg.Name]
	return ok
}

// errcodeDotImported reports whether f dot-imports pkg/errcode
// (`import . "github.com/ghbvf/gocell/pkg/errcode"`). Under a dot-import,
// `Error{}` has no selector, so it escapes isErrcodeErrorType (which only
// matches *ast.SelectorExpr). Production code must never dot-import errcode;
// this is blind spot (b)'s reverse self-check, computed from the import AST
// (alias-independent) rather than a source-text anchor.
func errcodeDotImported(f *ast.File) bool {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != errcodeImportPath {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			return true
		}
	}
	return false
}

// errcodeErrorAliasReexports returns the names of every package-level type
// alias `type X = <errcode>.Error` in f, where <errcode> is errcode's local
// import name (default "errcode" OR any explicit alias). Such a re-export
// lets a third package construct `thirdpkg.X{}`, whose SelectorExpr.X is the
// third package — invisible to the pure-AST scanner. This is blind spot
// (c)'s reverse self-check. It resolves the package qualifier through
// errcodeImportNames, so an aliased import (`import ec ".../errcode";
// type X = ec.Error`) is detected — the bug a `= errcode.Error` source-text
// prefilter would have missed. errcodeImportNames is empty when f does not
// import errcode at all, short-circuiting the AST walk cheaply.
func errcodeErrorAliasReexports(f *ast.File) []string {
	errcodeNames := errcodeImportNames(f)
	if len(errcodeNames) == 0 {
		return nil
	}
	var names []string
	scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if !ts.Assign.IsValid() {
			return // `type X T` (definition), not `type X = T` (alias)
		}
		sel, ok := ts.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Error" {
			return
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if _, inNames := errcodeNames[pkg.Name]; inNames {
			names = append(names, ts.Name.Name)
		}
	})
	return names
}

// INVARIANT: MESSAGE-CONST-LITERAL-01
//
// TestErrcodeMessageConstLiteral enforces MESSAGE-CONST-LITERAL-01.
//
// MESSAGE-CONST-LITERAL-01 — every call to `errcode.New(...)` and
// `errcode.Wrap(...)` in production code must pass a compile-time const
// literal as the third (`message`) argument. Runtime data (user input, IDs,
// counts, secrets) belongs in WithDetails (sealed PublicDetail via
// errcode.PublicString) or WithInternal (sealed InternalDetail, server-side
// only). The PII-safe default is enforced statically here so regression
// cannot reintroduce `fmt.Sprintf` / string-concatenation messages that
// leak runtime context onto the wire.
//
// ref: docs/architecture/202605051730-adr-errcode-message-pii-safety.md
func TestErrcodeMessageConstLiteral(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	visited := map[string]bool{}

	diags := RunTyped(t,
		TypedOpts{Tests: false, Tags: []string{"e2e", "integration", "pg"}},
		patterns,
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				abs := p.Abs(file)
				if visited[abs] {
					continue
				}
				visited[abs] = true

				rel := p.Rel(file)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if strings.HasPrefix(rel, errcodeMessageAllowlist) {
					continue
				}
				if strings.HasPrefix(rel, errcodeMessageTestdataAllowlist) {
					continue
				}
				out = append(out, scanErrcodeMessageASTDiags(p.Fset, file, rel, p.TypesInfo)...)
			}
			return out
		})

	Report(t, ruleMessageConstLiteral01, diags)
}

// scanErrcodeMessageASTDiags is the Diagnostic-returning form used by the
// TestErrcodeMessageConstLiteral Pass-funnel rule.
func scanErrcodeMessageASTDiags(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		callee, ok := resolveGatedCallee(call, info)
		if !ok {
			return
		}
		if len(call.Args) <= callee.messageArgIndex {
			return
		}
		msgArg := call.Args[callee.messageArgIndex]
		if isAcceptableMessageExpr(msgArg, info) {
			return
		}
		line := fset.Position(call.Pos()).Line
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"%s(...) message must be a const literal (got %T) "+
					"— move runtime data to WithDetails(errcode.PublicString/PublicInt/PublicBool/PublicDuration/PublicTime(...)) or "+
					"WithInternal(errcode.InternalAttr(...))",
				callee.displayName, msgArg),
		})
	})
	return out
}

// resolveGatedCallee matches call against messageGatedCallees and returns
// the matched gatedCallee. info-based resolution (production scan) checks
// the imported package path; AST-only fallback (fixture scan) checks the
// local selector name (e.g. selector.X.Name == "errcode") so fixtures can
// shadow the helper packages locally.
func resolveGatedCallee(call *ast.CallExpr, info *types.Info) (gatedCallee, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return gatedCallee{}, false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return gatedCallee{}, false
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return gatedCallee{}, false
		}
		pkgPath := fn.Pkg().Path()
		name := fn.Name()
		for _, c := range messageGatedCallees {
			if c.pkgPath == pkgPath && c.name == name {
				return c, true
			}
		}
		return gatedCallee{}, false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return gatedCallee{}, false
	}
	if _, registered := fixtureASTPackageNames[xIdent.Name]; !registered {
		return gatedCallee{}, false
	}
	for _, c := range messageGatedCallees {
		// AST-only mode keys on selector.X.Name == package short-name
		// (last segment of the import path). All four gated callees use
		// their natural short name in fixtures.
		shortName := lastPathSegment(c.pkgPath)
		if shortName == xIdent.Name && sel.Sel.Name == c.name {
			return c, true
		}
	}
	return gatedCallee{}, false
}

// lastPathSegment returns the substring after the final '/' in a Go import
// path — the package's natural short name when imported without an alias.
func lastPathSegment(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// isLiteralStringExpr reports whether expr is a string-literal expression
// or a BinaryExpr whose operands are both string literals (recursively).
// Used by the fixture-mode BinaryExpr branch where TypesInfo is unavailable;
// rejects any Ident / SelectorExpr operand because the fixture cannot
// distinguish package-level const from runtime var.
func isLiteralStringExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.BinaryExpr:
		return isLiteralStringExpr(e.X) && isLiteralStringExpr(e.Y)
	default:
		return false
	}
}

// isAcceptableMessageExpr reports whether expr is a const literal or a
// package-level string constant, or a Go constant expression (e.g. "a"+"b"
// or `(...)`) — the only forms allowed for the message argument under
// MESSAGE-CONST-LITERAL-01. info may be nil (fixture mode); in that case
// Ident / SelectorExpr fallbacks are accepted as const-like because the
// fixture cannot be type-checked, and the violations we care about are
// fmt.Sprintf-style CallExpr shapes which the BasicLit / BinaryExpr
// branches do not match.
//
// The TypesInfo.Types path handles BinaryExpr (string + string), unary,
// and parenthesized forms uniformly: any expr Go's constant-folding
// resolves to a known value passes (tv.Value != nil), runtime expressions
// fall through to the AST-shape branches.
func isAcceptableMessageExpr(expr ast.Expr, info *types.Info) bool {
	if info != nil {
		if tv, ok := info.Types[expr]; ok && tv.Value != nil {
			return true
		}
	}
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.STRING
	case *ast.Ident:
		if info == nil {
			return true
		}
		obj := info.Uses[e]
		_, isConst := obj.(*types.Const)
		return isConst
	case *ast.SelectorExpr:
		if e.Sel == nil {
			return false
		}
		if info == nil {
			return true
		}
		obj := info.Uses[e.Sel]
		_, isConst := obj.(*types.Const)
		return isConst
	case *ast.BinaryExpr:
		// Fixture-mode fallback (info == nil): accept BinaryExpr only when
		// both operands are string literals — the strictest interpretation
		// of "compile-time const concatenation" without type info.
		// Production scans always have info set and resolve via
		// TypesInfo.Types above; this branch only handles the pure-AST
		// fixture form `"a" + "b"` and rejects any Ident operand
		// (which could be a runtime var).
		if info != nil {
			return false
		}
		return isLiteralStringExpr(e.X) && isLiteralStringExpr(e.Y)
	default:
		return false
	}
}

// INVARIANT: ERROR-FIRST-API-01
//
// TestErrorFirstAPI01 walks the enforced file list and reports panic() calls
// inside error-less function declarations: in the explicitly enrolled files
// (PR-MODE-6 scope), exported and unexported function declarations whose
// return signature does NOT include an error MUST NOT contain a `panic(...)`
// call in the function body.
//
// Companion invariant ERROR-FIRST-TYPED-NIL-01 (asserted by
// TestErrorFirstTypedNil01 below) requires error-returning New* constructors
// in the enrolled file scope to nil-guard each nil-able dependency parameter
// at construction time. Interface params must be guarded with
// validation.IsNilInterface(p) (typed-nil defeat); pointer / map / chan /
// func params may use p == nil.
func TestErrorFirstAPI01(t *testing.T) {
	root := findModuleRoot(t)

	// Build the enforced set as a map for O(1) lookup by rel path.
	enforcedSet := make(map[string]struct{}, len(errorFirstEnforcedFiles))
	for _, rel := range errorFirstEnforcedFiles {
		enforcedSet[rel] = struct{}{}
	}

	diags := Run(t, ModuleScope(root, MatchRels(func(rel string) bool {
		_, ok := enforcedSet[rel]
		return ok
	})), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			out = append(out, scanFileForErrorFirstViolations(p.Fset, file, rel)...)
		}
		return out
	})

	Report(t, ruleErrorFirstAPI01, diags)
}

// scanFileForErrorFirstViolations parses a single Go source file and returns
// any panic() call inside an error-less function (excluding Must*-prefixed
// functions and init).
//
// Note: PANIC-REGISTERED-01 (panic_invariants_test.go) no longer exempts
// Must*-prefixed functions — every production panic must wrap its payload
// with panicregister.Approved(literal, value). The Must* exemption below
// is specific to ERROR-FIRST-API-01: it permits Must* functions to call
// panic at all (vs. the rule's "no panic in error-less functions" default),
// without dictating the panic shape. The two rules compose: a panic in a
// Must* function must still be panicregister.Approved-wrapped.
func scanFileForErrorFirstViolations(fset *token.FileSet, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Body == nil {
			return
		}
		if isInitFunc(fd) {
			return
		}
		if strings.HasPrefix(fd.Name.Name, "Must") {
			return
		}
		if signatureReturnsError(fd.Type.Results) {
			return
		}
		whitelistKey := rel + "::" + fd.Name.Name
		if _, whitelisted := errorFirstPanicWhitelist[whitelistKey]; whitelisted {
			return
		}
		findPanicCalls(fd.Body, func(callPos token.Pos) {
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(callPos).Line,
				Message: fmt.Sprintf(
					"function %s does not return error but contains panic()",
					fd.Name.Name),
			})
		})
	})
	return out
}

// INVARIANT: ERROR-FIRST-TYPED-NIL-01
//
// TestErrorFirstTypedNil01 verifies error-returning New* constructors in the
// enrolled file scope nil-guard each nil-able dependency parameter at
// construction time (see ERROR-FIRST-API-01 for the companion panic-free
// rule).
func TestErrorFirstTypedNil01(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)

	enforced := errorFirstEnforcedFileMap(root)

	diags := RunTyped(t, TypedOpts{Tests: false}, errorFirstPackagePatterns(),
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				abs := filepath.Clean(p.Abs(file))
				rel, ok := enforced[abs]
				if !ok {
					continue
				}
				out = append(out, scanTypedNilGuardsInFile(p.Fset, p.TypesInfo, file, rel)...)
			}
			return out
		})

	Report(t, ruleErrorFirstTypedNil01, diags)
}

// TestErrorFirstTypedNilScannerFixtures verifies the typed-nil guard detector
// via real fixture modules (Hard upgrade from inline-source table).
//
// Each subdirectory under testdata/errorfirsttypednilfixture/ is a standalone
// Go module. Each fixture dir owns a diag.golden capturing the rule's real
// output (Rel:Line: Message). *_passes cases have an empty golden; *_violates
// cases have one or more diagnostic lines. Line numbers live in the golden,
// never in this table. See ADR
// docs/architecture/202605181200-adr-archtest-fixture-diagnostic-golden.md.
func TestErrorFirstTypedNilScannerFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/errorfirsttypednilfixture"

	// RED (*_violates) and GREEN (*_passes) cases share the same loop body;
	// distinction is captured entirely in the golden file.
	dirs := []string{
		"constructor_interface_without_isnil_violates",
		"constructor_interface_with_isnil_passes",
		"optional_interface_with_isnil_passes",
		"non_error_constructor_passes",
		"non_constructor_function_passes",
		"isnil_result_discarded_violates",
		"isnil_inside_non_if_call_violates",
		"if_cond_no_return_violates",
		"then_in_goroutine_violates",
		"and_compound_violates",
		"pointer_param_nil_guard_passes",
		"or_compound_isnil_passes",
		"map_param_nil_guard_passes",
		"chan_param_nil_guard_passes",
		"func_param_nil_guard_passes",
		"slice_param_passes",
		"then_in_defer_violates",
		"aliased_validation_violates",
		"unnamed_param_passes",
		"blank_param_passes",
		"constructor_validate_required_delegation_passes",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := base + "/" + dir
			diags := RunTypedDir(t, fixtureDir, TypedOpts{Tests: true}, []string{"./..."},
				func(p *Pass) []Diagnostic {
					var out []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)
						out = append(out, scanTypedNilGuardsInFile(p.Fset, p.TypesInfo, file, rel)...)
					}
					return out
				})

			goldenPath := filepath.Join(base, dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}

func errorFirstPackagePatterns() []string {
	dirs := make(map[string]struct{})
	for _, rel := range errorFirstEnforcedFiles {
		dirs[filepath.Dir(filepath.FromSlash(rel))] = struct{}{}
	}
	patterns := make([]string, 0, len(dirs))
	for dir := range dirs {
		patterns = append(patterns, "./"+filepath.ToSlash(dir))
	}
	sort.Strings(patterns)
	return patterns
}

func errorFirstEnforcedFileMap(root string) map[string]string {
	out := make(map[string]string, len(errorFirstEnforcedFiles))
	for _, rel := range errorFirstEnforcedFiles {
		out[filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))] = rel
	}
	return out
}

func isErrorFirstConstructor(fd *ast.FuncDecl) bool {
	return fd.Recv == nil &&
		strings.HasPrefix(fd.Name.Name, "New") &&
		signatureReturnsError(fd.Type.Results)
}

// paramKind classifies how a function parameter is nil-able for the purposes
// of ERROR-FIRST-TYPED-NIL-01. Slices are intentionally excluded: nil slice
// is safe to read (len/range) and treating it as a guard target would produce
// false positives for every []T parameter. Generic type parameters are also
// excluded — there is no enforced-scope code using them, and their nil-ability
// depends on the constraint, not the syntactic form.
type paramKind int

const (
	paramNone paramKind = iota
	paramInterface
	paramPointerOrNillableConcrete
)

// paramRef pairs a parameter name with its kind so the rule can pick the
// right guard form per kind (IsNilInterface for interfaces; == nil acceptable
// for pointer / map / chan / func).
type paramRef struct {
	name string
	kind paramKind
}

// nillableParamKind returns the paramKind for a Go type, or paramNone if the
// type is outside the rule's scope.
func nillableParamKind(t types.Type) paramKind {
	if t == nil {
		return paramNone
	}
	// clock.Clock is governed by its own sanctioned guard funnel —
	// clock.MustHaveClock, a programmer-error panic (ADR 202605270000 / #883
	// Option A) — NOT the validation.IsNilInterface error-first funnel this rule
	// enforces. A clk param is a mandatory positional dependency guarded by
	// MustHaveClock in the constructor body; double-requiring IsNilInterface
	// would force a redundant second guard. Exempt it so MustHaveClock-guarded
	// clk params are not flagged. ref: runtime-api.md §Option 范式分层
	// (kernel/clock.MustHaveClock 不属于 typed-nil helper 治理范围);
	// CLOCK-POSITIONAL-INJECTION-01 owns the clk positional-injection funnel.
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil &&
			obj.Pkg().Path() == kernelClockPkgPath && obj.Name() == "Clock" {
			return paramNone
		}
	}
	switch t.Underlying().(type) {
	case *types.Interface:
		return paramInterface
	case *types.Pointer, *types.Map, *types.Chan, *types.Signature:
		return paramPointerOrNillableConcrete
	}
	return paramNone
}

// nillableDependencyParams returns the named, nil-able parameters of fd.
// Unnamed (type-only) parameters like `func New(Dep) (*S, error)` are
// intentionally skipped — they cannot be referred to in a guard expression,
// so the rule has no symbol to verify; constructors that require such a
// parameter for ergonomic reasons should name it (`func New(_ Dep)` is also
// skipped on purpose because `_` is unaddressable).
func nillableDependencyParams(info *types.Info, fd *ast.FuncDecl) []paramRef {
	if info == nil || fd.Type.Params == nil {
		return nil
	}
	var out []paramRef
	for _, field := range fd.Type.Params.List {
		kind := nillableParamKind(info.TypeOf(field.Type))
		if kind == paramNone {
			continue
		}
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			out = append(out, paramRef{name: name.Name, kind: kind})
		}
	}
	return out
}

// hasNilGuard returns true if body contains an IfStmt whose Cond is a nil
// check on paramName AND whose Then-branch surfaces the nil case (return or
// assignment to paramName for defaulting). Goroutine / closure FuncLit bodies
// are stop-descend: a deferred return inside a closure does not satisfy the
// constructor's outer fail-fast contract.
func hasNilGuard(body *ast.BlockStmt, paramName string, kind paramKind) bool {
	_, found := FindFirstInSubtree[ast.IfStmt](body, func(ifStmt *ast.IfStmt) bool {
		return condMatchesNilCheck(ifStmt.Cond, paramName, kind) &&
			thenReturnsOrAssigns(ifStmt.Body, paramName)
	})
	return found
}

// condMatchesNilCheck returns true if expr nil-checks paramName, either as a
// leaf or as a leaf of a top-level || (LOR) chain. && (LAND) and unary ! are
// rejected: && lets nil flow past, and ! inverts the fail-fast direction.
//
// Leaf forms:
//   - validation.IsNilInterface(paramName)             (any kind)
//   - paramName == nil / nil == paramName              (paramPointerOrNillableConcrete only)
//
// Interface params reject == nil because typed-nil ((*Concrete)(nil) cast to
// interface) bypasses the comparison; only IsNilInterface defeats it.
func condMatchesNilCheck(expr ast.Expr, paramName string, kind paramKind) bool {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return condMatchesNilCheck(e.X, paramName, kind)
	case *ast.BinaryExpr:
		if e.Op == token.LOR {
			return condMatchesNilCheck(e.X, paramName, kind) ||
				condMatchesNilCheck(e.Y, paramName, kind)
		}
		if e.Op == token.EQL && kind == paramPointerOrNillableConcrete {
			return isNilEquality(e, paramName)
		}
		return false
	case *ast.CallExpr:
		return isValidationIsNilInterfaceCall(e, paramName)
	}
	return false
}

// isNilEquality returns true if e is `paramName == nil` or `nil == paramName`.
func isNilEquality(e *ast.BinaryExpr, paramName string) bool {
	if e.Op != token.EQL {
		return false
	}
	if isIdentNamed(e.X, paramName) && isNilIdent(e.Y) {
		return true
	}
	if isIdentNamed(e.Y, paramName) && isNilIdent(e.X) {
		return true
	}
	return false
}

func isIdentNamed(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// isValidationIsNilInterfaceCall returns true if call is exactly
// validation.IsNilInterface(paramName) — single argument, named param, fixed
// selector path on the unaliased "validation" identifier.
//
// known-gap: aliased imports (e.g. `import val "github.com/.../pkg/validation"`
// + `val.IsNilInterface(p)`) are not recognized as a guard; the alias would
// surface as a violation report. This is by design — every IsNilInterface
// call in the enrolled scope uses the unaliased package, and matching aliases
// would require types.Info-level package resolution that adds cost without
// covering a real-world need.
func isValidationIsNilInterfaceCall(call *ast.CallExpr, paramName string) bool {
	if len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "IsNilInterface" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "validation" {
		return false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	return ok && arg.Name == paramName
}

// thenReturnsOrAssigns returns true if body contains a top-level (non-FuncLit)
// ReturnStmt or an AssignStmt whose LHS includes paramName (defaulting). The
// FuncLit exclusion prevents `if cond { go func() { return }() }` from
// satisfying the constructor's outer contract.
func thenReturnsOrAssigns(body *ast.BlockStmt, paramName string) bool {
	// Collect pos ranges of all FuncLit nodes so we can skip nodes inside them.
	type posRange struct{ lo, hi token.Pos }
	var funcLitRanges []posRange
	EachInSubtree[ast.FuncLit](body, func(fl *ast.FuncLit) {
		funcLitRanges = append(funcLitRanges, posRange{fl.Pos(), fl.End()})
	})
	inFuncLit := func(pos token.Pos) bool {
		for _, r := range funcLitRanges {
			if pos >= r.lo && pos < r.hi {
				return true
			}
		}
		return false
	}

	_, foundReturn := FindFirstInSubtree[ast.ReturnStmt](body, func(s *ast.ReturnStmt) bool {
		return !inFuncLit(s.Pos())
	})
	if foundReturn {
		return true
	}
	_, foundAssign := FindFirstInSubtree[ast.AssignStmt](body, func(s *ast.AssignStmt) bool {
		if inFuncLit(s.Pos()) {
			return false
		}
		for _, lhs := range s.Lhs {
			if isIdentNamed(lhs, paramName) {
				return true
			}
		}
		return false
	})
	return foundAssign
}

// scanTypedNilGuardsInFile returns Diagnostic violations for
// ERROR-FIRST-TYPED-NIL-01 in a single file.
func scanTypedNilGuardsInFile(fset *token.FileSet, info *types.Info, file *ast.File, rel string) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Body == nil || !isErrorFirstConstructor(fd) {
			return
		}
		// A constructor that delegates to the generated validateRequired()
		// funnel (REQUIRED-DEP-NIL-GUARD-01) cedes required-dep nil checking to
		// that single source of truth: A1 locks the generated method to the
		// struct's gocell:"required" tags, A2 forces it to be called post-options
		// and its error consumed, A3 bans a hand-written IsNilInterface on those
		// fields. Re-requiring an inline per-param guard here would directly
		// contradict A3. The two rules compose: inline guard OR funnel.
		if bodyCallsValidateRequired(fd.Body) {
			return
		}
		for _, param := range nillableDependencyParams(info, fd) {
			if hasNilGuard(fd.Body, param.name, param.kind) {
				continue
			}
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: fset.Position(fd.Pos()).Line,
				Message: fmt.Sprintf(
					"constructor %s: nil-able dependency %s is not guarded at construction time",
					fd.Name.Name, param.name),
			})
		}
	})
	return out
}

// bodyCallsValidateRequired reports whether body contains a call to a method
// named validateRequired (the REQUIRED-DEP-NIL-GUARD-01 generated funnel).
// Reuses isValidateRequiredCallExpr from required_dep_nil_guard_test.go (same
// package) so both rules agree on the funnel callsite shape.
func bodyCallsValidateRequired(body *ast.BlockStmt) bool {
	found := false
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		if isValidateRequiredCallExpr(call) {
			found = true
		}
	})
	return found
}

// isInitFunc returns true if fd is `func init()` (no receiver, no params, no
// return values, name "init").
func isInitFunc(fd *ast.FuncDecl) bool {
	if fd.Name.Name != "init" {
		return false
	}
	if fd.Recv != nil {
		return false
	}
	return true
}

// signatureReturnsError returns true if the FieldList contains at least one
// field whose type is the identifier `error` (built-in) — handles single
// return, named returns, and tuple returns.
func signatureReturnsError(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, field := range results.List {
		if isErrorIdent(field.Type) {
			return true
		}
	}
	return false
}

// isErrorIdent returns true when expr is the unqualified identifier `error`.
// Qualified types (e.g., pkg.MyError) and pointer/slice/array wrappers are
// intentionally rejected — only the built-in `error` interface satisfies the
// rule.
func isErrorIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "error"
}

// findPanicCalls walks body and invokes onPanic for every call to the built-in
// `panic` function. Calls inside nested function literals are also reported —
// a closure that panics still violates the rule unless the enclosing function
// returns error (which would let the closure propagate the failure instead).
//
// Built-in panic detection: the rule matches `panic(...)` where the Fun is the
// unqualified identifier `panic`. Re-defined locals (e.g. `var panic = func()`)
// would shadow the built-in; we treat them the same as the built-in to keep
// the rule conservative — there is no production reason to shadow `panic`.
func findPanicCalls(body *ast.BlockStmt, onPanic func(token.Pos)) {
	EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return
		}
		if ident.Name == "panic" {
			onPanic(call.Pos())
		}
	})
}

// INVARIANT: EXPORTED-ERROR-NEW-01
//
// TestExportedErrorNew enforces EXPORTED-ERROR-NEW-01 by walking every
// production-code file (per fileroles.IsProductionCode) outside the
// pkg/errcode/ allow-list and flagging package-scope exported sentinel
// vars whose initializer is `errors.New(...)`.
//
// EXPORTED-ERROR-NEW-01 — invariant-driven gate.
//
// Invariant: In all production-shippable .go files outside pkg/errcode/,
// no top-level (package-scope) `var` declaration may bind an exported
// identifier matching the sentinel naming convention `^Err[A-Z]\w*$` to
// an `errors.New(...)` call expression. Use pkg/errcode.New(code, message)
// so the sentinel participates in the wire-protocol error code taxonomy
// and HTTP status mapping (CLAUDE.md: 禁止 errors.New 对外暴露).
//
// ref: docs/plans/202605011500-029-master-roadmap.md G2
func TestExportedErrorNew(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	visited := map[string]bool{}

	diags := RunTyped(t,
		TypedOpts{Tests: false, Tags: []string{"e2e", "integration", "pg"}},
		patterns,
		func(p *Pass) []Diagnostic {
			var out []Diagnostic
			for _, file := range p.Files {
				abs := p.Abs(file)
				if visited[abs] {
					continue
				}
				visited[abs] = true

				rel := p.Rel(file)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if strings.HasPrefix(rel, errcodeAllowlistPath) {
					continue
				}
				out = append(out, scanExportedErrorNewASTDiags(p.Fset, file, rel, p.TypesInfo)...)
			}
			return out
		})

	Report(t, ruleExportedErrorNew01, diags)
}

// scanExportedErrorNewASTDiags is the Diagnostic-returning form used by the
// TestExportedErrorNew Pass-funnel rule.
func scanExportedErrorNewASTDiags(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	var out []Diagnostic
	EachInSubtree[ast.GenDecl](file, func(gen *ast.GenDecl) {
		if gen.Tok != token.VAR {
			return
		}
		EachInChildren[ast.ValueSpec](gen, func(vs *ast.ValueSpec) {
			// A ValueSpec with N names and 1 value is a multi-assign from a
			// single function call; errors.New only returns one value, so
			// such a form would not type-check. We still iterate Values
			// indexed by position to be safe.
			for i, name := range vs.Names {
				if !isExportedErrSentinelName(name.Name) {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				if !isErrorsNewCall(vs.Values[i], info) {
					continue
				}
				pos := fset.Position(name.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"%s = errors.New(...) — migrate to errcode.New(code, message)",
						name.Name),
				})
			}
		})
	})
	return out
}

// isExportedErrSentinelName reports whether name follows the exported sentinel
// convention `Err` + ASCII uppercase + zero-or-more word chars. Names like
// Errno / Errors (4th byte lowercase) and bare `Err` are not sentinel-pattern
// matches and are accepted. Go exported identifiers are conventionally ASCII,
// so byte indexing (`name[3]`) is sufficient — the gate intentionally does not
// handle Unicode-uppercase 4th runes (e.g. a non-ASCII capital after "Err"
// would be vanishingly rare in practice).
func isExportedErrSentinelName(name string) bool {
	if !strings.HasPrefix(name, "Err") {
		return false
	}
	if len(name) <= 3 {
		return false
	}
	c := name[3]
	return c >= 'A' && c <= 'Z'
}

// isErrorsNewCall reports whether expr is a call to stdlib `errors.New`,
// resolving aliased imports via TypesInfo.Uses.
func isErrorsNewCall(expr ast.Expr, info *types.Info) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if info == nil {
		return false
	}
	obj := info.Uses[sel.Sel]
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return false
	}
	if fn.Name() != "New" {
		return false
	}
	pkg := fn.Pkg()
	if pkg == nil {
		return false
	}
	return pkg.Path() == "errors"
}

// ─── details_sealed_field_frozen ────────────────────────────────────────────
//
// INVARIANT: DETAILS-SEALED-FIELD-FROZEN-01
//
// errcode.PublicDetail / errcode.InternalDetail are sealed-construction
// carriers (ADR docs/architecture/202605051730-adr-errcode-message-pii-safety.md
// §Amendment 2026-05-27). The construction Hard claim requires two
// invariants that this archtest locks via reflect:
//
//  1. Field layout: both structs MUST have exactly {key, value}, both
//     unexported. An exported field re-opens the literal-construction
//     bypass (`errcode.PublicDetail{Key:..., Value:...}` becomes valid
//     from outside the package), collapsing the Hard rating.
//  2. publicValue typed-marker: errcode.PublicDetail.value MUST be the
//     sealed interface (named "publicValue", unexported method publicValue()).
//     A widening to `any` (or a renamed-but-method-exporting variant)
//     would let callers route wire-unsafe types (chan, func, NaN/Inf
//     floats, maps, structs, pointers) into Error.Details, silently
//     downgrading 4xx → 500 at json.Marshal time.
//
// InternalDetail.value is intentionally `any` (server-only channel; ADR
// amendment §"Three-layer table revision"); the test asserts that
// deliberate asymmetry so it cannot drift silently.
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md §Hard 范本目录
// "sealed construction"):
//   - Downstream Hard: typed Public* constructors are the only callsites
//     that can produce a non-zero publicValue (the marker method is
//     unexported, so no other package can implement it). Wire-unsafe
//     value types are inexpressible at compile time.
//   - Upstream Hard: PublicDetail.key / PublicDetail.value are unexported,
//     so outside-package struct-literal construction is a Go compile
//     error. errcode.Error.Details ([]PublicDetail) remains exported and
//     can be mutated by outside code, but only via PublicDetail values
//     produced from the typed constructors; the wire-side 5xx Details-
//     strip invariant (Error.MarshalJSON → project() → []PublicDetail{})
//     is the defense for runtime data leakage regardless of append source.
//
// Blind spots (reverse self-check below):
//   - The lock keys on field NAMES "key"/"value"; a rename surfaces as
//     a test error (visible), not a silent pass.
//   - The publicValue type identity is checked by name and by the
//     presence of an unexported method "publicValue" — an aliased type
//     declared inside pkg/errcode with the same method set would pass,
//     but it would still be sealed (the marker is package-local).
//   - Outside-package aliasing of PublicDetail (`type Foo = errcode.PublicDetail`
//     in another package) does not re-open construction: aliases preserve
//     field visibility, so unexported fields stay unconstructable.
//
// Reference: SUBSCRIBERS-DERIVED-FIELD-FROZEN-01 (same reflect lock pattern),
// OUTBOX-HANDLERESULT-FIELDS-FROZEN-01 (sibling envelope freeze).
func TestDetailsSealedFieldFrozen01(t *testing.T) {
	t.Parallel()

	t.Run("PublicDetail", func(t *testing.T) {
		dt := reflect.TypeOf(errcode.PublicDetail{})
		assertSealedKeyValueShape(t, "PublicDetail", dt)

		// PublicDetail.value MUST be the publicValue sealed interface
		// — name check pins the marker identity; an in-package rename
		// surfaces visibly here.
		valueField, ok := dt.FieldByName("value")
		if !ok {
			t.Fatal("DETAILS-SEALED-FIELD-FROZEN-01: PublicDetail has no value field (caught by shape assertion above)")
		}
		if valueField.Type.Kind() != reflect.Interface {
			t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: PublicDetail.value Kind = %s, want Interface "+
				"(widening to a concrete type, or to a non-marker interface, breaks the typed-value funnel)",
				valueField.Type.Kind())
		}
		if got := valueField.Type.Name(); got != "publicValue" {
			t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: PublicDetail.value type name = %q, want %q "+
				"(if you renamed the marker, update this archtest and the ADR amendment table)",
				got, "publicValue")
		}

		// Confirm the typed constructors land on the same value field type
		// (defense-in-depth: if PublicString were rewired to return a
		// PublicDetail with a different value kind, this test fires).
		probe := reflect.ValueOf(errcode.PublicString("k", "v"))
		probeValue := probe.FieldByName("value")
		if probeValue.Kind() != reflect.Interface {
			t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: PublicString(...) produced value Kind %s, want Interface",
				probeValue.Kind())
		}

		root := findModuleRoot(t)
		detailsPath := filepath.Join(root, "pkg", "errcode", "details.go")
		file := mustParseGoFile(t, detailsPath)
		assertExactStringSet(t, "DETAILS-SEALED-FIELD-FROZEN-01 publicValue implementers",
			collectPublicValueImplementers(file),
			[]string{"publicBool", "publicDuration", "publicInt", "publicString", "publicTime"})
		assertExactStringSet(t, "DETAILS-SEALED-FIELD-FROZEN-01 PublicDetail constructors",
			collectPublicDetailConstructors(file),
			[]string{"PublicBool", "PublicDuration", "PublicInt", "PublicString", "PublicTime"})
	})

	t.Run("InternalDetail", func(t *testing.T) {
		dt := reflect.TypeOf(errcode.InternalDetail{})
		assertSealedKeyValueShape(t, "InternalDetail", dt)

		// InternalDetail.value is intentionally untyped any (server-only
		// channel; runtime data including fmt.Sprintf output is the
		// documented use case). Lock the asymmetry so a future refactor
		// that "harmonizes" both carriers must update the ADR first.
		valueField, ok := dt.FieldByName("value")
		if !ok {
			t.Fatal("DETAILS-SEALED-FIELD-FROZEN-01: InternalDetail has no value field (caught by shape assertion above)")
		}
		if valueField.Type.Kind() != reflect.Interface || valueField.Type.Name() != "" {
			t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: InternalDetail.value type = %s, want untyped any "+
				"(InternalDetail is server-only; tightening to a marker interface needs an ADR amendment)",
				valueField.Type.String())
		}
	})
}

func mustParseGoFile(t *testing.T, path string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	require.NoError(t, err, "DETAILS-SEALED-FIELD-FROZEN-01: parse %s", path)
	return file
}

func collectPublicValueImplementers(file *ast.File) []string {
	seen := map[string]struct{}{}
	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv == nil || fn.Name.Name != "publicValue" || len(fn.Recv.List) == 0 {
			return
		}
		if name := detailReceiverTypeName(fn.Recv.List[0].Type); name != "" {
			seen[name] = struct{}{}
		}
	})
	return sortedSetKeys(seen)
}

func collectPublicDetailConstructors(file *ast.File) []string {
	seen := map[string]struct{}{}
	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil {
			return
		}
		for _, result := range fn.Type.Results.List {
			if ident, ok := result.Type.(*ast.Ident); ok && ident.Name == "PublicDetail" {
				seen[fn.Name.Name] = struct{}{}
			}
		}
	})
	return sortedSetKeys(seen)
}

func detailReceiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return detailReceiverTypeName(t.X)
	default:
		return ""
	}
}

func sortedSetKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func assertExactStringSet(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v. Adding/removing a public detail scalar kind must update "+
			"details.go, error-response-v1.schema.json, the ADR, and this archtest together.",
			name, got, want)
	}
}

// checkSealedKeyValueShape returns one violation message per shape axis
// (field count, key presence/visibility/type, value presence/visibility).
// Pure function so the reverse self-check below can call it directly on
// synthetic structs without involving *testing.T. Empty result = clean.
func checkSealedKeyValueShape(name string, dt reflect.Type) []string {
	var violations []string
	if dt.NumField() != 2 {
		violations = append(violations, fmt.Sprintf(
			"%s NumField = %d, want 2 (adding a field re-opens the sealed-construction invariant; update the ADR amendment first)",
			name, dt.NumField()))
		return violations
	}
	if keyField, ok := dt.FieldByName("key"); !ok {
		violations = append(violations, fmt.Sprintf(
			"%s has no 'key' field (renamed? exported? both break the sealed-construction invariant)", name))
	} else {
		if keyField.PkgPath == "" {
			violations = append(violations, fmt.Sprintf(
				"%s.key is exported (PkgPath empty); outside-package literal construction becomes possible — re-seal by lowercasing",
				name))
		}
		if keyField.Type.Kind() != reflect.String {
			violations = append(violations, fmt.Sprintf(
				"%s.key Kind = %s, want String", name, keyField.Type.Kind()))
		}
	}
	if valueField, ok := dt.FieldByName("value"); !ok {
		violations = append(violations, fmt.Sprintf(
			"%s has no 'value' field (renamed? exported? both break the sealed-construction invariant)", name))
	} else if valueField.PkgPath == "" {
		violations = append(violations, fmt.Sprintf(
			"%s.value is exported (PkgPath empty); outside-package literal construction becomes possible — re-seal by lowercasing",
			name))
	}
	return violations
}

// assertSealedKeyValueShape adapts checkSealedKeyValueShape to *testing.T
// so the production assertion (TestDetailsSealedFieldFrozen01) can share
// the same logic as the reverse self-check below.
func assertSealedKeyValueShape(t *testing.T, name string, dt reflect.Type) {
	t.Helper()
	for _, v := range checkSealedKeyValueShape(name, dt) {
		t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: %s", v)
	}
}

// TestDetailsSealedFieldFrozen01_ScannerFires proves the field-shape
// assertion has teeth (reverse self-check): a synthetic struct that
// violates each axis must produce a non-empty result from
// checkSealedKeyValueShape. Without this, a refactor that lowercases
// the helper's guard conditions could silently disable the lock.
//
// Synthetic types are built via reflect.StructOf so the linter does not
// see "unused" field declarations on probe-only structs.
func TestDetailsSealedFieldFrozen01_ScannerFires(t *testing.T) {
	t.Parallel()

	stringType := reflect.TypeOf("")
	anyType := reflect.TypeOf((*any)(nil)).Elem()

	mkField := func(name string, typ reflect.Type, exported bool) reflect.StructField {
		f := reflect.StructField{Name: name, Type: typ}
		if !exported {
			f.PkgPath = "github.com/ghbvf/gocell/tools/archtest"
		}
		return f
	}

	cases := []struct {
		name   string
		fields []reflect.StructField
		want   bool // true = scanner must report violation
	}{
		{
			name: "extraField", // wrong field count
			fields: []reflect.StructField{
				mkField("key", stringType, false),
				mkField("value", anyType, false),
				mkField("Extra", stringType, true),
			},
			want: true,
		},
		{
			name: "exportedKey", // outside-package literal construction becomes possible
			fields: []reflect.StructField{
				mkField("Key", stringType, true),
				mkField("value", anyType, false),
			},
			want: true,
		},
		{
			name: "renamedKey",
			fields: []reflect.StructField{
				mkField("ident", stringType, false),
				mkField("value", anyType, false),
			},
			want: true,
		},
		{
			name: "goodShape", // canonical {key, value} unexported — must pass
			fields: []reflect.StructField{
				mkField("key", stringType, false),
				mkField("value", anyType, false),
			},
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := checkSealedKeyValueShape(tc.name, reflect.StructOf(tc.fields))
			if tc.want && len(got) == 0 {
				t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: scanner did not fire on %s", tc.name)
			}
			if !tc.want && len(got) != 0 {
				t.Errorf("DETAILS-SEALED-FIELD-FROZEN-01: scanner false-positive on %s: %v", tc.name, got)
			}
		})
	}

	// Keep the time import live for parity with details.go imports.
	_ = time.Second
}
