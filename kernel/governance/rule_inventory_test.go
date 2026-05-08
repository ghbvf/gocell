package governance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
)

// TestArchtestInventoryNoIDTruncation guards against a regex defect in
// scripts/audit/list-archtests.sh that truncated multi-segment governance
// rule IDs in docs/audit/archtest-inventory.md.
//
// History: the original alternation used `\b...|CONSISTENCY|...-[A-Z0-9-]+`
// which matched `\bCONSISTENCY-EMIT-01` mid-token inside
// CONTRACT-CONSISTENCY-EMIT-01, producing CONSISTENCY-EMIT-01 in the
// inventory output. Fix in PR-FUNNEL-03 reordered alternation so longer
// compound prefixes (CONTRACT-CONSISTENCY-EMIT / SLICE-CONSISTENCY /
// DOC-NAME) come before their shorter substrings.
//
// This test asserts that every governance rule ID with a compound prefix
// (one or more internal hyphens before the canonical -NN suffix) appears
// verbatim in the inventory file. New compound-prefix rules MUST be added
// here when introduced.
func TestArchtestInventoryNoIDTruncation(t *testing.T) {
	t.Parallel()

	atRisk := []string{
		"CONTRACT-CONSISTENCY-EMIT-01", // truncated to CONSISTENCY-EMIT-01 pre-fix
		"SLICE-CONSISTENCY-01",
		"DOC-NAME-01",
		"WRAPPER-CONTRACTSPEC-IMPORT-01", // archtest cross-ref kept verbatim
		"WRAPPER-NO-PACKAGE-STATE",
		"FMT-CONTRACT-DIR-ID-MATCH-01",
	}

	inventoryPath := filepath.Join("..", "..", "docs", "audit", "archtest-inventory.md")
	data := fileutil.MustReadFile(t, inventoryPath)
	body := string(data)

	for _, id := range atRisk {
		if !strings.Contains(body, id) {
			t.Errorf("inventory missing full rule ID %q — likely truncated by "+
				"scripts/audit/list-archtests.sh regex; check alternation "+
				"orders longer prefixes first.", id)
		}
	}
}

// TestRuleInventoryGolden is the migration equivalence guard for PR-FUNNEL-03
// (governance rules consolidation). It pins the full set of rule IDs declared
// as string literals across kernel/governance/*.go (non-test) so that any
// add / rename / delete of a rule must be paired with an explicit golden
// update.
//
// Scope rationale: the inventory covers EVERY rule ID emitted by the
// governance package, not just rules_*.go. Files outside the consolidation
// scope (contracthealth.go for CH-01..03, depcheck.go for DEP-01..03) are
// included so the equivalence check stays precise across the package and
// drift from any quarter is caught.
//
// ref: kubernetes/apimachinery/pkg/util/validation/field/errors_test.go
// (golden error-code allowlist).
func TestRuleInventoryGolden(t *testing.T) {
	t.Parallel()

	golden := goldenRuleIDs()
	actual := scanRuleIDs(t, ".")

	if diff := symmetricDiff(golden, actual); len(diff) > 0 {
		t.Fatalf("rule inventory drift detected — golden vs actual differ.\n"+
			"To fix: add the new ID to goldenRuleIDs() OR remove the stray "+
			"literal.\nDiff (- only in golden, + only in actual):\n%s",
			strings.Join(diff, "\n"))
	}
}

// ruleIDPattern matches rule-ID literals: PREFIX-SUFFIX where PREFIX is one of
// the registered governance series (and may itself contain '-', e.g.
// CONTRACT-CONSISTENCY-EMIT) and SUFFIX is alphanumeric.
var ruleIDPattern = regexp.MustCompile(
	`^(ADV|CH|CONTRACT-CONSISTENCY-EMIT|DEP|DOC-NAME|FMT|OUTGUARD|REF|SLICE-CONSISTENCY|TOPO|VERIFY)-[A-Z0-9]+$`,
)

