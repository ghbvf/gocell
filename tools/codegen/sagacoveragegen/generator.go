// Package sagacoveragegen renders the saga fanout artifacts derived from the
// saga.Status / journal.EventKind const sets — the single source of truth for
// SAGA-STATUS-FANOUT-COVERAGE-01.
//
// It produces three byte-locked artifacts (consumed by `gocell generate
// saga-coverage` to write, and by the archtest to verify):
//
//   - terminal_coverage_gen.go — a struct with exactly one field per TERMINAL
//     saga.Status. The hand-written keyless composite literal of that struct in
//     framework/kernel/saga/sagajournaltest/conformance.go is a COMPILE-TIME exhaustiveness
//     gate: add a terminal status const → regen adds a field → the keyless literal
//     fails to compile ("too few values in struct literal") until a happy-path
//     driver is supplied. This is the Hard half of the fanout closure.
//   - the readyz.md saga lifecycle status table (Status / Value / Phase /
//     Terminal? rows) — generated from the const value + Status.IsTerminal().
//   - the alerting-rules.md "kind 速查" legend (value=wire entries) — generated
//     from the EventKind value + EventKind.String().
//
// Names are derived from String() via PascalCase under the project naming
// convention (const name == "Status"+PascalCase(String()) /
// "Kind"+PascalCase(String())); a convention violation surfaces as a build
// failure of the generated const references, never a silent drift.
package sagacoveragegen

import (
	"bytes"
	"fmt"
	"go/format"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Marker pairs delimit the generated regions inside the hand-curated ops docs.
// The CLI replaces the text between each pair; the archtest extracts and
// byte-compares it against the rendered fragment.
const (
	ReadyzTableStartMarker = "<!-- gocell:generated:saga-status-table — DO NOT EDIT (regen: gocell generate saga-coverage) -->"
	ReadyzTableEndMarker   = "<!-- /gocell:generated:saga-status-table -->"
	KindLegendStartMarker  = "<!-- gocell:generated:saga-event-kind-legend — DO NOT EDIT (regen: gocell generate saga-coverage) -->"
	KindLegendEndMarker    = "<!-- /gocell:generated:saga-event-kind-legend -->"
)

// statusPhase is the single source for the readyz table Phase column — ops
// narrative that is NOT derivable from the const set. Render fails closed if a
// status lacks a phase entry, so a newly-added status forces an explicit
// description rather than a silent blank cell.
var statusPhase = map[saga.Status]string{
	saga.StatusPending:            "Not yet started",
	saga.StatusRunning:            "Executing steps forward",
	saga.StatusCompensating:       "A step failed; rolling back in reverse",
	saga.StatusSucceeded:          "All steps committed",
	saga.StatusFailed:             "Forward failure, no rollback entered",
	saga.StatusCompensated:        "Rollback completed cleanly",
	saga.StatusExpired:            "Overall timeout elapsed",
	saga.StatusCompensationFailed: "Rollback itself encountered a step failure",
}

// ErrNoPhase is returned when a saga.Status has no statusPhase entry. Declared
// via errcode.New (EXPORTED-ERROR-NEW-01 bans exported errors.New sentinels).
var ErrNoPhase = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
	"sagacoveragegen: saga.Status missing a statusPhase description")

// Artifacts holds the three rendered fanout artifacts.
type Artifacts struct {
	// TerminalCoverageGo is the gofmt-canonical content of
	// framework/kernel/saga/sagajournaltest/terminal_coverage_gen.go.
	TerminalCoverageGo []byte
	// ReadyzTable is the markdown status table fragment (no markers, trailing \n).
	ReadyzTable string
	// KindLegend is the single-line markdown legend fragment (trailing \n).
	KindLegend string
}

type statusInfo struct {
	value    int
	name     string // const name, e.g. "StatusCompensationFailed"
	stripped string // const name minus "Status", e.g. "CompensationFailed"
	terminal bool
	phase    string
}

type kindInfo struct {
	value int
	wire  string // EventKind.String(), e.g. "saga_compensation_failed"
}

