// Package archtest_test — pg_schema_guard_invariants_test.go
//
// File invariants:
//   - INVARIANT: MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01
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
// INVARIANT: MIGRATION-DESTRUCTIVE-DOWN-GUC-GUARD-01 (Medium AI-rebust)
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
// AI-rebust: Medium — string pattern match on SQL content caught at test time.
// ---------------------------------------------------------------------------

// destructiveOps are the DDL tokens that classify a Down section as destructive.
var destructiveOps = []string{
	"DROP TABLE",
	"DROP COLUMN",
	"DROP CONSTRAINT",
	"DROP INDEX",
	"DROP TRIGGER",
	"DROP FUNCTION",
	"TRUNCATE",
	"DELETE FROM",
}

// gucGuardRE matches the required GUC check pattern in the Down section.
var gucGuardRE = regexp.MustCompile(`current_setting\s*\(\s*'gocell\.allow_destructive_down'`)

// gooseDownMarker marks the start of the Down section in a migration file.
const gooseDownMarker = "-- +goose Down"

// gucGuardGrandfatheredMigrations is the allowlist of migration filenames that
// predated the GUC fail-closed pattern and have not yet been retrofitted.
// These migrations existed before S3F and are grandfathered.
//
// New migrations (022+) must always include the GUC guard if their Down section
// is destructive. Migrations in this list SHOULD be retrofitted in a follow-up
// PR to close the pre-existing gap — this allowlist is a temporary carve-out,
// not a permanent exemption.
var gucGuardGrandfatheredMigrations = map[string]string{ //nolint:gosec // G101: false positive — migration filenames, not credentials
	"001_create_outbox_entries.sql":               "pre-S3F; retrofit guard in follow-up",
	"002_add_topic_column.sql":                    "pre-S3F; retrofit guard in follow-up",
	"003_outbox_status_columns.sql":               "pre-S3F; retrofit guard in follow-up",
	"004_create_config_entries_and_versions.sql":  "pre-S3F; retrofit guard in follow-up",
	"005_recreate_outbox_pending_concurrent.sql":  "pre-S3F; retrofit guard in follow-up",
	"006_add_config_versions_config_id_index.sql": "pre-S3F; retrofit guard in follow-up",
	"008_create_feature_flags.sql":                "pre-S3F; retrofit guard in follow-up",
	"009_create_feature_flags_index.sql":          "pre-S3F; retrofit guard in follow-up",
	"011_refresh_tokens_token_index.sql":          "pre-S3F; retrofit guard in follow-up",
	"012_refresh_tokens_rebuild.sql":              "pre-S3F; retrofit guard in follow-up",
	"013_add_outbox_observability_column.sql":     "pre-S3F; retrofit guard in follow-up",
	"014_add_outbox_lease_id.sql":                 "pre-S3F; retrofit guard in follow-up",
	"015_add_outbox_claiming_lease_check.sql":     "pre-S3F; retrofit guard in follow-up",
	"016_refresh_tokens_idle_grace.sql":           "pre-S3F; retrofit guard in follow-up",
	"020_audit_ledger.sql":                        "audit teardown not expected in production; retrofit guard in follow-up",
	"021_audit_entries_event_id_unique.sql":       "DROP INDEX only (non-destructive data-wise); retrofit guard in follow-up",
}

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
// INVARIANT: SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01 (Medium AI-rebust)
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
// AI-rebust: Medium — regex scan of SQL + schema_guard.go source.
// ---------------------------------------------------------------------------

// archtestExcludedTables is the explicit allowlist of post-017 tables that
// are NOT required to appear in expectedColumns. Each entry must have a reason.
// This list must be updated when tables are added to expectedColumns to
// "lift the floor".
//
// Current exclusions:
//   - audit_entries (020): owned by auditcore S7; schema guard coverage is a
//     separate work item; excluded until audit schema hardening PR ships.
var archtestExcludedTables = map[string]string{
	"audit_entries": "auditcore S7 hardening not yet in scope; add to expectedColumns when audit schema guard ships",
}

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
