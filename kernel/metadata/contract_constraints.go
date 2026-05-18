// Package metadata — contract syntactic constraints (single source of truth).
//
// JSON Schemas under kernel/metadata/schemas/ retain literal pattern/enum
// expressions for IDE / editor / standalone tooling consumption. The
// constants below are the authoritative Go-side source: TestSchemaConstants
// MatchSchemaLiterals (under kernel/metadata/schemas) asserts the schema
// literals match these constants byte-for-byte.
//
// Adding a new syntactic constraint:
//
//  1. add a const here;
//  2. update the corresponding schema file with the same literal;
//  3. wire the const into the governance validator (and into typed-identifier
//     boundary types where applicable, e.g. GoIdentifier);
//  4. extend TestSchemaConstantsMatchSchemaLiterals to compare the new pair.
//
// Runtime is single-source: parsers do not validate values; governance is the
// sole gatekeeper, importing the constants here. Schema literals are the
// authoritative form on disk; the test guard prevents drift.
package metadata

import (
	"regexp"
	"strings"

	"github.com/ghbvf/gocell/pkg/scaffoldid"
)

const (
	// AssemblyIDPattern restricts assembly ids to lowercase ASCII letters
	// + digits, ≥2 chars, must start with a letter. Mirrors
	// schemas/assembly.schema.json properties.id.pattern. Reverse-aliased
	// from pkg/scaffoldid.IdentifierPattern — pkg/scaffoldid owns the
	// single-source pattern so typed-identifier funnel (ScaffoldID) and
	// YAML schema validator stay in lock-step. kernel/ may depend on pkg/
	// (architecture rule).
	AssemblyIDPattern = scaffoldid.IdentifierPattern
	// CellIDPattern is identical to AssemblyIDPattern by design — both share
	// the no-dash concatenation convention enforced by FMT-16 / FMT-C1.
	// Same single-source reverse-alias as AssemblyIDPattern.
	CellIDPattern = scaffoldid.IdentifierPattern
	// GoStructNamePattern restricts cell.GoStructName to a Go-exported
	// identifier shape (uppercase first letter, ASCII letters + digits).
	// Mirrors schemas/cell.schema.json properties.goStructName.pattern.
	GoStructNamePattern = `^[A-Z][A-Za-z0-9]*$`
)

// DeployTemplateEnum lists the canonical values accepted for
// assembly.build.deployTemplate. Order matches the schema enum order; do not
// reorder without updating schemas/assembly.schema.json in lockstep.
var DeployTemplateEnum = []string{"k8s", "compose", "binary"}

var goStructNameRe = regexp.MustCompile(GoStructNamePattern)

// MatchAssemblyID reports whether s satisfies AssemblyIDPattern. Forwards
// to pkg/scaffoldid.Match so the regex is compiled exactly once across the
// codebase (CELL-ID-PATTERN-SINGLE-SOURCE-01).
func MatchAssemblyID(s string) bool { return scaffoldid.Match(s) }

// MatchCellID reports whether s satisfies CellIDPattern. Same single-source
// forwarding as MatchAssemblyID.
func MatchCellID(s string) bool { return scaffoldid.Match(s) }

// MatchGoStructName reports whether s satisfies GoStructNamePattern.
func MatchGoStructName(s string) bool { return goStructNameRe.MatchString(s) }

// IsValidMetadataText reports whether value is free of the control characters
// (\n, \r, \x00) that would break inline YAML scalar emission or fabricate
// adjacent YAML fields when interpolated into scaffold templates. All other
// characters — colons, dashes, unicode, punctuation — are accepted at this
// layer; full YAML scalar safety (quoting / escaping) is the responsibility
// of pkg/yamlsafe.Quote at the rendering boundary.
//
// Predicate convention: Match* for pattern-bound checks (regex compliance);
// Is* for semantic free-text checks. Both return bool so callers wrap with
// their own errcode.
//
// Predicate-style API mirrors MatchAssemblyID / MatchCellID — callers
// (kernel scaffold validation, cmd flag validation) compose their own
// errcode wrapping, so no errcode sentinel is introduced here.
//
// Single-source for metadata free-text constraints; eliminates per-caller
// mirror copies (cf. legacy validateAssemblyTextComponent inside
// kernel/assembly, now deleted).
//
// ref: kubernetes/apimachinery pkg/util/validation/validation.go — same
// exported-helper-only convention (pattern unexported, helper exported).
func IsValidMetadataText(value string) bool {
	return !strings.ContainsAny(value, "\n\r\x00")
}

// IsKnownDeployTemplate reports whether s is one of DeployTemplateEnum.
func IsKnownDeployTemplate(s string) bool {
	for _, v := range DeployTemplateEnum {
		if s == v {
			return true
		}
	}
	return false
}
