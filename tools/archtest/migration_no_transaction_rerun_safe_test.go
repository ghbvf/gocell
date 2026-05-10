package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// INVARIANT: MIGRATION-NO-TRANSACTION-RERUN-SAFE-01
//
// TestMigrationNoTransactionRerunSafe01 enforces MIGRATION-NO-TRANSACTION-
// RERUN-SAFE-01: every DDL statement in a `-- +goose NO TRANSACTION` migration
// MUST be rerun-safe — `IF NOT EXISTS` / `IF EXISTS` for plain CREATE/DROP/
// ADD COLUMN/DROP COLUMN, or wrapped in a DO block with explicit state guards
// for statements PostgreSQL has historically not supported `IF NOT EXISTS` on
// (e.g. ADD CONSTRAINT before PG 16+).
//
// Rationale: NO TRANSACTION is required for `CREATE INDEX CONCURRENTLY`, but
// it removes the migration's atomicity. If such a migration is interrupted
// (DDL succeeded but version not recorded) and rerun on a fresh deploy, every
// non-idempotent DDL fails with `42701 duplicate column` / `42710 duplicate
// object` and blocks startup. Wrapping every DDL in `IF [NOT] EXISTS` (or a
// DO block) makes interrupted migrations rerun-safe.
//
// ref: squawk prefer-robust-stmts (https://squawkhq.com/docs/prefer-robust-stmts)
// ref: squawk ban-concurrent-index-creation-in-transaction
//
// Scope: scans `adapters/postgres/migrations/*.sql`. Files without
// `-- +goose NO TRANSACTION` are skipped (transactional migrations roll back
// on failure and do not need IF NOT EXISTS).
//
// No allowlist: per the N8 design directive, 014 was retroactively patched to
// pass this rule rather than carved out, since adding `IF NOT EXISTS` to an
// already-applied migration is idempotent and harmless on existing DBs.
func TestMigrationNoTransactionRerunSafe01(t *testing.T) {
	root := findModuleRoot(t)
	// MatchRels limits to single-level scan matching the docstring's
	// "migrations/*.sql" promise. The migrations/ directory is flat by
	// convention (NNN_xxx.sql files); a future sub-directory would carry
	// non-migration files (e.g. sql utilities) that this rule shouldn't
	// touch. Without this predicate DirsScope would walk recursively.
	scope := scanner.DirsScope(root, []string{"adapters/postgres/migrations"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.ToSlash(filepath.Dir(rel)) == "adapters/postgres/migrations"
		}),
	)

	noTxMarker := regexp.MustCompile(`(?m)^\s*--\s*\+goose\s+NO\s+TRANSACTION\b`)
	scanned := 0

	scanner.EachContentFile(t, scope, []string{".sql"}, func(_ *testing.T, fc scanner.ContentContext) {
		if !noTxMarker.Match(fc.Bytes) {
			return
		}
		scanned++
		for _, v := range scanRerunSafetyViolations(string(fc.Bytes)) {
			t.Errorf("MIGRATION-NO-TRANSACTION-RERUN-SAFE-01: %s:%d: %s",
				fc.Rel, v.line, v.message)
		}
	})

	if scanned == 0 {
		t.Fatal("MIGRATION-NO-TRANSACTION-RERUN-SAFE-01: no NO TRANSACTION migration files were scanned; " +
			"the rule must apply to at least one file (014 / 015 / 011 should all be present)")
	}
}

type rerunViolation struct {
	line    int
	message string
}

