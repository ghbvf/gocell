//go:build archtest

// INVARIANT: REGISTRATION-EVENT-NO-DELETE-01
//
// REGISTRATION-EVENT-NO-DELETE-01 (#2386 / ADR 202606162119-303 §"Amendment
// 2026-06-19 — #2392/#2386") — production Go source MUST NOT issue a DELETE or
// TRUNCATE against the append-only migration-history table
// contract_registration_events. The history table is the source of truth for each
// registration's lifecycle (the projection table folds from it); any code-level
// removal breaks lifecycle reconstruction and the append-only guarantee.
//
// # What this guards
//
// This is a SQL-literal scan over the whole production (non-test) tree: every string
// literal is matched against (DELETE FROM | TRUNCATE [TABLE]) [schema.]contract_registration_events.
// A hit is reported as a violation. Production has ZERO hits today (the only literals
// touching the table are the INSERT in registry_repo.appendEvent and the History replay
// SELECT) — this is a regression tripwire.
//
// # Two-guard model (this archtest is the Medium defense-in-depth, NOT the primary)
//
// The PRIMARY append-only guard is Hard and lives at the DB-engine layer: migration
// 066_create_contract_registrations.sql runs `REVOKE UPDATE, DELETE ON
// contract_registration_events FROM gocell_app`, so the serving role structurally cannot
// delete at runtime (same restricted-role model as #1676, mirrors migration 058 for
// projection_events). That guard is unbypassable but binds ONLY the gocell_app serving
// role — it does not stop code that runs in an owner/superuser context (migrations, admin
// tooling, a future differently-rolled connection).
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
// Go-language ceiling as PROJECTION-EVENT-JOURNAL-NO-DELETE-01 and PG-SETLOCAL-FUNNEL-01's
// literal prong. The true Hard upgrade is the DB-engine REVOKE above, already in place
// since migration 066. So this is a sanctioned Medium guard sitting below an in-place
// Hard primary — not a Soft mechanism standing in for a reachable Hard one.
//
// # What is / isn't caught (charter §"强制盲区自检")
//
//   - Caught wherever the table name is a compile-time constant: the statement may run
//     through any exec wrapper (the scan is value-shaped, not call-shaped); a
//     double-quoted identifier (DELETE FROM "contract_registration_events",
//     "public"."contract_registration_events") matches; and a value assembled by
//     compile-time concatenation of string constants ("DELETE FROM " +
//     "contract_registration_events", or "DELETE FROM " + registrationEventsTable) is
//     folded by go/types (EvaluateConstString) and matched as a whole.
//   - Blind to a table name that arrives as a runtime (non-const) value: runtime string
//     concatenation, fmt.Sprintf("…%s…", table), or a fully dynamic name produces no
//     constant contract_registration_events string to fold — the permanent literal/const-scan
//     ceiling, same family as every literal-scanning funnel in this suite.
//   - .sql migration files are not Go source and are out of this scan's reach; the
//     migration down-script's DROP TABLE is gated separately (Migrator.Down +
//     DestructiveDownPermit, #1248).
//
// # Anti-vacuity
//
// A "expect zero matches" rule is vacuous if the scanner silently scans nothing or the
// regex stops matching. Two guards close that: (1) the production run requires ≥1
// contract_registration_events literal to be observed (tableRefs — proves the scan reached
// the real history SQL: appendEvent INSERT + History SELECT in registry_repo.go); (2) the
// RED fixture (registrationnodeletefixture) must yield exactly eight diagnostics (proves
// the pattern fires on bare / quoted / schema-qualified and compile-time-concatenated forms,
// and that neither the word boundary over-matches contract_registration_events_archive nor
// the const fold flags a runtime-assembled name).
package archtest

