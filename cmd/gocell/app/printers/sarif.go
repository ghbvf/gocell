package printers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/ghbvf/gocell/kernel/governance"
)

// SARIFPrinter renders results as a SARIF 2.1.0 log. Designed to be ingested
// by VS Code SARIF Explorer and GitHub Code Scanning without further
// post-processing.
//
// Design ref: golangci-lint pkg/printers/sarif.go — adopted top-level shape
// (runs[].tool.driver, runs[].results) and severity-to-level mapping.
// Improvement: golangci-lint leaves tool.driver.rules[] empty; we populate
// it deduplicated by Code so SARIF viewers can show the rule taxonomy.
type SARIFPrinter struct {
	w           io.Writer
	toolVersion string
}

// NewSARIFPrinter constructs a SARIF printer writing to w. toolVersion lands
// in runs[].tool.driver.version; pass the binary's build info or "dev".
func NewSARIFPrinter(w io.Writer, toolVersion string) *SARIFPrinter {
	if toolVersion == "" {
		toolVersion = "dev"
	}
	return &SARIFPrinter{w: w, toolVersion: toolVersion}
}

// SARIF schema constants. Pulled out so the JSON shape lives next to the
// values rather than scattered through code.
const (
	sarifSchema        = "https://json.schemastore.org/sarif-2.1.0.json"
	sarifVersion       = "2.1.0"
	sarifToolName      = "gocell"
	sarifToolInfoURI   = "https://github.com/ghbvf/gocell"
	sarifSrcRootBaseID = "SRCROOT"
)

// SARIF wire-format DTOs. Field names follow the SARIF 2.1.0 schema exactly,
// so the json tags drive the spelling — do not switch to camelCase shortcut
// here, the spec is the spec.
type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool               sarifTool                        `json:"tool"`
	Results            []sarifResult                    `json:"results"`
	OriginalUriBaseIDs map[string]sarifArtifactLocation `json:"originalUriBaseIds,omitempty"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string               `json:"id"`
	ShortDescription     sarifMessage         `json:"shortDescription"`
	DefaultConfiguration sarifRuleConfig      `json:"defaultConfiguration"`
	Properties           *sarifRuleProperties `json:"properties,omitempty"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

type sarifRuleProperties struct {
	IssueType string `json:"issueType,omitempty"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri,omitempty"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
}

// Print writes the full SARIF log. Results are sorted before emit so the
// rules[] array dedup is deterministic and golden output stays stable.
func (p *SARIFPrinter) Print(results []governance.ValidationResult) error {
	sorted := sortResults(results)

	rules := buildSARIFRules(sorted)
	sarifResults := make([]sarifResult, len(sorted))
	for i := range sorted {
		sarifResults[i] = toSARIFResult(sorted[i])
	}

	log := sarifLog{
		Schema:  sarifSchema,
		Version: sarifVersion,
		Runs: []sarifRun{
			{
				Tool: sarifTool{
					Driver: sarifDriver{
						Name:           sarifToolName,
						Version:        p.toolVersion,
						InformationURI: sarifToolInfoURI,
						Rules:          rules,
					},
				},
				Results: sarifResults,
				OriginalUriBaseIDs: map[string]sarifArtifactLocation{
					sarifSrcRootBaseID: {URI: ""},
				},
			},
		},
	}

	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	// Disable HTML escaping: see json.go for rationale. SARIF viewers (VS
	// Code SARIF Explorer, GitHub Code Scanning) do not interpret the
	// message text as HTML, so escaping `<` / `>` / `&` only obscures
	// rule descriptions that legitimately contain those characters.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(log); err != nil {
		return fmt.Errorf("encode sarif: %w", err)
	}
	return nil
}

// buildSARIFRules dedup-collects rules[] by Code. First occurrence of each
// Code wins for shortDescription/defaultConfiguration/issueType so the rule
// summary always reflects the same example the user sees in results[]. The
// returned slice is non-nil even when sorted is empty (SARIF allows []).
func buildSARIFRules(sorted []governance.ValidationResult) []sarifRule {
	if len(sorted) == 0 {
		return []sarifRule{}
	}
	seen := make(map[string]bool, len(sorted))
	rules := make([]sarifRule, 0, len(sorted))
	for i := range sorted {
		r := sorted[i]
		if seen[r.Code] {
			continue
		}
		seen[r.Code] = true
		rule := sarifRule{
			ID:               r.Code,
			ShortDescription: sarifMessage{Text: r.Message},
			DefaultConfiguration: sarifRuleConfig{
				Level: severityToSARIFLevel(r.Severity),
			},
		}
		if r.IssueType != "" {
			rule.Properties = &sarifRuleProperties{IssueType: string(r.IssueType)}
		}
		rules = append(rules, rule)
	}
	return rules
}

// toSARIFResult converts one ValidationResult to a SARIF result. Field is
// folded into the message text so SARIF viewers always show it; if the
// result is scope-only, locations[] is omitted and the scope name lands in
// the message prefix to keep it visible.
func toSARIFResult(r governance.ValidationResult) sarifResult {
	res := sarifResult{
		RuleID:  r.Code,
		Level:   severityToSARIFLevel(r.Severity),
		Message: sarifMessage{Text: composeSARIFMessage(r)},
	}
	if r.File != "" {
		loc := sarifLocation{
			PhysicalLocation: sarifPhysicalLocation{
				ArtifactLocation: sarifArtifactLocation{
					URI:       normalizeArtifactURI(r.File),
					URIBaseID: sarifSrcRootBaseID,
				},
			},
		}
		if r.Line > 0 {
			startCol := r.Column
			if startCol == 0 {
				// SARIF 2.1.0 §3.30.6: startColumn is 1-based and >= 1 when
				// startLine is set. Default to 1 when we know the line but
				// not the column.
				startCol = 1
			}
			loc.PhysicalLocation.Region = &sarifRegion{
				StartLine:   r.Line,
				StartColumn: startCol,
			}
		}
		res.Locations = []sarifLocation{loc}
	}
	return res
}

// composeSARIFMessage renders the message text shown in viewers. Scope-only
// findings prefix the scope name (e.g. "[scope: project] circular ...") so
// the context isn't lost when locations[] is omitted; field is appended
// inside parens so it tracks alongside the message for both file and
// scope-anchored results.
func composeSARIFMessage(r governance.ValidationResult) string {
	msg := r.Message
	if r.File == "" && r.Scope != "" {
		msg = fmt.Sprintf("[scope: %s] %s", r.Scope, msg)
	}
	if r.Field != "" {
		msg += fmt.Sprintf(" (field: %s)", r.Field)
	}
	return msg
}

// severityToSARIFLevel maps GoCell severity to SARIF 2.1.0 §3.27.10 level.
// We never emit "note" or "none" today; if the validator grows an
// info-level severity, this is the single edit point.
func severityToSARIFLevel(s governance.Severity) string {
	switch s {
	case governance.SeverityError:
		return "error"
	case governance.SeverityWarning:
		return "warning"
	default:
		return "none"
	}
}

// normalizeArtifactURI converts a repo-relative file path into an
// RFC 3986 compliant relative URI suitable for SARIF artifactLocation.uri.
// Steps: backslash → slash (Windows path defense) → path.Clean →
// PathEscape per segment so spaces / CJK / reserved chars become %XX.
func normalizeArtifactURI(file string) string {
	s := strings.ReplaceAll(file, "\\", "/")
	s = path.Clean(s)
	if s == "." || s == "/" {
		return s
	}
	// Strip leading "./" left over from Clean of "./foo"
	// (path.Clean keeps "./" as ".", which we already returned above).
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
