//go:build archtest

// INVARIANT: PROJECTION-EVENT-JOURNAL-NO-DELETE-01
//
// PROJECTION-EVENT-JOURNAL-NO-DELETE-01 (EPIC #1504 PR-05 / ADR 202606071600-1504
// §6 I4 / D7) — production Go source MUST NOT issue a DELETE or TRUNCATE against the
// durable projection journal table projection_events. The journal is the append-only
// rebuild-from-0 source for CQRS projections; any code-level removal reintroduces the
// #1504 root bug (a cleaned row breaks rebuild with a permanent error).
//
// # What this guards
//
// This is a SQL-literal scan over the whole production (non-test) tree: every string
// literal is matched against (DELETE FROM | TRUNCATE [TABLE]) [schema.]projection_events.
// A hit is reported as a violation. Production has ZERO hits today (the only literals
// touching the table are the INSERT in adapters/postgres.appendProjectionEvents and the
// replay SELECTs in projection_event_source.go) — this is a regression tripwire.
//
// # Two-guard model (this archtest is the Medium defense-in-depth, NOT the primary)
//
// The PRIMARY append-only guard is Hard and lives at the DB-engine layer: migration
// 058_create_projection_events.sql runs `REVOKE UPDATE, DELETE ON projection_events FROM
// gocell_app`, so the serving role structurally cannot delete at runtime (same
// restricted-role model as #1676). That guard is unbypassable but binds ONLY the
// gocell_app serving role — it does not stop code that runs in an owner/superuser
// context (migrations, admin tooling, a future differently-rolled connection).
//
// This archtest is the complementary Medium defense-in-depth: it catches the
// DELETE/TRUNCATE *literal anywhere in Go source* — including owner-context code that
// REVOKE would not stop at runtime — before it ships. The two guards' coverage is
// complementary, not redundant.
//
// # AI-robust rating — Medium (literal-scan ceiling)
//
// A Hard code-level guard (a sealed handle making a DELETE-on-this-table
// unrepresentable) is unreachable while the statement is a raw SQL string handed to
// pgx — the construct lives below the Go type system. This is the same permanent
// Go-language ceiling as PG-SETLOCAL-FUNNEL-01's literal prong (#1619 won't-do). The
// true Hard upgrade is the DB-engine REVOKE above, already in place since PR-01. So
// this is a sanctioned Medium guard sitting below an in-place Hard primary — not a
// Soft mechanism standing in for a reachable Hard one.
//
// # What is / isn't caught (charter §"强制盲区自检")
//
//   - Caught wherever the table name is a literal: the statement may run through any
//     exec wrapper (the scan is literal-shaped, not call-shaped), and a double-quoted
//     identifier (DELETE FROM "projection_events") still matches — the trailing \b
//     fires before "p" because the leading double-quote is a non-word char.
//   - Blind to a statement with no projection_events literal: SQL built by runtime
//     concatenation / fmt.Sprintf, or a fully dynamic table name, is invisible — the
//     same blind spot as every literal-scanning funnel in this suite.
//   - .sql migration files are not Go source and are out of this scan's reach; the
//     migration down-script's DROP TABLE is gated separately (Migrator.Down +
//     DestructiveDownPermit, #1248).
//   - A same-package raw exec is additionally backstopped by
//     PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01 (append chokepoint) + the DB-engine
//     REVOKE.
//
// # Anti-vacuity
//
// A "expect zero matches" rule is vacuous if the scanner silently scans nothing or the
// regex stops matching. Two guards close that: (1) the production run requires ≥1
// projection_events literal to be observed (tableRefs — proves the scan reached the
// real journal SQL); (2) the RED fixture (projectionnodeletefixture) must yield exactly
// three diagnostics (proves the DELETE/TRUNCATE pattern actually fires and that the
// word boundary does not over-match projection_events_archive).
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// projectionEventsTable is the durable journal table name; a literal containing it
// (case-insensitive) marks a line the scan reached, feeding the anti-vacuity counter.
const projectionEventsTable = "projection_events"

