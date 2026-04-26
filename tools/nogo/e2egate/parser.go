// Package e2egate parses `go test -json` event streams and decides whether
// the run satisfies the e2e execution gate: at least one test must have
// actually executed (passed or failed), and no package may declare tests yet
// have all of them skipped — that pattern indicates require.Docker / PG / RMQ
// gates were not opened, producing a misleading green CI.
//
// The parser is invoked from the command line as a stdin pipe consumer:
//
//	go test -tags=e2e -json ./tests/e2e/... | tee /tmp/e2e.json | e2egate
//
// On gate failure, exit code 1 with reasons on stderr. On success, exit 0
// with empty stdout/stderr. The pipeline-friendly tee preserves the raw
// event stream for log archival.
//
// ref: cmd/internal/test2json — event schema (Action, Package, Test, Output, Elapsed)
package e2egate

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Parse reads `go test -json` events from r and returns the aggregated Result.
// Parse returns an error only on I/O failures or malformed JSON; gate-failure
// conditions are reported via Result.Failed() / Result.Reasons.
func Parse(r io.Reader) (Result, error) {
	res := Result{Packages: map[string]*PackageStat{}}
	dec := json.NewDecoder(r)
	for {
		var ev testEvent
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				break
			}
			return res, fmt.Errorf("e2egate: decode test2json event: %w", err)
		}
		res.apply(ev)
	}
	res.evaluate()
	return res, nil
}

// Result is the aggregated outcome of a `go test -json` run.
type Result struct {
	// TotalExecuted is the count of test-level events that terminated with
	// pass or fail (i.e., the test actually ran a body).
	TotalExecuted int
	// TotalSkipped is the count of test-level events that terminated with
	// skip (including conditional t.Skip).
	TotalSkipped int
	// Packages keyed by import path; each tracks per-package counters.
	Packages map[string]*PackageStat
	// Reasons holds gate-failure messages (one per triggered rule); empty
	// when the gate passes.
	Reasons []string
}

// PackageStat tracks per-package test counters and the terminal action
// emitted for the package event (Test == "").
type PackageStat struct {
	// Executed counts pass + fail tests.
	Executed int
	// Skipped counts skipped tests.
	Skipped int
	// BuildFailed is true when the package emitted a build failure (no
	// test events; package-level "fail" action with build-failed output).
	BuildFailed bool
	// Action is the package-level terminal action ("pass", "fail", "skip").
	Action string
}

// Failed reports whether the gate fails. The gate fails when at least one
// rule in Reasons triggered.
func (r Result) Failed() bool {
	return len(r.Reasons) > 0
}

// testEvent matches the schema emitted by `go test -json` (cmd/internal/test2json).
// Only the fields we consume are declared.
type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

func (r *Result) apply(ev testEvent) {
	if ev.Package == "" {
		return
	}
	stat, ok := r.Packages[ev.Package]
	if !ok {
		stat = &PackageStat{}
		r.Packages[ev.Package] = stat
	}
	if ev.Action == "output" {
		return
	}
	if ev.Test == "" {
		// Package-level terminal event.
		switch ev.Action {
		case "pass", "fail", "skip":
			stat.Action = ev.Action
		}
		return
	}
	// Test-level terminal events.
	switch ev.Action {
	case "pass", "fail":
		stat.Executed++
		r.TotalExecuted++
	case "skip":
		stat.Skipped++
		r.TotalSkipped++
	}
}

func (r *Result) evaluate() {
	names := make([]string, 0, len(r.Packages))
	for n := range r.Packages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		stat := r.Packages[name]
		if stat.Action == "fail" && stat.Executed == 0 && stat.Skipped == 0 {
			stat.BuildFailed = true
			r.Reasons = append(r.Reasons,
				fmt.Sprintf("package %q build failed (no tests ran)", name))
			continue
		}
		if stat.Executed == 0 && (stat.Skipped > 0 || stat.Action == "skip") {
			r.Reasons = append(r.Reasons,
				fmt.Sprintf("package %q all-skipped (%d skipped, 0 executed)", name, stat.Skipped))
		}
	}
	if r.TotalExecuted == 0 {
		// Prepend the global summary so it appears before per-package reasons.
		r.Reasons = append([]string{"0 tests executed across all packages"}, r.Reasons...)
	}
}
