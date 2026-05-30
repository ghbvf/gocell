// Package archtest_test — pg_schema_guard_invariants_test.go
//
// File invariants:
//   - INVARIANT: MIGRATION-FORWARD-REBUILD-ANNOTATION-01
//   - INVARIANT: MIGRATION-NO-GUC-RESIDUE-01
//   - INVARIANT: SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01

package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	postgres "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// gooseDownMarker marks the start of the Down section in a migration file.
const gooseDownMarker = "-- +goose Down"

// gooseDownMarkerRE line-anchors gooseDownMarker (derived from the same const as
// its single source) so a literal "-- +goose Down" inside Up-section prose does
// not split the section. Mirrors the runtime gate's gooseDownMarkerRE in
// adapters/postgres/migrator.go (C2 fail-open fix — keep the two in step).
var gooseDownMarkerRE = regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(gooseDownMarker) + `\s*$`)

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

// upSectionOf returns the Up section of a migration file (from the start
// of the file to the -- +goose Down marker, or the whole file if no Down).
func upSectionOf(content string) string {
	if loc := gooseDownMarkerRE.FindStringIndex(content); loc != nil {
		return content[:loc[0]]
	}
	return content
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

// ---------------------------------------------------------------------------
// INVARIANT: MIGRATION-FORWARD-REBUILD-ANNOTATION-01 (Medium AI-robust)
//
// Every migration Up section that contains a whole-table destructive operation
// (TRUNCATE or DROP TABLE, after stripping line comments) MUST carry exactly
// one annotation of the form:
//
//	-- +gocell forward-rebuild target=<table_name>
//
// in the original (un-stripped) Up text. The annotation acts as the
// machine-readable contract between the SQL file and the Go phase0 permit
// gate: phase0 parses pending migrations, extracts forward-rebuild targets,
// and fails-closed if no matching ForwardRebuildPermit is provided by the
// caller. This archtest is the regression guard that the annotation is present
// and syntactically valid (target is a legal identifier) — it does NOT test
// the runtime gate itself.
//
// AI-robust: Medium — SQL text regex, caught at test time. The runtime gate
// (Go phase0 permit check) is the Hard backstop; this archtest ensures the
// annotation is always present so phase0 has something to act on.
//
// Blind spot: destructive token inside a quoted string literal is not
// stripped (no such case in the corpus today) — documented, not enforced.
// A TRUNCATE / DROP TABLE hidden in a block comment (/* ... */) is also not
// stripped; stripSQLLineComments only removes -- line comments.
//
// TDD RED confirmation: run
//
//	go test ./tools/archtest/... -run TestArchtest_MigrationForwardRebuildAnnotation
//
// Before the SQL files are updated (012/043/044 still carry GUC guards, no
// +gocell forward-rebuild annotation), the test must FAIL because:
//   - 012_refresh_tokens_rebuild.sql has TRUNCATE with no annotation
//   - 043_audit_entries_v2.sql has TRUNCATE with no annotation
//   - 044_outbox_entries_principal.sql has TRUNCATE with no annotation
//
// ---------------------------------------------------------------------------

// forwardRebuildAnnotationRE matches the required +gocell annotation in the
// raw (un-stripped) Up section. The target must be a valid SQL identifier.
// It uses postgres.ForwardRebuildAnnotationPattern as the single source of truth
// so the archtest regex stays in sync with the runtime gate in migrator.go.
var forwardRebuildAnnotationRE = regexp.MustCompile(postgres.ForwardRebuildAnnotationPattern)

// TestArchtest_MigrationForwardRebuildAnnotation asserts that every migration
// whose Up section contains TRUNCATE or DROP TABLE (after stripping line
// comments) carries exactly one -- +gocell forward-rebuild target=<table>
// annotation in the raw Up text.
//
// Rule: MIGRATION-FORWARD-REBUILD-ANNOTATION-01.
func TestArchtest_MigrationForwardRebuildAnnotation(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"adapters/postgres/migrations"})
	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, cc scanner.ContentContext) {
		content := string(cc.Bytes)
		rawUp := upSectionOf(content)

		// Detect whole-table destructive ops in the stripped Up section only.
		strippedUp := stripSQLLineComments(rawUp)
		upperStripped := strings.ToUpper(strippedUp)
		isWholeTableDestructive := strings.Contains(upperStripped, "TRUNCATE") ||
			strings.Contains(upperStripped, "DROP TABLE")
		if !isWholeTableDestructive {
			return
		}

		// Check raw (un-stripped) Up section for the annotation.
		matches := forwardRebuildAnnotationRE.FindAllStringSubmatch(rawUp, -1)
		if len(matches) == 0 {
			assert.Fail(t,
				"forward (Up) section destroys a whole table but lacks the +gocell forward-rebuild annotation",
				"file: %s\n"+
					"  Up section contains TRUNCATE / DROP TABLE but no "+
					"'-- +gocell forward-rebuild target=<table>' annotation.\n"+
					"  Add exactly one annotation at the top of the -- +goose Up section, e.g.:\n"+
					"    -- +gocell forward-rebuild target=audit_entries\n"+
					"  The Go phase0 gate (ForwardRebuildPermit) uses this annotation to fail-closed\n"+
					"  when no matching permit is provided by the caller.\n"+
					"  Rule: MIGRATION-FORWARD-REBUILD-ANNOTATION-01",
				cc.Rel,
			)
			return
		}
		if len(matches) > 1 {
			assert.Fail(t,
				"forward (Up) section has more than one +gocell forward-rebuild annotation",
				"file: %s has %d annotations; expected exactly one per migration.\n"+
					"Rule: MIGRATION-FORWARD-REBUILD-ANNOTATION-01",
				cc.Rel, len(matches),
			)
		}
	})
}

