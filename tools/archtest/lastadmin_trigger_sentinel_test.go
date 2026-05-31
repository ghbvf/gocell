// invariants:
//   - INVARIANT: LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01
//
// LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01 — the Go const
// `lastAdminTriggerSentinel` (cells/accesscore/internal/adapters/postgres/
// lastadmin.go) and the `RAISE EXCEPTION` message of the
// effective_admin_invariant_fn trigger (adapters/postgres/migrations/
// 024_effective_admin_invariant.sql) are two artifacts in two languages that
// MUST stay byte-consistent: `isLastAdminProtected` classifies the PL/pgSQL
// exception into errcode.ErrAuthLastAdminProtected (HTTP 403) by matching
// `strings.HasPrefix(pgErr.Message, lastAdminTriggerSentinel+":")`. A future
// migration that renames the trigger message would silently turn that 403 into
// a generic 500 with no compile/test signal. This invariant is the static guard
// that was missing (gh #740 / PR #578 DX4 S5 OUT_OF_SCOPE finding).
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
// strings.HasPrefix(pgErr.Message, sentinel+":"). A stray comment or unrelated
// string that merely contains the prefix is NOT a RAISE literal and does not
// count, which is the false-negative a whole-file substring scan would miss.
func raiseExceptionMessagesWithPrefix(sqlContent, prefix string) []string {
	var out []string
	for _, m := range raiseExceptionRe.FindAllStringSubmatch(sqlContent, -1) {
		// Un-double '' to recover the message Postgres actually raises.
		msg := strings.ReplaceAll(m[1], "''", "'")
		if strings.HasPrefix(msg, prefix) {
			out = append(out, msg)
		}
	}
	return out
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
//     exactly one `RAISE EXCEPTION '...'` message literal across all migrations.
//     The match is bound to the extracted RAISE literal (not a whole-file
//     substring), so a comment or unrelated string carrying the prefix while the
//     real RAISE message has drifted does NOT satisfy the rule — that drift is
//     exactly the runtime 403→500 regression this invariant exists to catch.
//     ("at least one" would not bite if the message were deleted while the const
//     survives; "exactly one literal" additionally pins single-source-of-truth —
//     see the rebuild note below.)
//   - SQL ⇒ Go: the RAISE literal is matched by const VALUE (typed const-eval),
//     so editing the SQL message without updating the const is the same failure
//     as the inverse.
//
// Blind-spot self-check (AI-robust §盲区自检): the chosen helpers are
// RunTypedProduction + EvaluateConstString (Go side) and EachContentFile (SQL
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
//
// Maintenance note: a future migration that legitimately *rebuilds* the trigger
// with the same message would add a 2nd matching RAISE literal and trip the
// "exactly one literal" assertion. That is the intended forcing function
// (contract-fanout discipline) — such a rebuild must update this archtest, not
// weaken it.
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

	// ── SQL side: exactly one RAISE EXCEPTION literal across all migrations must
	// have the `sentinel+":"` prefix. Bound to the extracted RAISE literal (not a
	// whole-file substring) so comment/identifier residue cannot mask drift. ──
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{lastAdminMigrationsDir},
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
	if len(matchLocs) != 1 {
		t.Fatalf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: expected exactly one "+
			"RAISE EXCEPTION literal with prefix %q (from const %q) across all migrations; found %d: %v. "+
			"Either the migration RAISE EXCEPTION message drifted from the Go const, or a rebuild "+
			"migration added a second matching literal (update this archtest if the rebuild is intentional).",
			prefix, lastAdminSentinelConstName, len(matchLocs), matchLocs)
	}
}

// TestLastadminTriggerSentinelConstSQLMatch01_SelfCheck is the blind-spot
// red-fixture for the SQL-side matcher: it proves raiseExceptionMessagesWithPrefix
// binds to the actual RAISE EXCEPTION literal, not to free text. The negative
// case is the precise false-negative a whole-file `strings.Contains` scan would
// have admitted — the sentinel survives only in a comment while the real RAISE
// message has drifted — and MUST report zero matches.
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

	// Negative (the masked drift): sentinel only in a comment, RAISE message
	// renamed. Whole-file substring would pass; literal-bound matcher must not.
	neg := `-- historical note: effective_admin_invariant: was the old prefix
		RAISE EXCEPTION 'effective_admin_guard_v2: would leave the system with no effective admin'
			USING ERRCODE = 'P0001';`
	if got := raiseExceptionMessagesWithPrefix(neg, prefix); len(got) != 0 {
		t.Fatalf("self-check negative: a comment-only residue with a drifted RAISE message must NOT "+
			"match (this is the false-negative the rule exists to catch), got %d (%v)", len(got), got)
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
	_ = RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
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
