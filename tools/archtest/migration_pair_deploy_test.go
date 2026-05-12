// INVARIANT: MIGRATION-PAIR-DEPLOY-01
package archtest

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// MIGRATION-PAIR-DEPLOY-01: a migration that declares
//
//	-- pair-deploy: <stem>
//
// in its header expresses a deploy-time atomicity dependency on the named
// partner migration: the two MUST go out in the same release because
// splitting them opens a correctness window. The rule guards anchor
// validity:
//
//  1. At most one `-- pair-deploy:` directive per file (multiple = ambiguous
//     intent, fail-closed).
//  2. The named partner stem must exist as `<stem>.sql` in the same
//     migrations directory.
//
// The rule does NOT enforce reciprocity — pair-deploy is intentionally
// single-direction. The migration that has the later dependency declares
// the anchor; the earlier migration is unchanged. Reciprocal anchors would
// require N×2 edits with no additional safety: removing one side already
// fails the existence check, and the deploy semantics are documentation +
// release manifest, not statically checkable from migration sources alone.
//
// Concrete live case: `021_audit_entries_event_id_unique` adds a UNIQUE
// INDEX on `audit_entries(namespace, event_id)`. The table is created in
// `020_audit_ledger`. Deploying 020 without 021 opens a window where
// concurrent INSERTs can race past the application-layer fingerprint check
// before either commits. A canary hard-assert at the bottom of this test
// enforces that 021 carries the live anchor — without the canary, deleting
// both sides of the anchor would silently degrade the rule to a no-op.
//
// AI-rebust 评级：Medium — anchor is a structured directive matched by a
// strict regex (not free-form prose). Bypass cost is "edit two real files
// in the same PR" (the migration plus removing the canary). Hard would
// require a release-manifest source-of-truth that this repo does not have.
//
// ref: docs/plans/202605112000-036-archtest-governance-rollout-plan.md §3.9 K-05
// ref: adapters/postgres/migrations/021_audit_entries_event_id_unique.sql header
func TestMigrationPairDeploy01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	const migrationsDir = "adapters/postgres/migrations"
	scope := scanner.DirsScope(root, []string{migrationsDir},
		scanner.MatchRels(func(rel string) bool {
			return filepath.ToSlash(filepath.Dir(rel)) == migrationsDir
		}),
	)

	anchorRe := regexp.MustCompile(`(?m)^\s*--\s*pair-deploy:\s*(\S+)\s*$`)

	type anchor struct {
		partner string
		line    int
	}
	anchors := map[string]anchor{}
	files := map[string]bool{}

	scanner.EachContentFile(t, scope, []string{".sql"}, func(t *testing.T, fc scanner.ContentContext) {
		stem := strings.TrimSuffix(filepath.Base(fc.Rel), ".sql")
		files[stem] = true

		matches := anchorRe.FindAllSubmatchIndex(fc.Bytes, -1)
		if len(matches) == 0 {
			return
		}
		if len(matches) > 1 {
			t.Errorf("MIGRATION-PAIR-DEPLOY-01: %s declares %d `-- pair-deploy:` directives; at most one allowed",
				fc.Rel, len(matches))
			return
		}
		m := matches[0]
		line := 1 + strings.Count(string(fc.Bytes[:m[0]]), "\n")
		partner := string(fc.Bytes[m[2]:m[3]])
		anchors[stem] = anchor{partner: partner, line: line}
	})

	for stem, a := range anchors {
		if !files[a.partner] {
			t.Errorf("MIGRATION-PAIR-DEPLOY-01: %s.sql:%d declares `pair-deploy: %s`, but %s.sql does not exist in %s/",
				stem, a.line, a.partner, a.partner, migrationsDir)
			continue
		}
		if a.partner == stem {
			t.Errorf("MIGRATION-PAIR-DEPLOY-01: %s.sql:%d declares pair-deploy to itself; partner must be a different migration",
				stem, a.line)
		}
	}

	// Canary: the live 020/021 pair MUST be wired up. Removing the anchor
	// without removing the deploy dependency is the silent-drift surface
	// this canary protects against.
	const livePartnerStem = "020_audit_ledger"
	const liveAnchorStem = "021_audit_entries_event_id_unique"
	live, ok := anchors[liveAnchorStem]
	if assert.True(t, ok,
		"MIGRATION-PAIR-DEPLOY-01 canary: %s.sql must declare `-- pair-deploy: %s` in its header — "+
			"the two migrations together create audit_entries + the UNIQUE INDEX guarding application-layer "+
			"idempotency; deploying them in separate releases opens a concurrent-INSERT race window. "+
			"If this canary trips, restore the anchor or update the canary with the new partner stem.",
		liveAnchorStem, livePartnerStem) {
		assert.Equal(t, livePartnerStem, live.partner,
			"MIGRATION-PAIR-DEPLOY-01 canary: %s.sql declares partner=%s but must declare %s",
			liveAnchorStem, live.partner, livePartnerStem)
	}
}