// scanRerunSafetyViolations returns violations of the rerun-safety rule for a
// NO TRANSACTION migration body. Logic:
//  1. Strip line comments (`-- ...`).
//  2. Strip DO $$ ... $$; blocks — DDL inside DO blocks is the documented
//     escape hatch for statements PostgreSQL does not support `IF NOT EXISTS`
//     on (e.g. ADD CONSTRAINT before PG 16+); the DO block author is
//     responsible for `IF NOT EXISTS`-equivalent state guards (e.g.
//     `pg_constraint` lookup before ALTER TABLE ADD CONSTRAINT).
//  3. For each remaining statement, flag DDL keywords that are missing the
//     idempotency guard.
func scanRerunSafetyViolations(body string) []rerunViolation {
	var out []rerunViolation

	stripped, lineMap := stripCommentsAndDOBlocks(body)

	// DDL detectors. Each regex matches the START of an offending DDL phrase
	// without `IF [NOT] EXISTS`. The negative lookahead is approximated via
	// post-match string check because Go RE2 lacks lookaround.
	type rule struct {
		pattern *regexp.Regexp
		guard   string // expected substring after match (e.g. "IF NOT EXISTS")
		label   string
	}
	rules := []rule{
		{
			pattern: regexp.MustCompile(`(?i)\bCREATE\s+(UNIQUE\s+)?INDEX\s+(CONCURRENTLY\s+)?`),
			guard:   "IF NOT EXISTS",
			label:   "CREATE INDEX",
		},
		{
			pattern: regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+`),
			guard:   "IF NOT EXISTS",
			label:   "CREATE TABLE",
		},
		{
			pattern: regexp.MustCompile(`(?i)\bDROP\s+INDEX\s+(CONCURRENTLY\s+)?`),
			guard:   "IF EXISTS",
			label:   "DROP INDEX",
		},
		{
			pattern: regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+`),
			guard:   "IF EXISTS",
			label:   "DROP TABLE",
		},
		{
			pattern: regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+\S+\s+ADD\s+COLUMN\s+`),
			guard:   "IF NOT EXISTS",
			label:   "ALTER TABLE ADD COLUMN",
		},
		{
			pattern: regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+\S+\s+DROP\s+COLUMN\s+`),
			guard:   "IF EXISTS",
			label:   "ALTER TABLE DROP COLUMN",
		},
		{
			// PostgreSQL only added IF NOT EXISTS for ADD CONSTRAINT in v16,
			// so projects targeting older PG must wrap ADD CONSTRAINT in a DO
			// block with a pg_constraint lookup. The DO-block strip step
			// removes such guarded statements before this rule runs, so a hit
			// here means a BARE `ALTER TABLE ... ADD CONSTRAINT` outside any
			// DO block — which is not rerun-safe and is rejected.
			pattern: regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+\S+\s+ADD\s+CONSTRAINT\s+`),
			guard:   "IF NOT EXISTS",
			label:   "ALTER TABLE ADD CONSTRAINT (must wrap in DO block with pg_constraint guard for PG <16)",
		},
		{
			pattern: regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+\S+\s+DROP\s+CONSTRAINT\s+`),
			guard:   "IF EXISTS",
			label:   "ALTER TABLE DROP CONSTRAINT",
		},
	}

	for _, r := range rules {
		for _, loc := range r.pattern.FindAllStringIndex(stripped, -1) {
			start := loc[0]
			matched := stripped[loc[0]:loc[1]]
			// Look ahead up to 64 chars for the guard.
			end := loc[1] + 64
			if end > len(stripped) {
				end = len(stripped)
			}
			tail := stripped[loc[1]:end]
			if !containsCaseInsensitive(matched+tail, r.guard) {
				line := lineMap[start]
				out = append(out, rerunViolation{
					line:    line,
					message: r.label + " is missing `" + r.guard + "` (NO TRANSACTION migration must be rerun-safe)",
				})
			}
		}
	}

	return out
}

func containsCaseInsensitive(s, sub string) bool {
	return strings.Contains(strings.ToUpper(s), strings.ToUpper(sub))
}

// stripCommentsAndDOBlocks removes `-- line comments` and `DO $tag$ ... $tag$;`
// blocks (multi-line) from body, replacing each removed character with a space
// so byte offsets in the output map back to original line numbers via
// lineMap[i] = line number (1-indexed) of byte i in the input.
func stripCommentsAndDOBlocks(body string) (string, []int) {
	out := []byte(body)
	lineMap := make([]int, len(body))
	line := 1
	for i, c := range []byte(body) {
		lineMap[i] = line
		if c == '\n' {
			line++
		}
	}

	// Strip line comments.
	for i := 0; i < len(out)-1; i++ {
		if out[i] == '-' && out[i+1] == '-' {
			j := i
			for j < len(out) && out[j] != '\n' {
				out[j] = ' '
				j++
			}
			i = j
		}
	}

	// Strip DO $tag$ ... $tag$; blocks. Tag may be empty ($$) or named
	// ($migration_014$). Match opening `DO` keyword, then the tag, then the
	// matching closing tag, optionally followed by `;`.
	doRe := regexp.MustCompile(`(?is)\bDO\s+(\$[A-Za-z0-9_]*\$)`)
	for {
		loc := doRe.FindIndex(out)
		if loc == nil {
			break
		}
		matchedTag := doRe.FindSubmatch(out[loc[0]:loc[1]])
		if len(matchedTag) < 2 {
			break
		}
		tag := matchedTag[1]
		// Find closing tag after loc[1].
		closeIdx := strings.Index(string(out[loc[1]:]), string(tag))
		if closeIdx < 0 {
			break
		}
		end := loc[1] + closeIdx + len(tag)
		// Consume trailing `;` if present.
		if end < len(out) && out[end] == ';' {
			end++
		}
		for k := loc[0]; k < end; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}

	return string(out), lineMap
}
