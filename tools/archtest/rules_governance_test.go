//go:build archtest

// Package archtest guards agent instruction rule files.
//
//   - INVARIANT: AGENT-RULES-GOVERNANCE-01
//
// Agent rules are prompt-context inputs, not changelogs or enforcement
// inventories. This invariant keeps `.claude/rules/gocell/*.md` short and
// future-facing. The check is intentionally a repository content gate: prose
// semantics cannot be made Go type-system Hard, so the strongest useful
// enforcement is CI-blocking text shape with synthetic red cases and production
// anti-vacuity.
package archtest

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const agentRulesGovernanceRuleID = "AGENT-RULES-GOVERNANCE-01"

// These prompt-context limits are deliberately loose upper bounds: each is
// more than 2x the current largest rule file, so threshold changes should be
// explicit policy updates rather than incidental edits.
const (
	agentRuleMaxBytes = 16 * 1024
	agentRuleMaxLines = 220
	agentRuleMaxRunes = 240
)

var agentRuleHistoryPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{name: "current PR marker", re: regexp.MustCompile(`本 PR`)},
	{name: "landed marker", re: regexp.MustCompile(`已落`)},
	{name: "review round marker", re: regexp.MustCompile(`(?i)\bround(?:-|[[:space:]])[0-9]+\b`)},
	{name: "review finding marker", re: regexp.MustCompile(`(?i)\breview\s+(?:F[0-9]+|finding)\b`)},
	{name: "deferred backlog marker", re: regexp.MustCompile(`推迟 backlog`)},
	{name: "PR number changelog marker", re: regexp.MustCompile(`\bPR\s*#[0-9]+\b`)},
	{name: "PR hyphen changelog marker", re: regexp.MustCompile(`\bPR-[0-9]+\b`)},
	{name: "PR token changelog marker", re: regexp.MustCompile(`\bPR-[A-Z]+[0-9]+[A-Za-z0-9_-]*\b`)},
}

type agentRuleFile struct {
	rel   string
	bytes []byte
}

func TestAgentRulesGovernance(t *testing.T) {
	t.Run("production_rules", func(t *testing.T) {
		Report(t, agentRulesGovernanceRuleID, CheckAgentRulesGovernance(t, ConfigForExternalCell{}))
	})
	t.Run("synthetic_red_cases", testAgentRulesGovernanceSyntheticRedCases)
}

func CheckAgentRulesGovernance(t testing.TB, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	_ = cfg

	root := findModuleRoot(t)
	scope := DirsScope(root, []string{".claude/rules/gocell"})
	contentFiles, err := loadContentFiles(scope, []string{".md"})
	if err != nil {
		return []Diagnostic{diagFile(".claude/rules/gocell", fmt.Sprintf("cannot scan agent rules: %v", err))}
	}
	files := make([]agentRuleFile, 0, len(contentFiles))
	for _, fc := range contentFiles {
		files = append(files, agentRuleFile{rel: fc.Rel, bytes: fc.Bytes})
	}
	return checkAgentRulesGovernanceFiles(files)
}

func checkAgentRulesGovernanceFiles(files []agentRuleFile) []Diagnostic {
	if len(files) == 0 {
		return []Diagnostic{diagFile(".claude/rules/gocell", "agent rules scan is vacuous: no .md files found")}
	}

	var out []Diagnostic
	for _, file := range files {
		out = append(out, checkAgentRuleFile(file)...)
	}
	return Canonical(out)
}

