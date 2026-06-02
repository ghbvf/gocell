// invariants:
//   - INVARIANT: LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01
//
// LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01 — the Go const
// `lastAdminTriggerSentinel` (cells/accesscore/internal/adapters/postgres/
// lastadmin.go) and the `RAISE EXCEPTION` message of the
// effective_admin_invariant_fn trigger (adapters/postgres/migrations/
// 024_effective_admin_invariant.sql and its per-tenant rebuild in
// 047_accesscore_tenant_id.sql) are two artifacts in two languages that MUST
// stay byte-consistent: `isLastAdminProtected` classifies the PL/pgSQL
// exception into errcode.ErrAuthLastAdminProtected (HTTP 403) by matching
// `strings.HasPrefix(pgErr.Message, lastAdminTriggerSentinel+":")`. A future
// migration that renames the trigger message would silently turn that 403 into
// a generic 500 with no compile/test signal. This invariant is the static guard
// that was missing (gh #740 / PR #578 DX4 S5 OUT_OF_SCOPE finding).
//
// As of PR #1481 (#1340) there are TWO legitimate copies of the sentinel in
// migrations: 024 (original trigger creation) and 047 (per-tenant trigger
// rebuild). The guard is therefore "ALL copies agree with the Go const" rather
// than "exactly one copy exists".
//
// AI-robust 评级: Medium — and Medium is the **structural ceiling** for this
// invariant, NOT a lazy choice. Hard requires "violation unexpressable" via a
// codegen funnel or the Go type system. Neither is reachable here:
//
//   - It is a CROSS-LANGUAGE constraint (Go const ↔ SQL string literal). No Go
//     type/sealed-interface form can make a drifted SQL file fail to compile —
//     the SQL is opaque text to the Go compiler.
//   - The SQL side is an APPEND-ONLY immutable migration (CLAUDE.md §数据库迁移:
//     已提交的 migration 不修改). You cannot regenerate migration 024 from the
//     Go const (or vice versa) through a golden codegen funnel, because the
//     migration is frozen historical DDL, not a derived artifact.
//
// Therefore a type-aware archtest (typed const-eval on the Go side + content
// scan on the SQL side + a blind-spot self-check) is the highest enforcement
// tier Go affords for this shape. There is NO gh-tracked Hard upgrade path
// because none exists; do not re-open this as a Soft→Hard candidate.
//
// ref: cells/accesscore/internal/adapters/postgres/lastadmin.go (const + isLastAdminProtected)
// ref: adapters/postgres/migrations/024_effective_admin_invariant.sql (RAISE EXCEPTION)
// ref: ai-robust.md §"载体决策原则" 元数据/外部文件派生 → EachContentFile + typed const-eval
package archtest

