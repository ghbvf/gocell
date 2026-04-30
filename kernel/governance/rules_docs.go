package governance

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const docNamingGuardRelPath = "docs/architecture/naming-guard.yaml"

type docNamingGuardConfig struct {
	Include      []string               `yaml:"include"`
	Exclude      []string               `yaml:"exclude"`
	Replacements []docNamingReplacement `yaml:"replacements"`
}

type docNamingReplacement struct {
	Literal     string `yaml:"literal"`
	Replacement string `yaml:"replacement"`
}

// validateDOCNAME01 scans active documentation for legacy literals declared in
// docs/architecture/naming-guard.yaml. It is strict-only: non-strict validation
// remains silent so historical documents do not become warnings by accident.
func (v *Validator) validateDOCNAME01(strict bool) []ValidationResult {
	if !strict || v.root == "" {
		return nil
	}

	cfg, ok, results := v.loadDocNamingGuard()
	if !ok || len(results) > 0 {
		return results
	}

	targets, targetResults := v.docNamingTargets(cfg)
	results = append(results, targetResults...)
	if len(results) > 0 {
		return results
	}

	for _, rel := range targets {
		data, err := v.readFile(filepath.Join(v.root, filepath.FromSlash(rel)))
		if err != nil {
			results = append(results, docNamingResult(
				rel,
				0,
				0,
				"content",
				fmt.Sprintf("cannot read active document: %v", err),
				IssueInvalid,
			))
			continue
		}
		results = append(results, scanDocNamingLiterals(rel, string(data), cfg.Replacements)...)
	}
	return results
}

func (v *Validator) loadDocNamingGuard() (docNamingGuardConfig, bool, []ValidationResult) {
	var cfg docNamingGuardConfig
	data, err := v.readFile(filepath.Join(v.root, filepath.FromSlash(docNamingGuardRelPath)))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, []ValidationResult{docNamingResult(
			docNamingGuardRelPath,
			0,
			0,
			"",
			"document naming guard is required for strict validation",
			IssueRequired,
		)}
	}
	if err != nil {
		return cfg, false, []ValidationResult{docNamingResult(
			docNamingGuardRelPath,
			0,
			0,
			"",
			fmt.Sprintf("cannot read document naming guard: %v", err),
			IssueInvalid,
		)}
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, false, []ValidationResult{docNamingResult(
			docNamingGuardRelPath,
			0,
			0,
			"",
			fmt.Sprintf("cannot parse document naming guard: %v", err),
			IssueInvalid,
		)}
	}

	var results []ValidationResult
	if len(cfg.Include) == 0 {
		results = append(results, docNamingResult(
			docNamingGuardRelPath, 0, 0, "include",
			"document naming guard must declare at least one include pattern",
			IssueRequired,
		))
	}
	if len(cfg.Replacements) == 0 {
		results = append(results, docNamingResult(
			docNamingGuardRelPath, 0, 0, "replacements",
			"document naming guard must declare at least one replacement",
			IssueRequired,
		))
	}
	for i, repl := range cfg.Replacements {
		if repl.Literal == "" || repl.Replacement == "" {
			results = append(results, docNamingResult(
				docNamingGuardRelPath, 0, 0, fmt.Sprintf("replacements[%d]", i),
				"document naming guard replacement requires literal and replacement",
				IssueRequired,
			))
		}
	}
	return cfg, true, results
}

func (v *Validator) docNamingTargets(cfg docNamingGuardConfig) ([]string, []ValidationResult) {
	seen := map[string]struct{}{}
	var results []ValidationResult

	for _, include := range cfg.Include {
		results = append(results, v.collectDocNamingInclude(include, cfg.Exclude, seen)...)
	}

	targets := make([]string, 0, len(seen))
	for rel := range seen {
		targets = append(targets, rel)
	}
	sort.Strings(targets)
	return targets, results
}

func (v *Validator) collectDocNamingInclude(include string, exclude []string, seen map[string]struct{}) []ValidationResult {
	include = strings.TrimSpace(include)
	if include == "" {
		return nil
	}

	includeSlash := filepath.ToSlash(include)
	switch {
	case strings.HasSuffix(includeSlash, "/**"):
		return v.walkDocNamingInclude(includeSlash, exclude, seen)
	case hasGlobMeta(includeSlash):
		return v.globDocNamingInclude(includeSlash, exclude, seen)
	default:
		v.addDocNamingTarget(filepath.Join(v.root, filepath.FromSlash(includeSlash)), exclude, seen)
		return nil
	}
}