// blockCommentRE / sqlStringLitRE extract the two SQL lexical regions that
// stripSQLLineComments does NOT strip — the documented blind spots of
// MIGRATION-FORWARD-REBUILD-ANNOTATION-01 / MIGRATION-NO-GUC-RESIDUE-01.
var (
	blockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	sqlStringLitRE = regexp.MustCompile(`'[^']*'`)
)

// TestArchtest_MigrationDestructiveToken_NoCommentOrStringBlindSpot is the
// reverse self-check (ai-robust charter: every blind spot needs a reverse test)
// for the documented blind spots of MIGRATION-FORWARD-REBUILD-ANNOTATION-01 and
// MIGRATION-NO-GUC-RESIDUE-01. stripSQLLineComments only removes "-- " line
// comments, so a TRUNCATE / DROP TABLE token hidden inside a /* block comment */
// or a '...' string literal would evade the destructive-op detection (and thus
// the annotation requirement). This test asserts the migration corpus contains
// no such token in those two regions — making the blind spots vacuous in
// practice and supplying the Medium rating its required reverse evidence. If a
// future migration introduces such a form, this fails and the detector must be
// upgraded to a real SQL lexer.
func TestArchtest_MigrationDestructiveToken_NoCommentOrStringBlindSpot(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"adapters/postgres/migrations"})
	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, cc scanner.ContentContext) {
		// Strip -- line comments first: their prose (e.g. "table's identity")
		// contains apostrophes that would otherwise mis-pair the simple string
		// regex across statements and swallow real DDL. After stripping, only
		// genuine SQL string literals and /* */ block comments remain — the
		// actual blind-spot regions stripSQLLineComments does not cover.
		content := stripSQLLineComments(string(cc.Bytes))
		regions := append(
			blockCommentRE.FindAllString(content, -1),
			sqlStringLitRE.FindAllString(content, -1)...,
		)
		for _, r := range regions {
			up := strings.ToUpper(r)
			if strings.Contains(up, "TRUNCATE") || strings.Contains(up, "DROP TABLE") {
				assert.Fail(t,
					"destructive token hidden in a block comment or string literal (blind spot)",
					"file: %s\n  region: %q\n  TRUNCATE / DROP TABLE inside /* */ or '...' evades "+
						"stripSQLLineComments, so MIGRATION-FORWARD-REBUILD-ANNOTATION-01 would miss it.\n"+
						"  Upgrade the detector to a SQL lexer.",
					cc.Rel, r)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// INVARIANT: MIGRATION-NO-GUC-RESIDUE-01 (Medium AI-robust)
//
// No migration SQL file (Up or Down section, including comments) may contain
// the substring "gocell.allow_" (case-insensitive). The four GUC variables
// that previously guarded destructive operations:
//
//	gocell.allow_destructive_down
//	gocell.allow_audit_rebuild
//	gocell.allow_outbox_rebuild
//	gocell.allow_destructive_refresh_tokens_rebuild
//
// have been replaced by the typed Go ForwardRebuildPermit / DestructiveDownPermit
// channel. Any residual reference — whether in current_setting(), set_config(),
// or a comment — is a stale artifact that should be removed.
//
// AI-robust: Medium — substring scan on SQL text, caught at test time.
//
// TDD RED confirmation: run
//
//	go test ./tools/archtest/... -run TestArchtest_MigrationNoGUCResidue
//
// Before the SQL files are updated, the test must FAIL because multiple
// migration files still contain "gocell.allow_" strings (001, 002, 003, 007,
// 008, 012, 014, 016, 043, 044, etc.).
//
// ---------------------------------------------------------------------------

// TestArchtest_MigrationNoGUCResidue asserts that no migration SQL file
// contains "gocell.allow_" (case-insensitive), confirming the four legacy GUC
// variables have been fully removed and replaced by the typed permit channel.
//
// Rule: MIGRATION-NO-GUC-RESIDUE-01.
func TestArchtest_MigrationNoGUCResidue(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"adapters/postgres/migrations"})
	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, cc scanner.ContentContext) {
		content := string(cc.Bytes)
		if strings.Contains(strings.ToLower(content), "gocell.allow_") {
			assert.Fail(t,
				"migration SQL file contains residual GUC reference",
				"file: %s\n"+
					"  Contains 'gocell.allow_' which is a reference to the legacy GUC variables\n"+
					"  (gocell.allow_destructive_down / gocell.allow_audit_rebuild /\n"+
					"   gocell.allow_outbox_rebuild / gocell.allow_destructive_refresh_tokens_rebuild).\n"+
					"  These GUC variables have been replaced by the typed Go permit channel\n"+
					"  (ForwardRebuildPermit / DestructiveDownPermit). Remove all references,\n"+
					"  including current_setting() calls, set_config() calls, and comments.\n"+
					"  Rule: MIGRATION-NO-GUC-RESIDUE-01",
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
