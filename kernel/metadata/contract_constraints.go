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
	"fmt"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/ghbvf/gocell/kernel/cellvocab"
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

// ValidateGRPCProtoPath validates an endpoints.grpc.proto path field. It applies
// five guards in order:
//
//  1. non-empty — a proto field is required on every grpc contract.
//  2. must be rooted under GRPCProtoPathPrefix ("contracts/grpc/") — prevents
//     referencing protos outside the governed contracts tree.
//  3. no control rune — a control character would corrupt generated doc comments
//     and file path handling.
//  4. filepath.IsLocal — HasPrefix alone does not stop a traversal such as
//     "contracts/grpc/../../../etc/x" escaping the repo root on os.ReadFile.
//     This is the single-source guard shared by contractgen and governance FMT-37
//     (governance never runs contractgen, so without this shared function FMT-37
//     missed the IsLocal check — PR #1601 finding #2 / #3).
//  5. subtree escape after lexical clean — a path like "contracts/grpc/../http/x.proto"
//     passes guard 2 (string HasPrefix) and guard 4 (stays under repo root) but
//     cleans to "contracts/http/x.proto", which is OUTSIDE the contracts/grpc/
//     subtree. Guard 5 lexically cleans the path and re-asserts the prefix so that
//     intra-subtree ".." sequences (e.g. "contracts/grpc/device/../device/x.proto"
//     → "contracts/grpc/device/x.proto") are accepted while cross-subtree escapes
//     are rejected.
//
// This is a pure lexical guard — symlink resolution is intentionally out of scope.
// The contracts/grpc/ tree is repo-controlled and the validator must remain FS-free
// so governance can run it without reading the proto.
//
// The returned error carries a plain, path-focused message. Callers are expected
// to wrap it with their own contract-identity context:
//
//	if err := metadata.ValidateGRPCProtoPath(proto); err != nil {
//	    return fmt.Errorf("contract %q: %w", id, err)
//	}
func ValidateGRPCProtoPath(proto string) error {
	if proto == "" {
		return fmt.Errorf("grpc block requires proto: endpoints.grpc.proto must be a non-empty path")
	}
	if !strings.HasPrefix(proto, GRPCProtoPathPrefix) {
		return fmt.Errorf("grpc proto %q must be rooted under %q", proto, GRPCProtoPathPrefix)
	}
	if i := strings.IndexFunc(proto, unicode.IsControl); i >= 0 {
		return fmt.Errorf("grpc proto path %q contains a control character at byte %d", proto, i)
	}
	if !filepath.IsLocal(filepath.FromSlash(proto)) {
		return fmt.Errorf("grpc proto %q must be a local path (no traversal, no absolute)", proto)
	}
	// Guard 5: lexically clean and re-assert the subtree prefix. A path such as
	// "contracts/grpc/../http/x.proto" passes HasPrefix (guard 2) and IsLocal
	// (guard 4) but escapes the contracts/grpc/ subtree once cleaned.
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(proto)))
	if !strings.HasPrefix(clean, GRPCProtoPathPrefix) {
		return fmt.Errorf("grpc proto %q escapes the %q subtree (cleans to %q)", proto, GRPCProtoPathPrefix, clean)
	}
	return nil
}

// GRPCServiceGoName extracts the proto service's simple name (last dotted
// segment of the fully-qualified service name, e.g.
// "device.command.v1.DeviceCommandService" → "DeviceCommandService") and
// validates that it is an exported Go identifier.
//
// The protoc-gen-go-grpc generator derives Register<Name>Server from the
// service's Go name, which for valid proto service declarations equals the
// last FQN segment. An unexported or syntactically invalid segment would cause
// cellgen to emit a lowercase RegisterfooServer selector that the Go compiler
// cannot resolve. This function is the single source for that derivation:
// both cellgen (RegisterFunc derivation) and contractgen (service-name
// validation) must call it rather than duplicating the logic.
//
// Returns an error when the name is empty, not a valid Go identifier, or not
// exported (uppercase first letter). It is intentionally FS-free so it can
// run inside governance and codegen without reading the proto file.
func GRPCServiceGoName(service string) (string, error) {
	name := service
	if i := strings.LastIndex(service, "."); i >= 0 {
		name = service[i+1:]
	}
	if name == "" {
		return "", fmt.Errorf("grpc service %q: simple name is empty", service)
	}
	if !token.IsIdentifier(name) {
		return "", fmt.Errorf("grpc service %q: simple name %q is not a valid Go identifier", service, name)
	}
	if !token.IsExported(name) {
		return "", fmt.Errorf("grpc service %q: simple name %q is not exported (must start with uppercase letter)", service, name)
	}
	return name, nil
}

// TransportEnum lists the canonical wire transports accepted for contract.yaml
// `transports[]`. It is the string projection of cellvocab.AllTransports() (the
// typed single source); ordering matches that slice and, in lockstep, the
// schemas/contract.schema.json transports enum (byte-locked by
// TestSchemaConstantsMatchSchemaLiterals#transportEnum). Membership is enforced
// at the DECLARATION layer only — governance FMT-39 (via IsKnownTransport) plus
// the schema enum. Runtime kernel/contractspec.Validate deliberately does NOT
// re-check transport membership (a production ContractSpec.Transport is trusted
// as codegen/contractbuild-derived; declaration-layer enforcement, see ADR #1389
// D6), so schema and governance — not the runtime value type — own this set.
var TransportEnum = transportEnumStrings()

func transportEnumStrings() []string {
	all := cellvocab.AllTransports()
	out := make([]string, len(all))
	for i, t := range all {
		out[i] = string(t)
	}
	return out
}

// IsKnownTransport reports whether s is one of TransportEnum (the closed set of
// wire transports GoCell sanctions). The empty string is NOT a member; callers
// that allow an omitted transport must guard the empty case separately. The
// parser defaults an OMITTED contract.yaml `transports:` per kind; an explicitly
// present-but-empty declaration (null / []) is left empty on purpose so
// governance FMT-39's non-empty guard flags it rather than silently defaulting.
func IsKnownTransport(s string) bool {
	for _, v := range TransportEnum {
		if s == v {
			return true
		}
	}
	return false
}
