// Package archtest_test — pg_schema_guard_invariants_test.go
//
// File invariants:
//   - INVARIANT: MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01
//   - INVARIANT: MIGRATION-DESTRUCTIVE-UP-REBUILD-GUC-01
//   - INVARIANT: SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01

package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ---------------------------------------------------------------------------
// INVARIANT: MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01 (Medium AI-robust)
//
// Every migration Down section that contains a destructive DDL operation
// (DROP TABLE, DROP COLUMN, DROP CONSTRAINT, DROP INDEX, DROP TRIGGER,
// DROP FUNCTION, TRUNCATE, DELETE FROM) must include the GUC fail-closed
// guard: current_setting('gocell.allow_destructive_down', ...).
//
// Rationale: the Go-layer DestructiveDownPermit already protects the
// Migrator.Down code path; this archtest ensures the SQL layer cannot be
// bypassed by direct goose CLI / psql usage. Any migration that lacks the
// guard is caught at archtest time, not at runtime.
//
// AI-robust: Medium — string pattern match on SQL content caught at test time.
// ---------------------------------------------------------------------------

// destructiveOps are the DDL tokens that classify a Down section as
// "data-destructive" — operations that delete row-level or column-level data
// and require the SQL-side fail-closed GUC guard.
//
// Scope rationale:
//   - DROP TABLE: destroys all row data in the table. Operator opt-in required.
//   - TRUNCATE: destroys all row data in the table. Operator opt-in required.
//   - DELETE FROM: destroys row data matching a predicate. Operator opt-in required.
//   - DROP COLUMN: destroys all data stored in the column. Operator opt-in required;
//     a rollback that drops a column is irreversible without a DB restore or
//     manual backfill. Included alongside DROP TABLE / TRUNCATE for consistency.
//
// Schema-only operations (DROP INDEX, DROP CONSTRAINT, DROP TRIGGER,
// DROP FUNCTION) are NOT in this set — they alter schema shape but do not
// delete stored row or column data; binding them to the same gate would
// over-pressurize routine schema evolution.
var destructiveOps = []string{
	"DROP TABLE",
	"DROP COLUMN",
	"TRUNCATE",
	"DELETE FROM",
}

// gucGuardRE matches the required GUC check pattern in the Down section.
var gucGuardRE = regexp.MustCompile(`current_setting\s*\(\s*'gocell\.allow_destructive_down'`)

// gooseDownMarker marks the start of the Down section in a migration file.
const gooseDownMarker = "-- +goose Down"

// gucGuardGrandfatheredMigrations is intentionally empty.
//
// S3F retrofits the GUC fail-closed guard on every pre-S3F migration whose
// Down section drops row-level data (007, 012, 014/015 already share the
// refresh_tokens GUC predecessor; 001/004/008/020 retrofitted by S3F). All
// data-destructive Down sections in the repository now carry the guard.
//
// This map remains as a typed extension point: if a future migration
// genuinely cannot host the guard (e.g. requires a non-postgres dialect),
// add it here with a concrete reason. An empty map is the intended steady
// state — every entry that appears later must close cleanly, not stay as
// a permanent carve-out (per CLAUDE.md "no soft fallback" guidance).
var gucGuardGrandfatheredMigrations = map[string]string{}