func (v *Validator) walkDocNamingInclude(include string, exclude []string, seen map[string]struct{}) []ValidationResult {
	baseRel := strings.TrimSuffix(include, "/**")
	baseAbs := filepath.Join(v.root, filepath.FromSlash(baseRel))
	info, statErr := os.Stat(baseAbs)
	if statErr != nil || !info.IsDir() {
		return nil
	}
	err := filepath.WalkDir(baseAbs, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		v.addDocNamingTarget(abs, exclude, seen)
		return nil
	})
	if err == nil {
		return nil
	}
	return []ValidationResult{docNamingResult(
		baseRel, 0, 0, "",
		fmt.Sprintf("cannot walk document naming include %q: %v", include, err),
		IssueInvalid,
	)}
}

func (v *Validator) globDocNamingInclude(include string, exclude []string, seen map[string]struct{}) []ValidationResult {
	matches, err := filepath.Glob(filepath.Join(v.root, filepath.FromSlash(include)))
	if err != nil {
		return []ValidationResult{docNamingResult(
			docNamingGuardRelPath, 0, 0, "include",
			fmt.Sprintf("invalid document naming include pattern %q: %v", include, err),
			IssueInvalid,
		)}
	}
	for _, match := range matches {
		v.addDocNamingTarget(match, exclude, seen)
	}
	return nil
}

func (v *Validator) addDocNamingTarget(abs string, exclude []string, seen map[string]struct{}) {
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return
	}
	rel, err := filepath.Rel(v.root, abs)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if docNamingExcluded(rel, exclude) {
		return
	}
	seen[rel] = struct{}{}
}

func scanDocNamingLiterals(file, content string, replacements []docNamingReplacement) []ValidationResult {
	var results []ValidationResult
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		for _, repl := range replacements {
			for col := findDocLiteral(line, repl.Literal, 0); col >= 0; col = findDocLiteral(line, repl.Literal, col+len(repl.Literal)) {
				results = append(results, docNamingResult(
					file,
					lineNo,
					col+1,
					"content",
					fmt.Sprintf("active document contains legacy literal %q; use %q", repl.Literal, repl.Replacement),
					IssueForbidden,
				))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		results = append(results, docNamingResult(
			file,
			0,
			0,
			"content",
			fmt.Sprintf("cannot scan active document: %v", err),
			IssueInvalid,
		))
	}
	return results
}

func findDocLiteral(line, literal string, start int) int {
	if literal == "" || start >= len(line) {
		return -1
	}
	for {
		idx := strings.Index(line[start:], literal)
		if idx < 0 {
			return -1
		}
		idx += start
		end := idx + len(literal)
		if docNameBoundary(line, idx-1) && docNameBoundary(line, end) {
			return idx
		}
		start = end
		if start >= len(line) {
			return -1
		}
	}
}

func docNameBoundary(s string, idx int) bool {
	if idx < 0 || idx >= len(s) {
		return true
	}
	return !isDocNameChar(s[idx])
}

func isDocNameChar(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '-' ||
		c == '_'
}

func docNamingExcluded(rel string, patterns []string) bool {
	for _, pattern := range patterns {
		if docNamingPatternMatch(rel, pattern) {
			return true
		}
	}
	return false
}

func docNamingPatternMatch(rel, pattern string) bool {
	rel = filepath.ToSlash(rel)
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return false
	}
	if before, ok := strings.CutSuffix(pattern, "/**"); ok {
		base := before
		return rel == base || strings.HasPrefix(rel, base+"/")
	}
	if ok, err := path.Match(pattern, rel); err == nil && ok {
		return true
	}
	return rel == pattern
}

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func docNamingResult(file string, line, column int, field, message string, issue IssueType) ValidationResult {
	return ValidationResult{
		Code:      "DOC-NAME-01",
		Severity:  SeverityError,
		IssueType: issue,
		File:      file,
		Field:     field,
		Message:   message,
		Line:      line,
		Column:    column,
	}
}