import (
	"fmt"
	"go/ast"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// registrationEventsTable is the append-only history table name; a literal containing it
// (case-insensitive) marks a line the scan reached, feeding the anti-vacuity counter.
const registrationEventsTable = "contract_registration_events"

// registrationEventsDeletePattern matches a DELETE/TRUNCATE statement targeting the
// contract_registration_events table. The leading \b keeps the verb from matching when
// DELETE/TRUNCATE is embedded as the suffix of a larger word (e.g. a hypothetical
// UNDELETE …). The table token accepts an optional schema qualifier and a bare OR
// double-quoted identifier for either part — contract_registration_events,
// "contract_registration_events", public.contract_registration_events,
// "public"."contract_registration_events" — all legal PostgreSQL references to the same
// table (delimited identifiers per the SQL lexical spec). The bare table alternative ends
// in \b so a prefix collision (contract_registration_events_archive) does not match (_ is
// a word char, so there is no boundary between "events" and "_archive"); the quoted
// alternative is self-delimiting by its closing quote.
var registrationEventsDeletePattern = regexp.MustCompile(
	`(?i)\b(?:DELETE\s+FROM|TRUNCATE(?:\s+TABLE)?)\s+(?:(?:"\w+"|\w+)\.)?(?:"contract_registration_events"|contract_registration_events\b)`,
)

// scanRegistrationEventDelete reports every compile-time-constant SQL string that is a
// DELETE/TRUNCATE of contract_registration_events, and counts constant strings that merely
// reference the table into tableRefs (anti-vacuity). It scans concrete constant-bearing
// node types (BasicLit, BinaryExpr, Ident, SelectorExpr) via four EachInSubtree passes,
// calling EvaluateConstString on each candidate. Partial operands of a concatenation
// (e.g. "DELETE FROM " or "contract_registration_events" alone) do not individually match
// the pattern because registrationEventsDeletePattern requires the DELETE/TRUNCATE verb and
// the table name to be adjacent in the same folded string, so visiting operands separately
// does not produce false positives. The four passes scan disjoint node kinds, so no node is
// re-visited; a diagnostic is emitted once per matching node position. The RED fixture's 8
// cases (6 BasicLit + 2 BinaryExpr) lock the exact count as the regression backstop.
// tableRefs may exceed 1 in production (over-counting is harmless — the anti-vacuity guard
// only requires ≥ 1). Reused over both production (expect zero diagnostics, tableRefs ≥ 1)
// and the RED fixture.
func scanRegistrationEventDelete(p *Pass, tableRefs *int) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		check := func(expr ast.Expr) {
			s, ok := EvaluateConstString(p.TypesInfo, expr)
			if !ok {
				return
			}
			if strings.Contains(strings.ToLower(s), registrationEventsTable) {
				*tableRefs++
			}
			if registrationEventsDeletePattern.MatchString(s) {
				pos := p.Fset.Position(expr.Pos())
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"REGISTRATION-EVENT-NO-DELETE-01: DELETE/TRUNCATE of contract_registration_events in %s:%d — "+
							"the registration migration history is append-only (it is the lifecycle source of truth). "+
							"Removing a row breaks lifecycle reconstruction and the append-only guarantee. Never "+
							"DELETE/TRUNCATE this table; if archive/retention lands, this guard (a pure literal scan "+
							"with no allowlist) must be updated here to exempt the sanctioned archive callsite, citing "+
							"the archive ADR section (per contract-fanout.md).",
						rel, pos.Line,
					),
				})
			}
		}
		EachInSubtree[ast.BasicLit](file, func(n *ast.BasicLit) { check(n) })
		EachInSubtree[ast.BinaryExpr](file, func(n *ast.BinaryExpr) { check(n) })
		EachInSubtree[ast.Ident](file, func(n *ast.Ident) { check(n) })
		EachInSubtree[ast.SelectorExpr](file, func(n *ast.SelectorExpr) { check(n) })
	}
	return diags
}

// TestRegistrationEventNoDelete01 asserts no production string literal issues a
// DELETE/TRUNCATE against contract_registration_events, and that the scan actually reached
// the history SQL (anti-vacuity).
func TestRegistrationEventNoDelete01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var tableRefs int
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanRegistrationEventDelete(p, &tableRefs)
	})
	if tableRefs == 0 {
		diags = append(diags, Diagnostic{
			Message: "REGISTRATION-EVENT-NO-DELETE-01 anti-vacuity: no contract_registration_events literal " +
				"observed in production — the scan reached no history SQL (table renamed, source moved into " +
				"*_test.go which the Production scope excludes, or the walk regressed), so the no-DELETE " +
				"tripwire would be silently vacuous.",
		})
	}
	Report(t, "REGISTRATION-EVENT-NO-DELETE-01", diags)
}

// TestRegistrationEventNoDelete01_RedFixture is the negative control: the scanner run
// against registrationnodeletefixture must fire on exactly the eight RED cases (bare /
// quoted / schema-qualified DELETE+TRUNCATE + two compile-time-concatenated forms) and
// leave the GREEN controls (INSERT / SELECT / sibling-table DELETE / runtime-concat)
// untouched.
func TestRegistrationEventNoDelete01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var throwaway int
	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/registrationnodeletefixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			found += len(scanRegistrationEventDelete(p, &throwaway))
			return nil
		})
	assert.Equal(t, 8, found,
		"REGISTRATION-EVENT-NO-DELETE-01 RED fixture self-check FAILED: expected exactly 8 "+
			"violations (bare/quoted/schema-qualified DELETE+TRUNCATE + 2 compile-time-concat forms). Got "+
			"%d — found<8 means the pattern missed a form (quoted identifier or const-fold regression); "+
			"found>8 means it over-matched (e.g. flagged a GREEN INSERT/SELECT, the "+
			"contract_registration_events_archive sibling, or a runtime-assembled name).", found)
}