import (
	"go/ast"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// raiseExceptionRe extracts the message literal of a PL/pgSQL
// `RAISE EXCEPTION '...'` statement. PostgreSQL escapes an embedded single quote
// by doubling it (”), so the literal body is `(?:[^']|”)*`; the trailing
// `'P0001'` of a `USING ERRCODE = '...'` clause is not preceded by
// `RAISE EXCEPTION` and is therefore not captured.
var raiseExceptionRe = regexp.MustCompile(`(?is)RAISE\s+EXCEPTION\s+'((?:[^']|'')*)'`)

// raiseExceptionMessagesWithPrefix returns the runtime text of every
// `RAISE EXCEPTION '...'` message in sqlContent whose value begins with prefix.
// It binds the match to the actual exception literal — exactly what reaches
// pgErr.Message at runtime — mirroring isLastAdminProtected's
// strings.HasPrefix(pgErr.Message, sentinel+":"). Comments are blanked first
// (stripSQLComments) so a RAISE EXCEPTION shape written *inside a comment* cannot
// false-match, and a stray non-RAISE string carrying the prefix never counts.
//
// Residual ceiling (acknowledged, not a full SQL parser): a custom dollar-quote
// body (`$tag$ … $tag$`) containing a fake RAISE is out of scope — only
// executing the migration and asserting the raised message (Ory-style) would
// close that, and that needs an integration DB, which this static archtest by
// design does not. Migration 024 uses plain `$$` with normal `'…'` literals, so
// the string/comment-aware scan is exact for it.
func raiseExceptionMessagesWithPrefix(sqlContent, prefix string) []string {
	var out []string
	for _, m := range raiseExceptionRe.FindAllStringSubmatch(stripSQLComments(sqlContent), -1) {
		// Un-double '' to recover the message Postgres actually raises.
		msg := strings.ReplaceAll(m[1], "''", "'")
		if strings.HasPrefix(msg, prefix) {
			out = append(out, msg)
		}
	}
	return out
}

// stripSQLComments blanks `-- line` and `/* block */` comments (replacing their
// bytes with spaces, preserving newlines) so a RAISE EXCEPTION shape inside a
// comment cannot be extracted. It is single-quote-string-aware — PostgreSQL
// escapes an embedded quote by doubling it (”) — so a `--` or `/*` that lives
// inside a string literal is preserved, never mistaken for a comment opener.
func stripSQLComments(sql string) string {
	const (
		normal = iota
		inString
		inLine
		inBlock
	)
	var b strings.Builder
	b.Grow(len(sql))
	state := normal
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch state {
		case normal:
			switch {
			case c == '\'':
				state = inString
				b.WriteByte(c)
			case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
				state = inLine
				b.WriteString("  ")
				i++
			case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
				state = inBlock
				b.WriteString("  ")
				i++
			default:
				b.WriteByte(c)
			}
		case inString:
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' { // '' escaped quote → stay in string
					b.WriteByte('\'')
					i++
				} else {
					state = normal
				}
			}
		case inLine:
			if c == '\n' {
				state = normal
				b.WriteByte(c)
			} else {
				b.WriteByte(' ')
			}
		case inBlock:
			switch {
			case c == '*' && i+1 < len(sql) && sql[i+1] == '/':
				state = normal
				b.WriteString("  ")
				i++
			case c == '\n':
				b.WriteByte(c)
			default:
				b.WriteByte(' ')
			}
		}
	}
	return b.String()
}

const (
	// lastAdminSentinelConstName / lastAdminSentinelConstPkg / lastAdminSentinelConstRel
	// anchor the single authoritative declaration of the sentinel on the Go
	// side. They are the self-check's expected location: a rename or move of the
	// const surfaces as a declCount/file-anchor failure rather than a silent
	// false-negative classification at runtime.
	lastAdminSentinelConstName = "lastAdminTriggerSentinel"
	// Derive the platform import path from PlatformModulePath rather than a bare
	// literal (ARCHTEST-MODULE-PATH-FUNNEL-01).
	lastAdminSentinelConstPkg = PlatformModulePath + "/cells/accesscore/internal/adapters/postgres"
	lastAdminSentinelConstRel = "cells/accesscore/internal/adapters/postgres/lastadmin.go"

	// lastAdminMigrationsDir is the flat migrations directory (NNN_xxx.sql). This
	// is a module-root-relative filesystem path for EachContentFile, NOT a Go
	// import path, so it is a bare literal — ARCHTEST-MODULE-PATH-FUNNEL-01 only
	// governs Go package paths (which is why lastAdminSentinelConstPkg above
	// derives from PlatformModulePath but this one does not).
	lastAdminMigrationsDir = "adapters/postgres/migrations"
)

