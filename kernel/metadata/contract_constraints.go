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
	// SagaStepNamePattern restricts saga.steps[].name to a Go-identifier-safe
	// camelCase token (letters + digits, leading letter). It is STRICTER than a
	// generic SafeID because the step name flows through contractgen's
	// goPascalCase into generated Go identifiers (Run<Name> / <Name>Output); a
	// '.', ':' or '/' would emit uncompilable Go. Single source for the saga
	// step-name shape: schemas/contract.schema.json saga.steps[].name.pattern is
	// byte-locked to this const by TestSchemaConstantsMatchSchemaLiterals, and
	// kernel/governance SAGA-CONTRACT-STEP-NAME-VALID-01 compiles it (replacing
	// the prior hand-duplicated literal).
	SagaStepNamePattern = `^[a-zA-Z][a-zA-Z0-9]*$`
	// AssemblyModulePathPattern is the single source for assembly.yaml cell
	// `module` hygiene (the cross-module Go module path, #1086): a non-empty
	// string carrying no rune that could break out of the generated
	// "<module>/cellmodules/<id>" import string literal. The negated character
	// class is exactly the blocklist enforced by metadata.MatchAssemblyModulePath
	// (and consumed by AssemblyCellRef.decodeMapping): the control+space range
	// \x00-\x20, DEL \x7f, double-quote, backtick (\x60), and backslash (\\).
	// Mirrors schemas/assembly.schema.json
	// cells.items.oneOf[1].properties.module.pattern, byte-locked by
	// TestAssemblyCellRefSchemaPatternsMatchConstants. Defense-in-depth on top of
	// the %q-quoting + Go-compiler import-resolvability gate (ADR §D3).
	AssemblyModulePathPattern = "^[^\\x00-\\x20\\x7f\"\\x60\\\\]+$"
)

// DeployTemplateEnum lists the canonical values accepted for
// assembly.build.deployTemplate. Order matches the schema enum order; do not
// reorder without updating schemas/assembly.schema.json in lockstep.
var DeployTemplateEnum = []string{"k8s", "compose", "binary"}

// CapabilityEnum lists the canonical values accepted for cell.requires items.
// Order matches the schema enum order at schemas/cell.schema.json
// properties.requires.items.enum; do not reorder without updating the schema
// in lockstep. TestSchemaConstantsMatchSchemaLiterals (kernel/metadata/schemas)
// asserts byte-level parity between this slice and the schema enum array. The
// assembly's provisioned set is the derived union of its cells' requires
// (Design Y, #855) — there is no assembly-level capabilities enum.
var CapabilityEnum = []string{"postgres", "redis", "rabbitmq"}

var goStructNameRe = regexp.MustCompile(GoStructNamePattern)

var assemblyModulePathRe = regexp.MustCompile(AssemblyModulePathPattern)

// MatchAssemblyID reports whether s satisfies AssemblyIDPattern. Forwards
// to pkg/scaffoldid.Match so the regex is compiled exactly once across the
// codebase (CELL-ID-PATTERN-SINGLE-SOURCE-01).
func MatchAssemblyID(s string) bool { return scaffoldid.Match(s) }

// MatchCellID reports whether s satisfies CellIDPattern. Same single-source
// forwarding as MatchAssemblyID.
func MatchCellID(s string) bool { return scaffoldid.Match(s) }

// MatchGoStructName reports whether s satisfies GoStructNamePattern.
func MatchGoStructName(s string) bool { return goStructNameRe.MatchString(s) }

// MatchAssemblyModulePath reports whether s satisfies AssemblyModulePathPattern:
// a non-empty assembly cell `module` path free of runes that could break out of
// the generated cellmodules import string literal.
func MatchAssemblyModulePath(s string) bool { return assemblyModulePathRe.MatchString(s) }

// IsValidMetadataText reports whether value is free of the control characters
// (\n, \r, \x00, \t) that would break inline YAML scalar emission or fabricate
// adjacent YAML fields when interpolated into scaffold templates. All other
// characters — colons, dashes, unicode, punctuation — are accepted at this
// layer; full YAML scalar safety (quoting / escaping) is the responsibility
// of pkg/yamlsafe.Quote at the rendering boundary.
//
// Tab (\t) is included because YAML 1.2 §5.1 allows tab in plain scalars, so
// yamlsafe.Quote would not add quotes around a tab-containing value, letting
// the tab pass through to the generated YAML unchanged. This matches the
// validateScaffoldID / validateScaffoldText tab-hardening (#8).
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
	return !strings.ContainsAny(value, "\n\r\x00\t")
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

// IsKnownCapability reports whether s is one of CapabilityEnum.
func IsKnownCapability(s string) bool {
	for _, v := range CapabilityEnum {
		if s == v {
			return true
		}
	}
	return false
}

// GRPCStreamingTypeEnum lists the canonical values accepted for
// endpoints.grpc.streamingType WHEN THE FIELD IS PRESENT. Order matches the
// schema enum order at schemas/contract.schema.json
// (...grpc.properties.streamingType.enum); do not reorder without updating the
// schema in lockstep. TestSchemaConstantsMatchSchemaLiterals
// (kernel/metadata/schemas) asserts byte-level parity. This slice is the single
// source consumed by both governance FMT-37 and runtime
// kernel/contractspec.validateGRPC, so schema, governance, and runtime never
// drift on the accepted streaming patterns.
//
// An empty/omitted streamingType is treated as unary by downstream codegen
// (PR 2+), so it is intentionally NOT a member — callers guard the empty case
// separately before calling IsKnownGRPCStreamingType.
var GRPCStreamingTypeEnum = []string{"unary", "server-stream", "client-stream", "bidi"}

// GRPCProtoPathPrefix is the required leading path component for
// endpoints.grpc.proto: every .proto reference must be a contracts-relative
// path rooted under contracts/grpc/. The schema literal at
// schemas/contract.schema.json (...grpc.properties.proto.pattern) is kept equal
// to "^"+GRPCProtoPathPrefix by TestSchemaConstantsMatchSchemaLiterals (the
// prefix contains no regex metacharacters, so the anchored pattern is a literal
// prefix match). Single source shared by governance FMT-37 and runtime
// kernel/contractspec.validateGRPC.
const GRPCProtoPathPrefix = "contracts/grpc/"

// IsKnownGRPCStreamingType reports whether s is one of GRPCStreamingTypeEnum.
// The empty string is NOT a member: callers treat empty as the unary default
// and must guard it before calling (mirrors the schema, where streamingType is
// optional but constrained to the enum once present).
func IsKnownGRPCStreamingType(s string) bool {
	for _, v := range GRPCStreamingTypeEnum {
		if s == v {
			return true
		}
	}
	return false
}