// TestArchtest_MigrationDestructiveDownGUCGuard asserts that every migration
// file with a destructive Down section contains the GUC fail-closed guard,
// unless it is in the grandfathered allowlist.
//
// Migrations in gucGuardGrandfatheredMigrations are temporarily exempt (pre-S3F).
// Any migration 022+ must include the guard for destructive Down sections.
//
// To confirm this test catches violations (TDD RED):
//  1. After Agent A adds guards to 007/017/018/019, temporarily remove the
//     GUC guard block from 017_users.sql Down section.
//  2. Run: go test ./tools/archtest/... -run TestArchtest_MigrationDestructiveDownGUCGuard
//  3. Test must FAIL. Restore the guard, test must PASS.
func TestArchtest_MigrationDestructiveDownGUCGuard(t *testing.T) {
	root := findModuleRoot(t)
	// DirsScope takes paths relative to the module root.
	scope := scanner.DirsScope(root, []string{"adapters/postgres/migrations"})
	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, cc scanner.ContentContext) {
		base := filepath.Base(cc.AbsPath)

		// Skip grandfathered pre-S3F migrations.
		if reason, grandfathered := gucGuardGrandfatheredMigrations[base]; grandfathered {
			t.Logf("MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01: %s grandfathered (%s)", base, reason)
			return
		}

		content := string(cc.Bytes)

		// Split into Up and Down sections.
		downIdx := strings.Index(content, gooseDownMarker)
		if downIdx < 0 {
			// No Down section — skip.
			return
		}
		downSection := content[downIdx+len(gooseDownMarker):]

		// Check if the Down section has any destructive operations (case-insensitive).
		upperDown := strings.ToUpper(downSection)
		isDestructive := false
		for _, op := range destructiveOps {
			if strings.Contains(upperDown, op) {
				isDestructive = true
				break
			}
		}
		if !isDestructive {
			return
		}

		// Destructive Down must have the GUC guard.
		if !gucGuardRE.MatchString(downSection) {
			assert.Fail(t,
				"migration Down section is destructive but lacks the GUC fail-closed guard",
				"file: %s\n"+
					"  Down section contains destructive DDL but no current_setting('gocell.allow_destructive_down') check.\n"+
					"  Add the guard block at the top of the -- +goose Down section:\n"+
					"    DO $$ BEGIN\n"+
					"      IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN\n"+
					"        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';\n"+
					"      END IF;\n"+
					"    END $$;\n"+
					"  This ensures direct goose CLI / psql usage cannot bypass the Go-layer DestructiveDownPermit.\n"+
					"  Rule: MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01",
				cc.Rel,
			)
		}
	})
}

// ---------------------------------------------------------------------------
// INVARIANT: MIGRATION-DESTRUCTIVE-UP-REBUILD-GUC-01 (Medium AI-robust)
//
// Every migration Up (forward) section that destroys an entire table's data
// (TRUNCATE or DROP TABLE) MUST gate behind a DEDICATED forward-rebuild GUC of
// the shape current_setting('gocell.allow_<domain>_rebuild', ...). It MUST NOT
// rely on the destructive-DOWN GUC (gocell.allow_destructive_down) for a forward
// operation: conflating "I am rolling back" with "I am rebuilding forward" is a
// semantic-mixing footgun that hides the blast radius of a forward TRUNCATE
// behind a rollback toggle.
//
// Rationale (Strong-Migrations philosophy): a forward migration that wipes a
// whole table is "dangerous by default" and must carry an explicit operator
// opt-in distinct from the rollback opt-in. Migration 043_audit_entries_v2
// established the dedicated-GUC pattern (gocell.allow_audit_rebuild) precisely
// to decouple forward rebuild from destructive-down; this archtest makes that
// decoupling a machine rule so a later forward-TRUNCATE migration cannot silently
// reuse the down GUC (issue #1229 review F4/C2).
//
// Scope: whole-table destroyers only (TRUNCATE, DROP TABLE). Column-grained ops
// (DROP COLUMN) and predicate deletes (DELETE FROM) are a different, finer risk
// class and are intentionally out of scope — they are not table rebuilds.
//
// Bound set today: {012_refresh_tokens_rebuild, 043_audit_entries_v2,
// 044_outbox_entries_principal}, all of which carry a dedicated allow_*_rebuild
// GUC. No grandfathering — an empty steady state is the intended shape.
//
// AI-robust: Medium — string/regex match on SQL content caught at test time
// (sibling of MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01). The runtime gate itself
// (the GUC fail-closed RAISE EXCEPTION in the SQL) is the Hard backstop; this
// archtest is the regression guard that the gate is present and uses the
// dedicated forward GUC.
//
// Tool blind spot: a destructive op token appearing inside a SQL comment must
// NOT trigger the requirement (e.g. 040 mentions "TRUNCATE saga_events" in a
// comment). stripSQLLineComments removes -- line comments before the scan; a
// destructive token hidden inside a quoted string literal is not handled (no
// such case exists in the migration corpus today) — documented, not enforced.
// ---------------------------------------------------------------------------

// forwardRebuildOps are the whole-table data-destroying DDL tokens that, when
// present in a forward (Up) section, require the dedicated forward-rebuild GUC.
var forwardRebuildOps = []string{
	"TRUNCATE",
	"DROP TABLE",
}

// forwardRebuildGUCRE matches the required dedicated forward-rebuild GUC in an
// Up section: current_setting('gocell.allow_<domain>_rebuild', ...). It does NOT
// match gocell.allow_destructive_down (the rollback GUC), which is the whole
// point — a forward destructive rebuild must use its own opt-in.
var forwardRebuildGUCRE = regexp.MustCompile(`current_setting\s*\(\s*'gocell\.allow_[a-z_]*rebuild'`)

