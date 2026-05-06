// Command slowgate enforces a wall-clock budget on individual Go tests by
// reading `go test -json` events from stdin. Tests whose Elapsed exceeds
// --threshold are reported on stderr and the process exits 1, unless the
// (Package, TestName) pair appears in the allowlist file referenced by
// --allowlist.
//
// Usage:
//
//	go test -json ./... | tee /tmp/test.json |
//	  slowgate --threshold=2s --allowlist=tools/slowgate/allowlist.txt
//
// The companion `tee` is the recommended pipeline form — it preserves the
// raw event stream for log archival; slowgate itself emits no stdout, only
// stderr reasons plus exit code.
//
// Allowlist file format (line-based, grep-friendly, no external parser):
//
//	# comments after `#` are ignored; blank lines are ignored
//	github.com/example/pkg<TAB>TestName       # tab-separated (preferred)
//	github.com/example/pkg TestName            # any whitespace also accepted
//
// Each data line must produce exactly two fields. The (Package, TestName)
// pair is matched verbatim against the `Package` and `Test` fields of the
// `go test -json` event. Subtests (Test names containing `/`) are skipped
// because the parent root-test event already aggregates the subtest's
// wall-clock into its own Elapsed; counting subtests independently would
// double-count the same time spent.
//
// Companion gates (independent, no shared invariant):
//   - TEST-SLEEP-DISCIPLINE-01 (tools/archtest) — every time.Sleep in test
//     code carries a //archtest:allow:test-sleep <reason> annotation. Targets
//     sleep paper-trail; not coupled to slowgate.
//   - SLOWGATE-ALLOWLIST-01 (tools/archtest) — every entry in the slowgate
//     allowlist (a) maps to a real top-level `func TestXxx`, AND (b) is
//     preceded by a `# <reason>` comment line in allowlist.txt. Decoupled
//     from sleep annotations: most >2s tests in this codebase are slow due
//     to packages.Load / subprocess go-toolchain / fixture walks, not sleep.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G9
// ref: cmd/test2json — TestEvent schema (Action/Package/Test/Elapsed)
// ref: github.com/gotestyourself/gotestsum cmd/tool/slowest/slowest.go
// ref: github.com/ghbvf/gocell/tools/e2egate/parser.go (stdin pipe pattern)
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	// defaultAllowlistPath is the canonical project-relative path used in
	// the actionable stderr footer when the caller does not pass --allowlist
	// (e.g. ad-hoc invocations). When --allowlist is set, the actual flag
	// value is shown instead so the message stays accurate.
	defaultAllowlistPath = "tools/slowgate/allowlist.txt"

	// defaultThreshold is the default per-test wall-clock budget. 5s is the
	// empirical floor needed to absorb GHA ubuntu-latest runner variance
	// across this codebase: the dominant >2s test population is
	// packages.Load-bound (archtest / typeseval / metricschema /
	// generatedverify) and subprocess-go-toolchain (kernel/verify), where
	// individual runs swing 2–7s from cold cache + concurrent CPU. 2s
	// produces a long tail of allowlist churn that is pure noise; 5s lets
	// the gate retain its real signal — a sleep regression from 100ms to
	// 6s is still caught, while CI flakes from 1.9→2.1s are absorbed.
	defaultThreshold = 5 * time.Second
)

func main() {
	threshold := flag.Duration("threshold", defaultThreshold, "max allowed Elapsed per test")
	allowlistFile := flag.String("allowlist", "", "path to allowlist file (optional)")
	flag.Parse()

	allowlist := map[string]struct{}{}
	if *allowlistFile != "" {
		f, err := os.Open(*allowlistFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slowgate: open allowlist: %v\n", err)
			os.Exit(2)
		}
		parsed, err := parseAllowlist(f)
		_ = f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "slowgate: parse allowlist %s: %v\n", *allowlistFile, err)
			os.Exit(2)
		}
		allowlist = parsed
	}

	violations, err := evaluate(os.Stdin, *threshold, allowlist)
	if err != nil {
		fmt.Fprintf(os.Stderr, "slowgate: %v\n", err)
		os.Exit(2)
	}
	if len(violations) > 0 {
		footerPath := *allowlistFile
		if footerPath == "" {
			footerPath = defaultAllowlistPath
		}
		renderViolations(os.Stderr, violations, *threshold, footerPath)
		os.Exit(1)
	}
}

// testEvent matches the schema emitted by `go test -json` (cmd/test2json).
// Only fields we consume are declared.
type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

// violation records a single test that exceeded the threshold without an
// allowlist entry.
type violation struct {
	Package string
	Test    string
	Elapsed time.Duration
}

