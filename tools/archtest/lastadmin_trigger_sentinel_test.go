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
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

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

	// lastAdminMigrationsDir is the flat migrations directory (NNN_xxx.sql).
	lastAdminMigrationsDir = "adapters/postgres/migrations"
)

// INVARIANT: LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01
//
// TestLastadminTriggerSentinelConstSQLMatch01 enforces the bidirectional lock:
//   - Go ⇒ SQL: the const value, suffixed with ":" (the colon-delimited form
//     `isLastAdminProtected` actually matches against), MUST appear in exactly
//     one migration file. ("at least one" would not bite if the message were
//     deleted while the const survives; "exactly one" additionally pins the
//     single-source-of-truth — see the rebuild note below.)
//   - SQL ⇒ Go: the migration message is matched by const VALUE (typed
//     const-eval), so editing the SQL message without updating the const is the
//     same failure as the inverse.
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
// with the same message would add a 2nd colon-form occurrence and trip the
// "exactly one" assertion. That is the intended forcing function (contract-
// fanout discipline) — such a rebuild must update this archtest, not weaken it.
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

	// ── SQL side: the colon-delimited RAISE form must appear in exactly one
	// migration. needle mirrors isLastAdminProtected's `sentinel+":"` match. ──
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{lastAdminMigrationsDir},
		scanner.MatchRels(func(rel string) bool {
			return filepath.ToSlash(filepath.Dir(rel)) == lastAdminMigrationsDir
		}),
	)
	needle := value + ":"
	var matched []string
	scanner.EachContentFile(t, scope, []string{".sql"}, func(_ *testing.T, fc scanner.ContentContext) {
		if strings.Contains(string(fc.Bytes), needle) {
			matched = append(matched, fc.Rel)
		}
	})
	if len(matched) != 1 {
		t.Fatalf("LASTADMIN-TRIGGER-SENTINEL-CONST-SQL-MATCH-01: the trigger message prefix %q "+
			"(from const %q) must appear in exactly one migration file; found in %v. Either the "+
			"migration RAISE EXCEPTION message drifted from the Go const, or a rebuild migration "+
			"added a second occurrence (update this archtest if the rebuild is intentional).",
			needle, lastAdminSentinelConstName, matched)
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