// scanRuleIDs walks dir for non-test .go files, parses each, and returns the
// sorted unique set of rule-ID string literals (matched by ruleIDPattern).
// Comments are excluded because go/parser exposes them separately from
// *ast.BasicLit nodes.
func scanRuleIDs(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read governance dir: %v", err)
	}

	seen := map[string]struct{}{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if ruleIDPattern.MatchString(s) {
				seen[s] = struct{}{}
			}
			return true
		})
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// goldenRuleIDs returns the pinned set of all rule IDs declared in
// kernel/governance/*.go. Update this list whenever a rule is added /
// renamed / removed.
//
// Total: 81 IDs across 11 series.
func goldenRuleIDs() []string {
	return []string{
		// ADV — advisory warnings (rules_misc_advisory.go)
		"ADV-01", "ADV-03", "ADV-04", "ADV-05", "ADV-06",

		// CH — contract-health (contracthealth.go + rules_http.go)
		"CH-01", "CH-02", "CH-03", "CH-04", "CH-05", "CH-06",

		// CONTRACT-CONSISTENCY-EMIT — http trigger ↔ outbox emit alignment
		// (rules_misc_consistency.go)
		"CONTRACT-CONSISTENCY-EMIT-01",

		// DEP — dependency graph (depcheck.go)
		"DEP-01", "DEP-02", "DEP-03",

		// DOC-NAME — document literal scanning (rules_misc_advisory.go;
		// strict-mode orchestrator is in rules_misc_strict.go)
		"DOC-NAME-01",

		// FMT — format / structural (rules_fmt.go for FMT-01..15, 24, 26..30
		// + strict-mode FMT-16/17/19/A1/C1 + FMT-20..23/25 in
		// rules_misc_strict.go; FMT-19 implementation in rules_misc_advisory.go).
		"FMT-01", "FMT-02", "FMT-03", "FMT-04", "FMT-05",
		"FMT-06", "FMT-07", "FMT-08", "FMT-09", "FMT-10",
		"FMT-11", "FMT-12", "FMT-13", "FMT-14", "FMT-15",
		"FMT-16", "FMT-17", "FMT-19",
		"FMT-20", "FMT-21", "FMT-22", "FMT-23", "FMT-24", "FMT-25",
		"FMT-26", "FMT-27", "FMT-28", "FMT-29", "FMT-30",
		"FMT-A1", "FMT-C1",

		// OUTGUARD — outbox durability (rules_misc_advisory.go)
		"OUTGUARD-01",

		// REF — reference integrity (rules_ref.go for REF-01..11, 13..17;
		// REF-12 was relocated to rules_fmt.go in PR-FUNNEL-03 because it is
		// I/O-flavored — pairs with FMT cluster's disk-format rules).
		"REF-01", "REF-02", "REF-03", "REF-04", "REF-05",
		"REF-06", "REF-07", "REF-08", "REF-09", "REF-10",
		"REF-11", "REF-12", "REF-13", "REF-14", "REF-15",
		"REF-16", "REF-17",

		// SLICE-CONSISTENCY — slice level vs parent cell (rules_misc_advisory.go)
		"SLICE-CONSISTENCY-01",

		// TOPO — topology (rules_topo.go)
		"TOPO-01", "TOPO-02", "TOPO-03", "TOPO-04", "TOPO-05",
		"TOPO-06", "TOPO-07", "TOPO-08", "TOPO-09",

		// VERIFY — verification closure (rules_verify.go)
		"VERIFY-01", "VERIFY-02", "VERIFY-03",
		"VERIFY-04", "VERIFY-05", "VERIFY-06",
	}
}

// symmetricDiff returns ordered "- a" / "+ b" lines for items present in only
// one side. Inputs must be sorted.
func symmetricDiff(want, got []string) []string {
	wantSet := map[string]struct{}{}
	for _, s := range want {
		wantSet[s] = struct{}{}
	}
	gotSet := map[string]struct{}{}
	for _, s := range got {
		gotSet[s] = struct{}{}
	}
	var diff []string
	for _, s := range want {
		if _, ok := gotSet[s]; !ok {
			diff = append(diff, "- "+s)
		}
	}
	for _, s := range got {
		if _, ok := wantSet[s]; !ok {
			diff = append(diff, "+ "+s)
		}
	}
	return diff
}
