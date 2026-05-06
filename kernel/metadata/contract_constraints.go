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

import "regexp"

const (
	// AssemblyIDPattern restricts assembly ids to lowercase ASCII letters
	// + digits, ≥2 chars, must start with a letter. Mirrors
	// schemas/assembly.schema.json properties.id.pattern.
	AssemblyIDPattern = `^[a-z][a-z0-9]+$`
	// GoStructNamePattern restricts cell.GoStructName to a Go-exported
	// identifier shape (uppercase first letter, ASCII letters + digits).
	// Mirrors schemas/cell.schema.json properties.goStructName.pattern.
	//
	// Note: cell.schema.json id.pattern is intentionally more permissive
	// than the governance FMT-C1 strict-mode rule; that pre-existing
	// schema/governance gap is out of scope for this PR (see review
	// docs/reviews/202605070218-pr404-second-wave-review.md §R-meta).
	GoStructNamePattern = `^[A-Z][A-Za-z0-9]*$`
)

// DeployTemplateEnum lists the canonical values accepted for
// assembly.build.deployTemplate. Order matches the schema enum order; do not
// reorder without updating schemas/assembly.schema.json in lockstep.
var DeployTemplateEnum = []string{"k8s", "compose", "binary"}

var (
	assemblyIDRe   = regexp.MustCompile(AssemblyIDPattern)
	goStructNameRe = regexp.MustCompile(GoStructNamePattern)
)

// MatchAssemblyID reports whether s satisfies AssemblyIDPattern.
func MatchAssemblyID(s string) bool { return assemblyIDRe.MatchString(s) }

// MatchGoStructName reports whether s satisfies GoStructNamePattern.
func MatchGoStructName(s string) bool { return goStructNameRe.MatchString(s) }

// IsKnownDeployTemplate reports whether s is one of DeployTemplateEnum.
func IsKnownDeployTemplate(s string) bool {
	for _, v := range DeployTemplateEnum {
		if s == v {
			return true
		}
	}
	return false
}