func testAgentRulesGovernanceSyntheticRedCases(t *testing.T) {
	tests := []struct {
		name       string
		rel        string
		body       string
		wantSubstr []string
	}{
		{
			name: "missing_heading",
			rel:  ".claude/rules/gocell/missing-heading.md",
			body: "Future behavior without a heading.\n",
			wantSubstr: []string{
				"must start with a Markdown H1 heading",
			},
		},
		{
			name: "history_markers",
			rel:  ".claude/rules/gocell/history.md",
			body: "# History\n本 PR 已落 review F2 round-3 推迟 backlog PR #123 PR-1798 PR-A10\n",
			wantSubstr: []string{
				"historical marker",
			},
		},
		{
			name: "byte_limit",
			rel:  ".claude/rules/gocell/byte-limit.md",
			body: "# Big\n" + strings.Repeat(strings.Repeat("x", 200)+"\n", agentRuleMaxBytes/201+2),
			wantSubstr: []string{
				"exceeds 16384 bytes",
			},
		},
		{
			name: "line_limit",
			rel:  ".claude/rules/gocell/line-limit.md",
			body: "# Big\n" + strings.Repeat("line\n", agentRuleMaxLines+1),
			wantSubstr: []string{
				"exceeds 220 lines",
			},
		},
		{
			name: "legitimate_future_words",
			rel:  ".claude/rules/gocell/future.md",
			body: "# Future\nUse round-robin retry and PR-time checks for current behavior.\n",
		},
		{
			name: "bom_crlf_heading",
			rel:  ".claude/rules/gocell/bom-crlf.md",
			body: string([]byte{0xef, 0xbb, 0xbf}) + "# Future\r\nUse current behavior.\r\n",
		},
		{
			name: "oversized_shape",
			rel:  ".claude/rules/gocell/oversized.md",
			body: "# Oversized\n" + strings.Repeat("x", agentRuleMaxRunes+1) + "\n",
			wantSubstr: []string{
				"line exceeds",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := checkAgentRuleFile(agentRuleFile{rel: tt.rel, bytes: []byte(tt.body)})
			if len(tt.wantSubstr) == 0 {
				assert.Empty(t, diags)
				return
			}
			require.NotEmpty(t, diags)
			got := diagnosticsJoined(diags)
			for _, want := range tt.wantSubstr {
				assert.Contains(t, got, want)
			}
		})
	}

	t.Run("vacuous_scan", func(t *testing.T) {
		diags := checkAgentRulesGovernanceFiles(nil)
		require.NotEmpty(t, diags)
		assert.Contains(t, diagnosticsJoined(diags), "agent rules scan is vacuous")
	})
}

func checkAgentRuleFile(file agentRuleFile) []Diagnostic {
	var out []Diagnostic
	b := normalizeAgentRuleBytes(file.bytes)
	if !bytes.HasPrefix(b, []byte("# ")) {
		out = append(out, diagFile(file.rel, "agent rule must start with a Markdown H1 heading"))
	}
	if len(b) > agentRuleMaxBytes {
		out = append(out, diagFile(file.rel, fmt.Sprintf(
			"rule file exceeds %d bytes; move rationale/history to docs, ADR, archtest godoc, or GitHub Issues",
			agentRuleMaxBytes)))
	}

	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > agentRuleMaxLines {
		out = append(out, diagFile(file.rel, fmt.Sprintf(
			"rule file exceeds %d lines; split by topic/path or move long material out of rules",
			agentRuleMaxLines)))
	}
	for i, line := range lines {
		lineRunes := utf8.RuneCount(line)
		if lineRunes > agentRuleMaxRunes {
			out = append(out, diagAt(file.rel, i+1, fmt.Sprintf(
				"line exceeds %d runes; wrap prose or move long evidence out of rules",
				agentRuleMaxRunes)))
		}
	}

	for lineNo, text := range ruleBodyLines(b) {
		for _, pat := range agentRuleHistoryPatterns {
			if pat.re.MatchString(text) {
				out = append(out, diagAt(file.rel, lineNo, fmt.Sprintf(
					"historical marker %q belongs in ADR/docs/archtest godoc/GitHub Issues, not agent rules",
					pat.name)))
			}
		}
	}

	return Canonical(out)
}

func normalizeAgentRuleBytes(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func ruleBodyLines(b []byte) map[int]string {
	out := make(map[int]string)
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := 0; i < len(lines); i++ {
		out[i+1] = lines[i]
	}
	return out
}

func diagnosticsJoined(diags []Diagnostic) string {
	var parts []string
	for _, d := range diags {
		parts = append(parts, d.Message)
	}
	return strings.Join(parts, "\n")
}