// stripSQLLineComments removes -- line comments from SQL so destructive-op
// detection inspects executable statements only, not prose in runbook comments.
func stripSQLLineComments(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// TestArchtest_MigrationDestructiveUpRebuildGUCGuard asserts that every migration
// whose Up section TRUNCATEs or DROPs a whole table gates behind a dedicated
// forward-rebuild GUC (gocell.allow_*_rebuild), not the destructive-down GUC.
//
// To confirm this test catches violations (TDD RED):
//  1. In 044_outbox_entries_principal.sql Up block, change
//     current_setting('gocell.allow_outbox_rebuild', ...) to
//     current_setting('gocell.allow_destructive_down', ...).
//  2. Run: go test ./tools/archtest/... -run TestArchtest_MigrationDestructiveUpRebuildGUCGuard
//  3. Test must FAIL. Restore the dedicated GUC, test must PASS.
func TestArchtest_MigrationDestructiveUpRebuildGUCGuard(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"adapters/postgres/migrations"})
	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, cc scanner.ContentContext) {
		upSection := stripSQLLineComments(upSectionOf(string(cc.Bytes)))
		upperUp := strings.ToUpper(upSection)

		isWholeTableDestructive := false
		for _, op := range forwardRebuildOps {
			if strings.Contains(upperUp, op) {
				isWholeTableDestructive = true
				break
			}
		}
		if !isWholeTableDestructive {
			return
		}

		if !forwardRebuildGUCRE.MatchString(upSection) {
			assert.Fail(t,
				"forward (Up) section destroys a whole table but lacks a dedicated forward-rebuild GUC",
				"file: %s\n"+
					"  Up section contains TRUNCATE / DROP TABLE but no "+
					"current_setting('gocell.allow_<domain>_rebuild') guard.\n"+
					"  A forward destructive rebuild MUST use its own opt-in GUC, NOT "+
					"gocell.allow_destructive_down (the rollback gate). Add a guard block "+
					"at the top of the -- +goose Up section, e.g.:\n"+
					"    DO $$ BEGIN\n"+
					"      IF <undelivered rows exist>\n"+
					"         AND current_setting('gocell.allow_outbox_rebuild', true) IS DISTINCT FROM 'true' THEN\n"+
					"        RAISE EXCEPTION 'rebuild blocked: GUC gocell.allow_outbox_rebuild not set';\n"+
					"      END IF;\n"+
					"    END $$;\n"+
					"  Rule: MIGRATION-DESTRUCTIVE-UP-REBUILD-GUC-01",
				cc.Rel,
			)
		}
	})
}

// ---------------------------------------------------------------------------
// INVARIANT: SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01 (Medium AI-robust)
//
// Every table created by migration 017 or later must appear in the
// expectedColumns registry in adapters/postgres/schema_guard.go. This forces
// developers who add a new table to also add the corresponding shape checks,
// preventing silent drift between schema and verification.
//
// Scope: migrations 017+ (S3F owned tables). Pre-017 tables (outbox_entries,
// config_entries, config_versions, refresh_tokens, feature_flags) are not
// covered by VerifyExpectedShape's structural checks. Tables that are
// intentionally excluded from schema_guard coverage must be listed in
// archtestExcludedTables with a reason.
//
// AI-robust: Medium — regex scan of SQL + schema_guard.go source.
// ---------------------------------------------------------------------------

// archtestExcludedTables is the explicit allowlist of post-017 tables that
// are NOT required to appear in expectedColumns. Each entry must have a reason.
// This list must be updated when tables are added to expectedColumns to
// "lift the floor".
//
// All post-017 tables currently have expectedColumns coverage. This map is
// retained as a typed extension point for future tables that genuinely cannot
// be covered at the time of creation (e.g. a table introduced in a separate PR
// before its schema guard coverage ships). An empty map is the intended steady
// state.
var archtestExcludedTables = map[string]string{}

// createTableRE matches CREATE TABLE statements in migration SQL files.
var createTableRE = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)

// schemaGuardTableRE extracts Table field string literals from the
// expectedColumns slice in schema_guard.go. Matches lines like:
//
//	{Table: "users", ...}
var schemaGuardTableRE = regexp.MustCompile(`Table:\s*"(\w+)"`)

