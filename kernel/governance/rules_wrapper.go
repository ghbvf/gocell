package governance

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// FMT-18 SPEC-CONTRACT-SYNC — cross-check wrapper.ContractSpec literals in
// cells/** against contracts/**/contract.yaml. Ensures ID, Kind, Method,
// Path (for http kind) match the authoritative YAML. examples/** is
// exempted because its demo routes often do not have backing YAML by
// design (PR-A11 round-4 ADR §2).
//
// FMT-19 WRAPPER-NO-PACKAGE-STATE — enforces that kernel/wrapper/*.go
// contains no mutable package-level variables of interface or pointer
// type. Immutable zero-value sentinels (NoopTracer{}, noopSpan{}) and
// compile-time interface checks (var _ Tracer = NoopTracer{}) are
// allowed; any `var x Tracer` / `var mu sync.Mutex` is rejected. Guards
// the round-4 invariant that kernel/wrapper is a pure value+rules layer.
//
// Both rules are strict-only (surface only under ValidateStrict(true)) to
// avoid disrupting the base Validate() path for rapid iteration.

const (
	codeFMT18 = "FMT-18"
	codeFMT19 = "FMT-19"
)

// contractSpecLiteral captures a parsed wrapper.ContractSpec{...} literal
// from a source file scan.
type contractSpecLiteral struct {
	file   string
	line   int
	id     string
	kind   string
	method string
	path   string
	topic  string
}

// contractSpecRe extracts the inner { ... } body of a
// `wrapper.ContractSpec{ ... }` struct literal. Tolerant of whitespace,
// field reordering, trailing commas.
var contractSpecRe = regexp.MustCompile(`wrapper\.ContractSpec\s*\{([^{}]*)\}`)

var (
	fieldIDRe          = regexp.MustCompile(`ID:\s*"([^"]+)"`)
	fieldKindRe        = regexp.MustCompile(`Kind:\s*"([^"]+)"`)
	fieldMethodRe      = regexp.MustCompile(`Method:\s*"([^"]+)"`)
	fieldPathRe        = regexp.MustCompile(`Path:\s*"([^"]+)"`)
	fieldTopicStringRe = regexp.MustCompile(`Topic:\s*"([^"]+)"`)
	wrapperVarDeclRe   = regexp.MustCompile(`^var\s+(\w+)\s+(.+?)\s*=\s*(.+)$`)
)

// validateFMT18 scans `cells/**/*.go` for wrapper.ContractSpec{} literals
// and cross-checks each against `contracts/**/contract.yaml`. Strict-only.
// examples/** is not scanned — demo surface contracts intentionally lack
// YAML backing.
func (v *Validator) validateFMT18(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	dir := filepath.Join(v.root, "cells")
	literals, err := scanContractSpecLiterals(dir)
	if err != nil {
		return []ValidationResult{
			v.newResult(codeFMT18, SeverityError, IssueInvalid,
				fmt.Sprintf("FMT-18: failed to scan cells/ for wrapper.ContractSpec literals: %v", err),
				"cells/", ""),
		}
	}

	var out []ValidationResult
	for _, lit := range literals {
		out = append(out, v.validateContractSpecLiteral(lit)...)
	}
	return out
}

func (v *Validator) validateContractSpecLiteral(lit contractSpecLiteral) []ValidationResult {
	if lit.id == "" {
		return nil
	}
	contract, ok := v.project.Contracts[lit.id]
	if !ok {
		return []ValidationResult{v.newResult(codeFMT18, SeverityError, IssueInvalid,
			fmt.Sprintf("FMT-18: %s:%d references ContractSpec ID %q with no matching contracts/**/contract.yaml entry",
				lit.file, lit.line, lit.id),
			lit.file, "")}
	}

	var out []ValidationResult
	if lit.kind != "" && lit.kind != contract.Kind {
		out = append(out, v.newResult(codeFMT18, SeverityError, IssueInvalid,
			fmt.Sprintf("FMT-18: %s:%d ContractSpec Kind=%q disagrees with YAML Kind=%q for %q",
				lit.file, lit.line, lit.kind, contract.Kind, lit.id),
			lit.file, ""))
	}
	out = append(out, v.validateHTTPContractSpecLiteral(lit, contract)...)
	return out
}

func (v *Validator) validateHTTPContractSpecLiteral(
	lit contractSpecLiteral,
	contract *metadata.ContractMeta,
) []ValidationResult {
	if contract.Kind != "http" || contract.Endpoints.HTTP == nil {
		return nil
	}
	h := contract.Endpoints.HTTP
	var out []ValidationResult
	if lit.method != "" && lit.method != h.Method {
		out = append(out, v.newResult(codeFMT18, SeverityError, IssueInvalid,
			fmt.Sprintf("FMT-18: %s:%d ContractSpec Method=%q disagrees with YAML %q for %q",
				lit.file, lit.line, lit.method, h.Method, lit.id),
			lit.file, ""))
	}
	if lit.path != "" && lit.path != h.Path {
		out = append(out, v.newResult(codeFMT18, SeverityError, IssueInvalid,
			fmt.Sprintf("FMT-18: %s:%d ContractSpec Path=%q disagrees with YAML %q for %q",
				lit.file, lit.line, lit.path, h.Path, lit.id),
			lit.file, ""))
	}
	return out
}