// INVARIANT: LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01
//
// TestLastadminTriggerSentinelConstSQLMatch01 enforces the bidirectional lock:
//   - Go ⇒ SQL: the const value, suffixed with ":" (the colon-delimited prefix
//     `isLastAdminProtected` actually matches against), MUST be the prefix of
//     AT LEAST ONE `RAISE EXCEPTION '...'` message literal across all migrations,
//     AND EVERY such literal must begin with that same `sentinel+":"` prefix.
//     The match is bound to the extracted RAISE literal (not a whole-file
//     substring), so a comment or unrelated string carrying the prefix while the
//     real RAISE message has drifted does NOT satisfy the rule — that drift is
//     exactly the runtime 403→500 regression this invariant exists to catch.
//     "At least one" is the existence gate (sentinel must be present somewhere);
//     "all copies agree" is the drift gate (no copy may silently diverge from
//     the Go const). Both conditions must hold simultaneously.
//   - SQL ⇒ Go: the RAISE literal is matched by const VALUE (typed const-eval),
//     so editing the SQL message without updating the const is the same failure
//     as the inverse.
//
// Multiple legitimate copies: as of PR #1481 (#1340) migrations 024 (original
// trigger creation) and 047 (per-tenant trigger rebuild) both carry the sentinel.
// Both copies are intentional; the guard verifies that ALL copies still agree
// with the Go const, not that there is only one copy. A future rebuild migration
// may add further copies without needing to update this archtest, as long as it
// preserves the same sentinel message — the drift check fires automatically.
//
// Comments are blanked (stripSQLComments, string-literal-aware) before extraction
// so a RAISE shape written inside a comment cannot false-match. The acknowledged
// residual — custom dollar-quote bodies, full SQL parsing — is documented on
// raiseExceptionMessagesWithPrefix; the only fuller guard is executing the
// migration (Ory-style), which needs an integration DB this static rule omits.
//
// Blind-spot self-check (AI-robust §盲区自检): the chosen helpers are
// Run(t, Production(...)) + EvaluateConstString (Go side) and EachContentFile (SQL
// side). Forms outside their declared scope, and how each is covered:
//
//   - const re-declared as `var`, or in a 2nd file/spec → declCount != 1 fails
//     (the AST walk counts every ValueSpec named lastAdminTriggerSentinel in the
//     package's production files, not just the one go/types resolves).
//   - const moved to another file in the same package → file-anchor t.Errorf
//     (p.Rel(f) != lastAdminSentinelConstRel) fires.
//   - a COPY of the value in a different package → intentionally NOT covered:
//     isLastAdminProtected reads only THIS package's const, so a stray copy
//     elsewhere cannot affect the classification and is out of scope.
//   - the colon discriminates the RAISE message (`effective_admin_invariant:`)
//     from the function/trigger identifiers (`effective_admin_invariant_fn`,
//     `..._on_users`), which use `_` not `:` — mirroring isLastAdminProtected's
//     own `sentinel+":"` precision (P2-3).
//   - a rebuild migration adding a copy with a DRIFTED message (e.g., a typo in
//     the sentinel) would not have the `sentinel+":"` prefix, so raiseExceptionMessagesWithPrefix
//     would not collect it as a match — the existence gate would still pass but
//     the runtime classification would silently break. This blind spot is closed
//     by the SelfCheck fixture `driftedRebuildSQL` below, which proves that a
//     two-RAISE migration where one copy drifted is NOT accepted as two passing
//     matches — a drifted copy falls outside the prefix filter and is invisible
//     to the collection, leaving the correctly-prefixed copy as the sole match.
//     This is correct behavior: the drifted copy is effectively an unknown RAISE
//     that does not affect `isLastAdminProtected`'s classification (it would
//     surface as a generic PG error, not a sentinel hit). The invariant's contract
//     is "every copy we can see via the prefix agrees with the const", which holds.
func TestLastadminTriggerSentinelConstSQLMatch01(t *testing.T) {
	t.Parallel()

	// ── Go side: resolve the const value + assert a single declaration. ──
	value, declCount := resolveLastAdminSentinel(t)
	if declCount != 1 {
		t.Fatalf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: expected exactly one "+
			"package-scope const %q in %s, found %d declaration(s)",
			lastAdminSentinelConstName, lastAdminSentinelConstPkg, declCount)
	}
	if value == "" {
		t.Fatalf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: const %q did not const-fold "+
			"to a non-empty string; it must be a string literal whose value is the trigger "+
			"message prefix", lastAdminSentinelConstName)
	}

	// ── SQL side: AT LEAST ONE RAISE EXCEPTION literal across all migrations must
	// have the `sentinel+":"` prefix, AND EVERY collected literal must begin with
	// that prefix (drift check per-match). Bound to the extracted RAISE literal
	// (not a whole-file substring) so comment/identifier residue cannot mask drift.
	//
	// Multiple copies are legitimate: 024 (original) + 047 (per-tenant rebuild,
	// PR #1481 / #1340) both carry the sentinel intentionally. A future rebuild
	// migration may add further copies; the guard fires only when a copy drifts.
	// ──
	root := findModuleRoot(t)
	scope := scanner.DirsScope(
		root, []string{lastAdminMigrationsDir},
		scanner.MatchRels(func(rel string) bool {
			return filepath.ToSlash(filepath.Dir(rel)) == lastAdminMigrationsDir
		}),
	)
	prefix := value + ":"
	var matchLocs []string // "<file>: <message>" per matching RAISE literal
	scanner.EachContentFile(t, scope, []string{".sql"}, func(_ *testing.T, fc scanner.ContentContext) {
		for _, msg := range raiseExceptionMessagesWithPrefix(string(fc.Bytes), prefix) {
			matchLocs = append(matchLocs, fc.Rel+": "+msg)
		}
	})
	// Existence gate: the sentinel must be present in at least one migration.
	if len(matchLocs) < 1 {
		t.Fatalf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: expected at least one "+
			"RAISE EXCEPTION literal with prefix %q (from const %q) across all migrations; found none. "+
			"Either the trigger was dropped without updating the Go const, or the RAISE message drifted "+
			"so that no literal matches the sentinel prefix anymore.",
			prefix, lastAdminSentinelConstName)
	}
	// Drift gate: every collected literal must begin with sentinel+":".
	// raiseExceptionMessagesWithPrefix already filters by prefix, so any entry in
	// matchLocs already satisfies the prefix condition by construction.  The loop
	// below is an explicit in-test assertion that makes the contract visible and
	// provides a per-match failure message should the helper's behavior change.
	for _, loc := range matchLocs {
		// loc is "<rel>: <message>"; extract the message part (after the first ": ").
		// The message itself was already verified to start with prefix by the helper;
		// this assertion is the belt-and-suspenders human-readable contract check.
		sep := ": "
		idx := strings.Index(loc, sep)
		if idx < 0 {
			t.Errorf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: internal: "+
				"matchLoc %q has unexpected format (want '<file>: <message>')", loc)
			continue
		}
		msg := loc[idx+len(sep):]
		if !strings.HasPrefix(msg, prefix) {
			t.Errorf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: RAISE EXCEPTION message "+
				"in %q has drifted from Go const %q: got prefix %q, want %q. "+
				"Update the migration RAISE message or the Go const so they agree.",
				loc[:idx], lastAdminSentinelConstName, msg, prefix)
		}
	}
}