// TestArchtest_SchemaGuardCoversEveryOwnedTable asserts that every table
// introduced by migration 017+ has a corresponding entry in the
// expectedColumns registry in schema_guard.go (or is explicitly excluded).
//
// To confirm this test catches violations (TDD RED):
//  1. Comment out one table block (e.g., all "users" entries) in expectedColumns.
//  2. Run: go test ./tools/archtest/... -run TestArchtest_SchemaGuardCoversEveryOwnedTable
//  3. Test must FAIL. Restore the entries, test must PASS.
func TestArchtest_SchemaGuardCoversEveryOwnedTable(t *testing.T) {
	root := findModuleRoot(t)

	// Step 1: Collect all tables created by migrations >= 017.
	migratedTables := map[string]string{} // table name -> migration file

	// DirsScope takes paths relative to the module root.
	scope := scanner.DirsScope(root, []string{"adapters/postgres/migrations"})
	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, cc scanner.ContentContext) {
		// Only process migrations 017 and later.
		base := filepath.Base(cc.AbsPath)
		if !isAtLeastMigration017(base) {
			return
		}
		// Extract CREATE TABLE names from the Up section only.
		upSection := upSectionOf(string(cc.Bytes))
		matches := createTableRE.FindAllStringSubmatch(upSection, -1)
		for _, m := range matches {
			tableName := strings.ToLower(m[1])
			migratedTables[tableName] = cc.Rel
		}
	})

	if len(migratedTables) == 0 {
		t.Fatal("no tables found in migrations 017+; likely a walker or regex error")
	}

	// Step 2: Collect all tables referenced in expectedColumns in schema_guard.go.
	sgPath := filepath.Join(root, "adapters", "postgres", "schema_guard.go")
	// #nosec G304 -- reading repo-resident file under module root
	sgContent, err := os.ReadFile(filepath.Clean(sgPath))
	if err != nil {
		t.Fatalf("cannot read schema_guard.go: %v", err)
	}

	guardedTables := map[string]struct{}{}
	for _, m := range schemaGuardTableRE.FindAllStringSubmatch(string(sgContent), -1) {
		guardedTables[strings.ToLower(m[1])] = struct{}{}
	}

	// Step 3: Assert every migrated table is either guarded or explicitly excluded.
	for tableName, migFile := range migratedTables {
		if reason, excluded := archtestExcludedTables[tableName]; excluded {
			t.Logf("SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01: table %q excluded from coverage check (reason: %s)",
				tableName, reason)
			continue
		}
		if _, ok := guardedTables[tableName]; !ok {
			assert.Fail(t,
				"migration 017+ table missing from schema_guard expectedColumns",
				"table %q (introduced in %s) has no entry in adapters/postgres/schema_guard.go expectedColumns.\n"+
					"Either add the table's column shapes to expectedColumns, or add it to archtestExcludedTables "+
					"in tools/archtest/pg_schema_guard_invariants_test.go with a reason.\n"+
					"Rule: SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01",
				tableName, migFile,
			)
		}
	}

	// Step 4: Warn about tables in schema_guard that don't correspond to any migration.
	// This catches stale entries (e.g. table was renamed/dropped).
	for guardedTable := range guardedTables {
		if _, inMigrations := migratedTables[guardedTable]; !inMigrations {
			// Pre-017 tables (outbox_entries etc.) appear in expectedColumns if
			// we broaden coverage in the future — that's fine. But currently, only
			// the 4 S3F tables are registered. This branch fires if a table is in
			// expectedColumns but has no corresponding CREATE TABLE in migrations 017+.
			// Log (not fail) to avoid false positives during gradual coverage expansion.
			t.Logf("SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01: table %q is in schema_guard expectedColumns but not in migrations 017+; "+
				"verify it is a pre-017 table or update the registry", guardedTable)
		}
	}
}

// isAtLeastMigration017 reports whether a migration filename's numeric prefix
// is >= 17 (e.g. "017_users.sql" → true, "016_refresh_tokens_idle_grace.sql" → false).
func isAtLeastMigration017(filename string) bool {
	migVersionRe := regexp.MustCompile(`^(\d+)_`)
	m := migVersionRe.FindStringSubmatch(filename)
	if m == nil {
		return false
	}
	// Trim leading zeros for comparison.
	numStr := strings.TrimLeft(m[1], "0")
	if numStr == "" {
		numStr = "0"
	}
	var n int
	for _, ch := range numStr {
		if ch < '0' || ch > '9' {
			return false
		}
		n = n*10 + int(ch-'0')
	}
	return n >= 17
}

// upSectionOf returns the Up section of a migration file (from the start
// of the file to the -- +goose Down marker, or the whole file if no Down).
func upSectionOf(content string) string {
	if idx := strings.Index(content, gooseDownMarker); idx >= 0 {
		return content[:idx]
	}
	return content
}