// projectionEventsDeletePattern matches a DELETE/TRUNCATE statement targeting
// projection_events. The leading \b keeps the verb from matching when DELETE/TRUNCATE
// is embedded as the suffix of a larger word (e.g. a hypothetical UNDELETE …); the
// (?:\w+\.)? branch admits a schema qualifier (public.projection_events); the trailing
// \b keeps a prefix collision (projection_events_archive) from matching — _ is a word
// char, so there is no boundary between "events" and "_archive".
var projectionEventsDeletePattern = regexp.MustCompile(
	`(?i)\b(?:DELETE\s+FROM|TRUNCATE(?:\s+TABLE)?)\s+(?:\w+\.)?projection_events\b`,
)

// scanProjectionEventDelete reports every string literal that is a DELETE/TRUNCATE of
// projection_events, and counts literals that merely reference the table into tableRefs
// (anti-vacuity). Reused over both production (expect zero diagnostics, tableRefs ≥ 1)
// and the RED fixture (expect exactly three diagnostics).
func scanProjectionEventDelete(p *Pass, tableRefs *int) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.BasicLit](file, func(lit *ast.BasicLit) {
			if lit.Kind != token.STRING {
				return
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return
			}
			if strings.Contains(strings.ToLower(val), projectionEventsTable) {
				*tableRefs++
			}
			if !projectionEventsDeletePattern.MatchString(val) {
				return
			}
			pos := p.Fset.Position(lit.Pos())
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"PROJECTION-EVENT-JOURNAL-NO-DELETE-01: DELETE/TRUNCATE of projection_events in %s:%d — "+
						"the durable projection journal is append-only (it is the rebuild-from-0 source). "+
						"Removing a row reintroduces the #1504 rebuild bug. Never DELETE/TRUNCATE this table; "+
						"if archive/retention lands, this guard (a pure literal scan with no allowlist) must be "+
						"updated here to exempt the sanctioned archive callsite, citing the archive ADR section "+
						"(per contract-fanout.md).",
					rel, pos.Line,
				),
			})
		})
	}
	return diags
}

// TestProjectionEventJournalNoDelete01 asserts no production string literal issues a
// DELETE/TRUNCATE against projection_events, and that the scan actually reached the
// journal SQL (anti-vacuity).
func TestProjectionEventJournalNoDelete01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var tableRefs int
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanProjectionEventDelete(p, &tableRefs)
	})
	if tableRefs == 0 {
		diags = append(diags, Diagnostic{
			Message: "PROJECTION-EVENT-JOURNAL-NO-DELETE-01 anti-vacuity: no projection_events literal " +
				"observed in production — the scan reached no journal SQL (table renamed, source moved into " +
				"*_test.go which the Production scope excludes, or the walk regressed), so the no-DELETE " +
				"tripwire would be silently vacuous.",
		})
	}
	Report(t, "PROJECTION-EVENT-JOURNAL-NO-DELETE-01", diags)
}

// TestProjectionEventJournalNoDelete01_RedFixture is the negative control: the scanner
// run against projectionnodeletefixture must fire on exactly the three RED cases
// (badDelete / badTruncate / badTruncateSchemaQualified) and leave the GREEN controls
// (INSERT / SELECT / sibling-table DELETE) untouched.
func TestProjectionEventJournalNoDelete01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var throwaway int
	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/projectionnodeletefixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			found += len(scanProjectionEventDelete(p, &throwaway))
			return nil
		})
	assert.Equal(t, 3, found,
		"PROJECTION-EVENT-JOURNAL-NO-DELETE-01 RED fixture self-check FAILED: expected exactly 3 "+
			"violations (badDelete + badTruncate + badTruncateSchemaQualified). Got %d — found<3 means the "+
			"DELETE/TRUNCATE pattern missed a form (scanner regression); found>3 means it over-matched "+
			"(e.g. flagged the GREEN INSERT/SELECT or the projection_events_archive sibling table).", found)
}