// evaluate consumes `go test -json` events from r and returns the set of
// tests whose Elapsed exceeded threshold and whose (Package, Test) pair
// is not in allowlist.
//
// Filtering rules (must match cmd/test2json semantics):
//   - only `pass` and `fail` Actions are considered (these are the per-test
//     terminal events with reliable Elapsed)
//   - empty Test field is the package-level summary event — its Elapsed
//     aggregates the whole package, not a single test, so we ignore it
//   - Test containing `/` is a subtest — its Elapsed is already wholly
//     contained within its parent root-test's Elapsed; counting both
//     would double-count the same wall-clock seconds
//   - skip / output / run / pause / cont — no test-level wall-clock signal
func evaluate(r io.Reader, threshold time.Duration, allowlist map[string]struct{}) ([]violation, error) {
	dec := json.NewDecoder(r)
	var out []violation
	for {
		var ev testEvent
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode test2json event: %w", err)
		}
		if v, ok := violationFromEvent(ev, threshold, allowlist); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// violationFromEvent returns the violation for a single test2json event when
// it represents an unallowlisted root-test pass/fail whose Elapsed exceeds
// threshold; otherwise returns the zero value with ok=false.
func violationFromEvent(ev testEvent, threshold time.Duration, allowlist map[string]struct{}) (violation, bool) {
	if ev.Action != "pass" && ev.Action != "fail" {
		return violation{}, false
	}
	if ev.Test == "" || strings.Contains(ev.Test, "/") {
		return violation{}, false
	}
	elapsed := time.Duration(ev.Elapsed * float64(time.Second))
	if elapsed <= threshold {
		return violation{}, false
	}
	if _, ok := allowlist[ev.Package+"\t"+ev.Test]; ok {
		return violation{}, false
	}
	return violation{Package: ev.Package, Test: ev.Test, Elapsed: elapsed}, true
}

// parseAllowlist reads the line-based allowlist format.
//
// Each non-comment, non-blank line must yield exactly two whitespace- or
// TAB-separated fields. The first is the Go import path of the test
// package, the second is the top-level test function name. The result map
// is keyed on `Package + "\t" + Test`; that exact composite key is what
// evaluate() looks up, so a single string-compare suffices in the hot path.
func parseAllowlist(r io.Reader) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	sc := bufio.NewScanner(r)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimRight(sc.Text(), "\r")
		// Skip blank and comment lines without trimming trailing whitespace,
		// so a `pkg<TAB>` data line (empty test name) can be diagnosed by
		// splitAllowlistLine rather than collapsing into "1 field".
		leftTrimmed := strings.TrimLeft(line, " \t")
		if leftTrimmed == "" || strings.HasPrefix(leftTrimmed, "#") {
			continue
		}
		fields := splitAllowlistLine(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("line %d: expected 2 fields (Package<TAB>Test), got %d (%q)", lineNum, len(fields), line)
		}
		if len(fields) > 2 {
			return nil, fmt.Errorf("line %d: extra fields after Test name (%q); inline `#` comments not supported on data lines", lineNum, line)
		}
		if fields[0] == "" {
			return nil, fmt.Errorf("line %d: empty package (%q)", lineNum, line)
		}
		if fields[1] == "" {
			return nil, fmt.Errorf("line %d: empty test name (%q)", lineNum, line)
		}
		out[fields[0]+"\t"+fields[1]] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan allowlist: %w", err)
	}
	return out, nil
}

// splitAllowlistLine prefers a single TAB separator (canonical format from
// slowgate's stderr report); falls back to any whitespace run for
// hand-edited entries. When TAB is present, all positional fields are
// preserved (including empty ones) so that `pkg<TAB>` can be diagnosed as
// "empty test name" rather than misreported as a single-field line.
func splitAllowlistLine(s string) []string {
	if strings.Contains(s, "\t") {
		parts := strings.Split(s, "\t")
		out := make([]string, len(parts))
		for i, p := range parts {
			out[i] = strings.TrimSpace(p)
		}
		return out
	}
	return strings.Fields(s)
}

// renderViolations writes a deterministic, actionable summary of breaches.
// Output shape:
//
//	slowgate: 3 test(s) exceeded 2s budget
//	  SLOW pkg/a TestX 3.412s > 2s
//	  ...
//	to allowlist a test, append `<Package><TAB><TestName>` to
//	<allowlistPath> with a leading `# <reason>` comment line.
//
// The footer references the actual allowlist path the caller passed via
// --allowlist (or the project-default const) so the actionable hint is
// always accurate.
func renderViolations(w io.Writer, vs []violation, threshold time.Duration, allowlistPath string) {
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Package != vs[j].Package {
			return vs[i].Package < vs[j].Package
		}
		return vs[i].Test < vs[j].Test
	})
	// Errors writing the diagnostic report are deliberately ignored — the
	// process is about to exit non-zero with the violations on stderr; a
	// secondary "could not write to stderr" message would be both noise and
	// itself dependent on the same writer that just failed.
	_, _ = fmt.Fprintf(w, "slowgate: %d test(s) exceeded %s budget\n", len(vs), threshold)
	for _, v := range vs {
		_, _ = fmt.Fprintf(w, "  SLOW %s %s %s > %s\n", v.Package, v.Test, v.Elapsed.Round(time.Millisecond), threshold)
	}
	_, _ = fmt.Fprintf(w, "to allowlist a test, append `<Package>\\t<TestName>` to %s\n", allowlistPath)
	_, _ = fmt.Fprintf(w, "with a leading `# <reason>` comment line documenting why the wall-clock cost is unavoidable.\n")
}
