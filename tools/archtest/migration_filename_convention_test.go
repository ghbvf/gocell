//go:build archtest

// INVARIANT: MIGRATION-FILENAME-GOOSE-PARSEABLE-01
package archtest

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// migrationFilenameRe is the goose-native migration filename convention enforced
// by MIGRATION-FILENAME-GOOSE-PARSEABLE-01: a zero-padded 3-digit version, an
// underscore, then a lowercase snake_case descriptor, then ".sql".
//
// It is intentionally STRICTER than goose's own parser (goose.NumericComponent
// does strconv.ParseInt(strings.Cut(base,"_")[0])): any name matching this regex
// is trivially goose-parseable, and — the load-bearing point for #1089 — a
// namespace-PREFIXED name like "platform_001_x.sql" FAILS this regex (starts
// with a letter, not a digit), exactly as it would fail goose's ParseInt. Under
// the namespace model the namespace lives in the goose TRACKING TABLE
// (schema_migrations_<namespace>), never in the filename; embedding it in the
// filename would silently break version parsing.
var migrationFilenameRe = regexp.MustCompile(`^[0-9]{3}_[a-z][a-z0-9_]*\.sql$`)

// MIGRATION-FILENAME-GOOSE-PARSEABLE-01: every migration file under
// adapters/postgres/migrations/ must be goose-native NNN_desc.sql (no namespace
// prefix). The platform's migrations are namespace "platform"; their tracking
// table is schema_migrations_platform, but the FILES stay goose-parseable so
// goose's NumericComponent can derive the version. An external Cell module ships
// its own migrations/ with the same convention (its own 001..N), registered via
// composition.WithMigrations(namespace, fs) — the namespace partitions the
// tracking table, not the filename.
//
// SCOPE: this rule scans ONLY adapters/postgres/migrations/ (the platform set).
// External Cell modules in their own repos are NOT covered here — they obtain
// the equivalent guard by enrolling this rule into the M3 archtest library
// (RunStandardCellRules, #1302); until then external repos rely on the
// documented convention (a non-NNN_ .sql is silently skipped by goose, never
// applied). The quickstart "Migrations" section states this explicitly.
//
// AI-robust 评级：Medium (permanent ceiling) — a filename is an on-disk artifact
// the Go type system cannot express. This is a value-level scan of every real
// .sql migration (no comment-anchor / name-convention escape; the regex mirrors
// goose's own parse contract as the single source). A "Hard via codegen" form is
// structurally unreachable for hand-authored SQL filenames — the same Go ceiling
// as the locator path funnel (gh #1235 won't-do) and #851. The downstream API
// that consumes namespaces IS Hard: composition.WithMigrations /
// adapters/postgres.NewMigrator take a typed migration.Namespace, so a bare
// table string is a compile error (see MIGRATION-TRACKING-TABLE-DERIVED-01 for
// the in-package backstop). Blind spots + negative control: TestMigrationFilenameConvention01_NegativeControl.
//
// ref: github.com/pressly/goose/v3 migration.go NumericComponent — strings.Cut + ParseInt.
// ref: pkg/migration.Namespace / adapters/postgres.trackingTableFor — namespace → table.
func TestMigrationFilenameConvention01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	const migrationsDir = "adapters/postgres/migrations"
	scope := scanner.DirsScope(
		root, []string{migrationsDir},
		scanner.MatchRels(func(rel string) bool {
			return filepath.ToSlash(filepath.Dir(rel)) == migrationsDir
		}),
	)

	seen := 0
	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, fc scanner.ContentContext) {
		seen++
		name := filepath.Base(fc.Rel)
		if !migrationFilenameRe.MatchString(name) {
			t.Errorf("MIGRATION-FILENAME-GOOSE-PARSEABLE-01: %s does not match the goose-native "+
				"convention %s (zero-padded 3-digit version + '_' + lowercase snake_case + '.sql'). "+
				"Namespaces live in the tracking table schema_migrations_<namespace>, NOT in the filename — "+
				"a name like 'platform_001_x.sql' breaks goose's NumericComponent version parser.",
				fc.Rel, migrationFilenameRe.String())
		}
	})

	// Anti-vacuity: the platform migration set is non-empty, so the rule actually
	// scanned files. A directory that silently scanned zero .sql would make this
	// invariant a no-op.
	if seen == 0 {
		t.Fatalf("MIGRATION-FILENAME-GOOSE-PARSEABLE-01: scanned 0 .sql files under %s/ — "+
			"the rule must verify at least the platform migration set", migrationsDir)
	}
}

// TestMigrationFilenameConvention01_NegativeControl proves the rule is not
// vacuous: the convention regex must REJECT the forms the invariant exists to
// catch — chiefly a namespace-prefixed filename (which would break goose) — and
// ACCEPT the canonical goose-native form.
func TestMigrationFilenameConvention01_NegativeControl(t *testing.T) {
	t.Parallel()
	reject := []string{
		"platform_001_init.sql", // namespace prefix → breaks goose NumericComponent (the #1089 footgun)
		"payment_002_charges.sql",
		"1_foo.sql",      // not zero-padded 3-digit
		"0001_foo.sql",   // 4-digit
		"001-foo.sql",    // dash separator
		"001_Foo.sql",    // uppercase descriptor
		"001_foo.up.sql", // dotted (still ends .sql but contains '.')
		"001_foo.txt",    // wrong extension
		"abc_foo.sql",    // non-numeric version
	}
	for _, name := range reject {
		if migrationFilenameRe.MatchString(name) {
			t.Errorf("negative control: %q must be REJECTED by the convention regex but matched", name)
		}
	}
	accept := []string{
		"001_create_outbox_entries.sql",
		"050_accesscore_tenant_id.sql",
		"999_z9.sql",
	}
	for _, name := range accept {
		if !migrationFilenameRe.MatchString(name) {
			t.Errorf("negative control: %q must be ACCEPTED by the convention regex but did not match", name)
		}
	}
}