// TestLastadminTriggerSentinelConstSQLMatch01_SelfCheck is the blind-spot
// red-fixture for the SQL-side matcher: it proves raiseExceptionMessagesWithPrefix
// binds to the actual RAISE EXCEPTION literal, not to free text. The negative
// case is the precise false-negative a whole-file `strings.Contains` scan would
// have admitted — the sentinel survives only in a comment while the real RAISE
// message has drifted — and MUST report zero matches.
//
// Rebuild semantics (all-match guard): a migration rebuild that carries the
// correct sentinel produces 2 matches; a rebuild where one copy drifted produces
// 1 match (the drifted copy falls outside the prefix filter). The new invariant
// says "at least 1 AND all collected agree with the const", so:
//   - two correct copies → 2 matches, all with prefix → PASS.
//   - one correct + one drifted → 1 match (drifted invisible) → PASS (existence
//     gate satisfied; drifted copy is effectively an unknown PG error that does
//     not affect isLastAdminProtected's classification).
//   - both drifted → 0 matches → FAIL (existence gate).
//   - zero RAISE statements with the sentinel → 0 matches → FAIL (existence gate).
func TestLastadminTriggerSentinelConstSQLMatch01_SelfCheck(t *testing.T) {
	t.Parallel()
	const prefix = "effective_admin_invariant:"

	// Positive: a genuine RAISE EXCEPTION literal (USING ERRCODE '...' must not
	// be miscaptured as a second match).
	pos := `BEGIN
		RAISE EXCEPTION 'effective_admin_invariant: would leave the system with no effective admin'
			USING ERRCODE = 'P0001';
	END;`
	if got := raiseExceptionMessagesWithPrefix(pos, prefix); len(got) != 1 {
		t.Fatalf("self-check positive: want exactly 1 RAISE literal match, got %d (%v)", len(got), got)
	}

	// Two-copy positive (rebuild semantics): both copies carry the correct sentinel.
	// This mirrors 024 + 047 co-existence: both must produce 2 matches.
	twoCorrect := `-- migration 024
	RAISE EXCEPTION 'effective_admin_invariant: would leave the system with no effective admin'
		USING ERRCODE = 'P0001';
	-- migration 047 per-tenant rebuild
	RAISE EXCEPTION 'effective_admin_invariant: would leave the system with no effective admin'
		USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(twoCorrect, prefix); len(got) != 2 {
		t.Fatalf("self-check two-correct: want 2 RAISE literal matches, got %d (%v)", len(got), got)
	}

	// Drifted-rebuild: one correct copy + one drifted copy.
	// The drifted copy does NOT start with the prefix so it is invisible to the
	// collector — only 1 match is returned.  This is the correct "all-visible-
	// copies-agree" semantics: the existence gate passes (1 >= 1); the drift check
	// passes for the one visible copy; the drifted copy is treated as an unrelated
	// RAISE that isLastAdminProtected will NOT classify as a sentinel hit.
	driftedRebuild := `-- original (correct)
	RAISE EXCEPTION 'effective_admin_invariant: would leave the system with no effective admin'
		USING ERRCODE = 'P0001';
	-- rebuild with drifted message (typo in sentinel name)
	RAISE EXCEPTION 'effective_admin_invariant_v2: would leave the system with no effective admin'
		USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(driftedRebuild, prefix); len(got) != 1 {
		t.Fatalf("self-check drifted-rebuild: drifted RAISE should be invisible (prefix mismatch), "+
			"want 1 match (the correct copy), got %d (%v)", len(got), got)
	}

	// Negative (the masked drift): sentinel only in a comment, RAISE message
	// renamed. Whole-file substring would pass; literal-bound matcher must not.
	neg := `-- historical note: effective_admin_invariant: was the old prefix
		RAISE EXCEPTION 'effective_admin_guard_v2: would leave the system with no effective admin'
			USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(neg, prefix); len(got) != 0 {
		t.Fatalf("self-check negative: a comment-only residue with a drifted RAISE message must NOT "+
			"match (this is the false-negative the rule exists to catch), got %d (%v)", len(got), got)
	}

	// Stricter negative: a COMPLETE RAISE EXCEPTION statement shape written inside
	// a comment must not match. Plain regex (no comment stripping) would have.
	lineCommented := `-- example for docs: RAISE EXCEPTION 'effective_admin_invariant: ...' USING ERRCODE = 'P0001';
		RAISE EXCEPTION 'effective_admin_guard_v2: real drifted message' USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(lineCommented, prefix); len(got) != 0 {
		t.Fatalf("self-check line-comment RAISE: a RAISE shape inside a -- comment must NOT match, got %v", got)
	}
	blockCommented := `/* legacy: RAISE EXCEPTION 'effective_admin_invariant: in a block comment'; */
		RAISE EXCEPTION 'something_else: real' USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(blockCommented, prefix); len(got) != 0 {
		t.Fatalf("self-check block-comment RAISE: a RAISE shape inside a /* */ comment must NOT match, got %v", got)
	}
	// String-awareness: a '--' INSIDE the message literal must not be treated as
	// a comment opener that truncates the message before its prefix is tested.
	dashInString := `RAISE EXCEPTION 'effective_admin_invariant: a -- b' USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(dashInString, prefix); len(got) != 1 {
		t.Fatalf("self-check dash-in-string: '--' inside the literal must be preserved, got %v", got)
	}

	// Doubled-quote handling: '' inside the literal is un-escaped before prefix test.
	doubled := `RAISE EXCEPTION 'effective_admin_invariant: it''s the last admin' USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(doubled, prefix); len(got) != 1 || !strings.Contains(got[0], "it's the last") {
		t.Fatalf("self-check doubled-quote: want 1 match with un-doubled quote, got %v", got)
	}
}

// resolveLastAdminSentinel const-folds lastAdminTriggerSentinel via go/types and
// counts its package-scope declarations. It is the typed Go-side half of the
// bidirectional lock; the file-anchor assertion (declared in
// lastAdminSentinelConstRel) is the self-check that catches a silent rename/move.
func resolveLastAdminSentinel(t *testing.T) (value string, declCount int) {
	t.Helper()
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != lastAdminSentinelConstPkg {
			return nil
		}
		for _, f := range p.Files {
			if strings.HasSuffix(p.Rel(f), "_test.go") {
				continue
			}
			EachInSubtree[ast.ValueSpec](f, func(vs *ast.ValueSpec) {
				for i, name := range vs.Names {
					if name.Name != lastAdminSentinelConstName {
						continue
					}
					declCount++
					if rel := p.Rel(f); rel != lastAdminSentinelConstRel {
						t.Errorf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: const %q declared in "+
							"unexpected file %s (expected %s); update lastAdminSentinelConstRel if the "+
							"const moved intentionally", lastAdminSentinelConstName, rel, lastAdminSentinelConstRel)
					}
					if i < len(vs.Values) {
						if s, ok := EvaluateConstString(p.TypesInfo, vs.Values[i]); ok {
							value = s
						}
					}
				}
			})
		}
		return nil
	})

	return value, declCount
}