// Render produces the three artifacts purely from the imported saga /
// journal const sets (no filesystem access).
func Render() (Artifacts, error) {
	statuses, err := collectStatuses()
	if err != nil {
		return Artifacts{}, err
	}
	goSrc, err := renderTerminalCoverageGo(statuses)
	if err != nil {
		return Artifacts{}, err
	}
	return Artifacts{
		TerminalCoverageGo: goSrc,
		ReadyzTable:        renderReadyzTable(statuses),
		KindLegend:         renderKindLegend(collectKinds()),
	}, nil
}

// collectStatuses enumerates the saga.Status const set in value order via the
// type's own Valid() bound, so a newly-added status auto-joins once Valid() and
// IsTerminal() are extended.
func collectStatuses() ([]statusInfo, error) {
	var out []statusInfo
	for s := saga.StatusPending; s.Valid(); s++ {
		stripped := pascalCase(s.String())
		phase, ok := statusPhase[s]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNoPhase, s.String())
		}
		out = append(out, statusInfo{
			value:    int(s),
			name:     "Status" + stripped,
			stripped: stripped,
			terminal: s.IsTerminal(),
			phase:    phase,
		})
	}
	return out, nil
}

// collectKinds enumerates the journal.EventKind const set in value order.
func collectKinds() []kindInfo {
	var out []kindInfo
	for k := journal.KindStepStarted; k.Valid(); k++ {
		out = append(out, kindInfo{value: int(k), wire: k.String()})
	}
	return out
}

// renderTerminalCoverageGo emits the terminal_coverage_gen.go source: one struct
// field per terminal status (the compile-time exhaustiveness gate), the want
// array, and the drivers() accessor.
func renderTerminalCoverageGo(statuses []statusInfo) ([]byte, error) {
	var terminals []statusInfo
	for _, s := range statuses {
		if s.terminal {
			terminals = append(terminals, s)
		}
	}

	var b bytes.Buffer
	b.WriteString(`// Code generated by gocell generate saga-coverage. DO NOT EDIT.
// Source of truth: saga.Status const set ∩ Status.IsTerminal().

package sagajournaltest

import "github.com/ghbvf/gocell/framework/kernel/saga"

// terminalCoverage has exactly one field per terminal saga.Status. The
// hand-written KEYLESS composite literal of this type (terminalHappyPaths in
// conformance.go) is a COMPILE-TIME exhaustiveness gate: add a terminal status
// const → regen adds a field → the keyless literal fails to compile ("too few
// values in struct literal") until a happy-path driver is supplied.
type terminalCoverage struct {
`)
	for _, s := range terminals {
		fmt.Fprintf(&b, "\t%s terminalDriver\n", s.stripped)
	}
	b.WriteString(`}

// terminalCoverageWant pairs each terminalCoverage field position with the
// terminal status it must drive to, in field order, for the harness's got==want
// assertion (catches a mis-mapped registry slot).
var terminalCoverageWant = [...]saga.Status{
`)
	for _, s := range terminals {
		fmt.Fprintf(&b, "\tsaga.%s,\n", s.name)
	}
	b.WriteString(`}

func (c terminalCoverage) drivers() []terminalDriver {
	return []terminalDriver{
`)
	for _, s := range terminals {
		fmt.Fprintf(&b, "\t\tc.%s,\n", s.stripped)
	}
	b.WriteString(`	}
}
`)

	src, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("sagacoveragegen: gofmt terminal_coverage_gen.go: %w", err)
	}
	return src, nil
}

// renderReadyzTable emits the readyz.md saga lifecycle status table.
func renderReadyzTable(statuses []statusInfo) string {
	var b strings.Builder
	b.WriteString("| Status | Value | Phase | Terminal? |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, s := range statuses {
		term := "No"
		if s.terminal {
			term = "Yes"
		}
		fmt.Fprintf(&b, "| `%s` | %d | %s | %s |\n", s.stripped, s.value, s.phase, term)
	}
	return b.String()
}

// renderKindLegend emits the alerting-rules.md "kind 速查" legend as one line.
func renderKindLegend(kinds []kindInfo) string {
	entries := make([]string, 0, len(kinds))
	for _, k := range kinds {
		entries = append(entries, fmt.Sprintf("%d=%s", k.value, k.wire))
	}
	return "`kind` 速查：" + strings.Join(entries, "，") + "。\n"
}

// pascalCase converts a snake_case label to PascalCase
// ("compensation_failed" → "CompensationFailed").
func pascalCase(snake string) string {
	parts := strings.Split(snake, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}