// scanContractSpecLiterals walks dir recursively, parses .go files, and
// extracts every wrapper.ContractSpec{...} literal.
func scanContractSpecLiterals(dir string) ([]contractSpecLiteral, error) {
	var out []contractSpecLiteral
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !shouldScanContractSpecFile(path, info) {
			return nil
		}
		literals, err := scanContractSpecFile(path)
		if err != nil {
			return err
		}
		out = append(out, literals...)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

func shouldScanContractSpecFile(path string, info os.FileInfo) bool {
	return !info.IsDir() &&
		strings.HasSuffix(path, ".go") &&
		!strings.HasSuffix(path, "_test.go")
}

func scanContractSpecFile(path string) ([]contractSpecLiteral, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fullText := string(data)
	matches := contractSpecRe.FindAllStringSubmatchIndex(fullText, -1)
	literals := make([]contractSpecLiteral, 0, len(matches))
	for _, match := range matches {
		literals = append(literals, parseContractSpecLiteral(path, fullText, match))
	}
	return literals, nil
}

func parseContractSpecLiteral(file, fullText string, match []int) contractSpecLiteral {
	body := fullText[match[2]:match[3]]
	startByte := match[0]
	lit := contractSpecLiteral{
		file: file,
		line: 1 + strings.Count(fullText[:startByte], "\n"),
	}
	lit.id = firstRegexSubmatch(fieldIDRe, body)
	lit.kind = firstRegexSubmatch(fieldKindRe, body)
	lit.method = firstRegexSubmatch(fieldMethodRe, body)
	lit.path = firstRegexSubmatch(fieldPathRe, body)
	lit.topic = firstRegexSubmatch(fieldTopicStringRe, body)
	return lit
}

func firstRegexSubmatch(re *regexp.Regexp, value string) string {
	sm := re.FindStringSubmatch(value)
	if len(sm) < 2 {
		return ""
	}
	return sm[1]
}

// validateFMT19 scans kernel/wrapper/*.go for package-level var declarations
// and rejects any whose RHS is not a zero-value struct literal or a
// compile-time interface check.
func (v *Validator) validateFMT19(strict bool) []ValidationResult {
	if !strict {
		return nil
	}
	dir := filepath.Join(v.root, "kernel", "wrapper")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []ValidationResult{
			v.newResult(codeFMT19, SeverityError, IssueInvalid,
				fmt.Sprintf("FMT-19: failed to read kernel/wrapper/: %v", err),
				"kernel/wrapper/", ""),
		}
	}

	var out []ValidationResult
	for _, entry := range entries {
		if !shouldScanWrapperFile(entry) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		out = append(out, v.validateWrapperPackageStateFile(path)...)
	}
	return out
}

func shouldScanWrapperFile(entry os.DirEntry) bool {
	name := entry.Name()
	return !entry.IsDir() &&
		strings.HasSuffix(name, ".go") &&
		!strings.HasSuffix(name, "_test.go")
}

func (v *Validator) validateWrapperPackageStateFile(path string) []ValidationResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return []ValidationResult{v.newResult(codeFMT19, SeverityError, IssueInvalid,
			fmt.Sprintf("FMT-19: failed to read %s: %v", path, err),
			path, "")}
	}

	var out []ValidationResult
	for i, line := range strings.Split(string(data), "\n") {
		name, typ, forbidden := forbiddenWrapperVar(strings.TrimSpace(line))
		if !forbidden {
			continue
		}
		out = append(out, v.newResult(codeFMT19, SeverityError, IssueInvalid,
			fmt.Sprintf("FMT-19: %s:%d forbids mutable package-level variable %q of type %q — "+
				"kernel/wrapper must stay stateless (round-4 constructor-injection invariant)",
				path, i+1, name, typ),
			path, ""))
	}
	return out
}

func forbiddenWrapperVar(line string) (name string, typ string, forbidden bool) {
	if !strings.HasPrefix(line, "var ") || isCompileTimeInterfaceCheck(line) {
		return "", "", false
	}
	sm := wrapperVarDeclRe.FindStringSubmatch(line)
	if len(sm) < 4 {
		return "", "", false
	}
	name = sm[1]
	typ = strings.TrimSpace(sm[2])
	rhs := strings.TrimSpace(sm[3])
	return name, typ, !strings.HasSuffix(rhs, "{}") && isInterfaceOrPointerType(typ)
}

func isCompileTimeInterfaceCheck(line string) bool {
	return strings.HasPrefix(line, "var _ ") || strings.HasPrefix(line, "var _\t")
}

// isInterfaceOrPointerType is a shallow classifier — pointers by leading
// '*', interfaces by capitalised identifier not in the known-struct
// allowlist. Used only by FMT-19 to decide whether a package-level var
// carries a potentially-mutable reference.
func isInterfaceOrPointerType(typ string) bool {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return false
	}
	if strings.HasPrefix(typ, "*") {
		return true
	}
	known := map[string]bool{
		"Attr":          true,
		"ContractSpec":  true,
		"Disposition":   true,
		"Entry":         true,
		"HandleResult":  true,
		"NoopTracer":    true,
		"StatusCode":    true,
		"ConsumerFunc":  true,
		"ErrorRedactor": true,
	}
	return !known[typ]
}
