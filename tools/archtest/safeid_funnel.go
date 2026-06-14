package archtest

// safeid_funnel.go — importable SAFEID-WIREMESSAGE-USAGE-01 +
// SAFEID-UPSTREAM-FUNNEL-HARD-01 rule logic (#1632 M3).
//
// This is the non-test home of the scanner logic so it can be compiled and run
// by an external Cell repository (Go never compiles a dependency's _test.go,
// so rule logic external repos must run cannot live in a _test.go file).
// GoCell's own Test* functions in safeid_funnel_test.go call the same Check*
// — single source, no parallel rule body.
//
// safeid_funnel.go — locks the wire-decode trust boundary for the
// kernel/outbox envelope: every ID-shaped field uses idutil.SafeID
// (downstream), and the wire envelope struct itself is package-private
// (upstream). Together these form a closed Hard funnel: SafeID's
// UnmarshalJSON enforces IsSafeID + length cap at json.Unmarshal time, and
// the unexported `wireMessage` struct makes cross-package decode/construction
// compile-time impossible — only outbox.MarshalEnvelope / outbox.UnmarshalEnvelope
// can move bytes ↔ envelope.
//
//   - INVARIANT: SAFEID-WIREMESSAGE-USAGE-01
//   - INVARIANT: SAFEID-UPSTREAM-FUNNEL-HARD-01
//
// AI-robust rating: closed Hard funnel (charter §"Funnel 双向锁评级").
//
//	Downstream Hard (SAFEID-WIREMESSAGE-USAGE-01) — string-typed concept
//	funnel (charter §"Hard 范本目录"). Form uniqueness:
//	  (a) field declared on wireMessage / ObservabilityMetadata in
//	      kernel/outbox, AND
//	  (b) field type resolves via go/types to
//	      github.com/ghbvf/gocell/framework/pkg/idutil.SafeID.
//	Any deviation (revert to `string`, alias to another named string type,
//	rename) fails archtest in CI. Type-system layer: json.Unmarshal cannot
//	decode an unsafe value into a SafeID field — there is no parser-level
//	shape that bypasses SafeID.UnmarshalJSON when the field is typed SafeID.
//
//	Upstream Hard (SAFEID-UPSTREAM-FUNNEL-HARD-01) — sealed construction
//	(charter §"Hard 范本目录"). Form uniqueness:
//	  (a) kernel/outbox declares `wireMessage` (lowercase, unexported), AND
//	  (b) kernel/outbox does NOT declare an exported `WireMessage` (struct,
//	      alias, or named type).
//	Go package-level visibility = type-system Hard: any external attempt at
//	`outbox.WireMessage{...}` / `outbox.wireMessage{...}` /
//	`json.Unmarshal(bytes, &outbox.WireMessage{})` is a compile-time error.
//	The archtest is the regression guard against a future PR re-exporting
//	the type. ref: go-kratos/kratos `transport/grpc/codec.go` (zero-size
//	unexported codec struct — closest industry equivalent for an unexported
//	codec gating wire decode); etcd-io/etcd `server/wal/wal.go` `WAL`
//	(sealed handle factory, distinct from wire-level `wal.Record`). Watermill
//	`message.Message` is NOT a Hard equivalent — its UUID/Metadata/Payload
//	are exported, only ack channels are sealed.
//
// Scanning tool: typeseval.SharedResolver via Run(t, Typed(...)) + go/types struct
// field inspection (kernel/outbox package scope, no fixture). Selected per
// ai-robust.md §"载体决策原则" — type information required (resolve named
// type to package + name; resolve scope.Lookup to detect unexported/exported
// re-export).
//
// Already-closed by Go type system (not archtest scope):
//
//  1. Reflection-driven field tag rewrites or unsafe pointer aliasing
//     that masquerades string as SafeID at runtime: out of scope; this
//     archtest verifies the source-level field type declaration. Reflection
//     against unexported field internals is not a supported wire path.
//     Go's reflect package cannot write to unexported struct fields from
//     outside the package — blocked at the language level, not by this archtest.
//
//  2. Generic helper `func decodeAny[T any](bytes []byte) T` that bypasses
//     the unexported type via type parameter: Go's visibility rules apply
//     to type arguments across packages — `outbox.wireMessage` cannot be
//     written as a type argument from outside the package, so this path
//     is compile-time blocked without any archtest involvement.
//
// Tool blind spots (forms Run(t, Typed(...)) + go/types cannot see):
//
//  1. Type aliases (`type Foo = idutil.SafeID`): alias resolution flattens
//     to the same TypeName via types.Named.Obj(), so the underlying check
//     succeeds. Documented for completeness.
//
//  2. New struct introduced in kernel/outbox that re-implements the
//     envelope wire shape under a different name (e.g. WireMessageV2):
//     captured by SAFEID-WIREMESSAGE-USAGE-01/NewWireStruct below
//     (scans for `Unmarshaler` methods on string-typed fields with
//     id/event/topic/aggregate names).
//
//  3. Re-export of `WireMessage` via type alias or parallel definition
//     (`type WireMessage = wireMessage` / `type WireMessage struct {...}`):
//     captured by SAFEID-UPSTREAM-FUNNEL-HARD-01 via go/types Lookup
//     ("WireMessage" must NOT exist), and by the
//     SAFEID-UPSTREAM-FUNNEL-HARD-01/NoReExport reverse self-test that
//     AST-scans for the literal `type WireMessage` declaration.
//
//  4. Package-internal new decode path (e.g., `_helpers_test.go` adding
//     `json.Unmarshal` on `wireMessage`): not in INVARIANT scope — the
//     downstream SafeID field-type Hard still routes every field decode
//     through `SafeID.UnmarshalJSON`, and package-internal code is the
//     trusted authority for envelope semantics.
//
// Carve-outs: ObservabilityMetadata.TraceParent stays `string` — it's a
// fixed-length W3C traceparent with its own format validator
// (validTraceParent), not an IsSafeID member set. ObservabilityMetadata
// itself stays exported because adapters/postgres/outbox_writer.go uses
// it cross-package for the producer-side marshaling helper.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green externally.

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"
)

const (
	safeIDPkgPath          = PlatformFrameworkModulePath + "/pkg/idutil"
	safeIDTypeName         = "SafeID"
	outboxPkgPath          = PlatformFrameworkModulePath + "/kernel/outbox"
	wireMessageType        = "wireMessage"
	wireMessageExportedOld = "WireMessage"
	observabilityType      = "ObservabilityMetadata"
	principalType          = "PrincipalMetadata"
)

// safeIDExemptFields lists, per guarded type, the exported fields that are
// legitimately NOT typed idutil.SafeID. The reverse direction is the funnel
// invariant: **every other exported field on wireMessage / ObservabilityMetadata
// MUST be SafeID**. Adding a new field to either type defaults to
// "SafeID required" — exempting it requires explicit reviewer judgment by
// adding an entry here with a one-line rationale.
//
// Deny-by-default mirrors Kubernetes API server's runtime.Decode pattern:
// new fields cannot silently slip past validation by being absent from a
// required-list; they are caught by reflective field walking that requires
// either a typed wrapper or an explicit carve-out.
var safeIDExemptFields = map[string]map[string]string{
	wireMessageType: {
		"SchemaVersion": "envelope version literal, not ID-shaped (e.g. \"v1\")",
		"Payload":       "json.RawMessage business payload bytes",
		"Metadata":      "map[string]string business metadata (validated separately by validateMetadata)",
		"Observability": "nested ObservabilityMetadata struct, walked recursively",
		"Principal":     "nested PrincipalMetadata struct, walked recursively (issue #1229)",
		"OccurredAt":    "time.Time producer-domain event time, not ID-shaped (issue #1229)",
		"CreatedAt":     "time.Time, not ID-shaped",
	},
	observabilityType: {
		"TraceParent": "W3C 55-byte fixed-format string with its own validator (validTraceParent)",
	},
	// PrincipalMetadata's four fields (ActorID/SubjectID/TenantID/SessionID) are
	// all idutil.SafeID — no carve-out needed; registering the type here makes
	// the walk verify each field is SafeID-typed (deny-by-default), the same way
	// ObservabilityMetadata is walked. (issue #1229)
	principalType: {},
}

// safeIDBlindSpotAllowlist lists OTHER named structs in kernel/outbox that
// legitimately hold string-typed ID-shaped fields. Entry is the in-memory
// representation populated AFTER UnmarshalEnvelope validates wire input;
// downstream code (slog.String, PG columns) consumes plain string. Adding
// a struct here requires reviewer judgment that it is NOT a wire-decode
// entry point.
var safeIDBlindSpotAllowlist = map[string]struct{}{
	"Entry": {},
	// EntryScan is the storage-adapter reconstruction funnel (issue #1229): it
	// carries exported string ID-shaped scan-target fields populated from DB
	// columns, then ToEntry() runs full Entry.Validate (SafeID enforcement). It
	// has NO wire role — wireMessage remains the only json.Unmarshal envelope —
	// so it is not a parallel wire decode entry point. Same category as Entry
	// (post-validation in-memory representation), reviewer-judged not-a-wire-decoder.
	"EntryScan": {},
}

// wireMessageCanonicalFields is the canonical field set wireMessage MUST
// carry. SAFEID-UPSTREAM-FUNNEL-HARD-01 fails if a future PR drops or
// renames any of these — a silent semantic regression (e.g., dropping
// SchemaVersion to "simplify" the envelope) is caught here.
var wireMessageCanonicalFields = []string{
	"SchemaVersion",
	"ID",
	"AggregateID",
	"AggregateType",
	"EventType",
	"Topic",
	"Payload",
	"Metadata",
	"Observability",
	"Principal",
	"OccurredAt",
	"CreatedAt",
}

// safeIDWireFieldNames is the closed set of canonical ID-shaped field names
// the BlindSpot detector looks for in other structs. Extending this set
// requires reviewer judgment; it is intentionally narrow (canonical names
// only) to keep the heuristic conservative — false-positives are cheap
// (just add the struct to safeIDBlindSpotAllowlist with rationale).
var safeIDWireFieldNames = map[string]struct{}{
	"ID":            {},
	"AggregateID":   {},
	"AggregateType": {},
	"EventType":     {},
	"Topic":         {},
	"TraceID":       {},
	"RequestID":     {},
	"CorrelationID": {},
}

// CheckSafeIDWireMessageUsage01 runs SAFEID-WIREMESSAGE-USAGE-01 over the
// running module and returns its diagnostics. It is the importable rule body;
// GoCell's TestSAFEIDWireMessageUsage01 calls it directly — single source,
// no parallel rule body.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green
// externally.
func CheckSafeIDWireMessageUsage01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./framework/kernel/outbox/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != outboxPkgPath {
				return nil
			}
			diags = append(diags, checkSafeIDFields(p.Pkg)...)
			return nil
		})

	return diags
}

// CheckSafeIDUpstreamFunnelHard01 runs SAFEID-UPSTREAM-FUNNEL-HARD-01 over the
// running module and returns its diagnostics. It is the importable rule body;
// GoCell's TestSAFEIDUpstreamFunnelHard01 calls it directly — single source,
// no parallel rule body.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green
// externally.
func CheckSafeIDUpstreamFunnelHard01(t *testing.T, _ ConfigForExternalCell) []Diagnostic { //nolint:gocognit,cyclop,funlen,lll // R2-approved: moved from _test.go (was waived there); six sub-checks are linear, not structurally refactorable
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./framework/kernel/outbox/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != outboxPkgPath {
				return nil
			}
			scope := p.Pkg.Scope()

			obj := scope.Lookup(wireMessageType)
			if obj == nil {
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf(
						"SAFEID-UPSTREAM-FUNNEL-HARD-01: %s.%s (unexported wire envelope) not found — funnel seal broken",
						p.Pkg.Path(), wireMessageType,
					),
				})
				return nil
			}
			if obj.Exported() {
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf(
						"SAFEID-UPSTREAM-FUNNEL-HARD-01: %s.%s is exported — must remain package-private to keep Go-visibility upstream Hard seal",
						p.Pkg.Path(), wireMessageType,
					),
				})
			}

			if exported := scope.Lookup(wireMessageExportedOld); exported != nil {
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf(
						"SAFEID-UPSTREAM-FUNNEL-HARD-01: %s.%s (exported) exists — "+
							"re-exporting the envelope as struct or alias breaks the Go-visibility "+
							"upstream Hard seal; keep envelope I/O sealed behind outbox.MarshalEnvelope / "+
							"outbox.UnmarshalEnvelope",
						p.Pkg.Path(), wireMessageExportedOld,
					),
				})
			}

			named, ok := obj.Type().(*types.Named)
			if !ok {
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf(
						"SAFEID-UPSTREAM-FUNNEL-HARD-01: %s.%s is not a named type",
						p.Pkg.Path(), wireMessageType,
					),
				})
				return nil
			}
			strct, ok := named.Underlying().(*types.Struct)
			if !ok {
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf(
						"SAFEID-UPSTREAM-FUNNEL-HARD-01: %s.%s is not a struct",
						p.Pkg.Path(), wireMessageType,
					),
				})
				return nil
			}
			present := make(map[string]struct{}, strct.NumFields())
			for i := 0; i < strct.NumFields(); i++ {
				present[strct.Field(i).Name()] = struct{}{}
			}
			for _, fieldName := range wireMessageCanonicalFields {
				if _, ok := present[fieldName]; !ok {
					diags = append(diags, Diagnostic{
						Message: fmt.Sprintf(
							"SAFEID-UPSTREAM-FUNNEL-HARD-01: %s.%s missing canonical envelope field %q — silent rename or semantic regression",
							p.Pkg.Path(), wireMessageType, fieldName,
						),
					})
				}
			}

			wireType := obj.Type()
			wireUnderlying := wireType.Underlying()
			canonicalSet := make(map[string]struct{}, len(wireMessageCanonicalFields))
			for _, fn := range wireMessageCanonicalFields {
				canonicalSet[fn] = struct{}{}
			}
			for _, name := range scope.Names() {
				if name == wireMessageType {
					continue
				}

				if _, allowed := safeIDBlindSpotAllowlist[name]; allowed {
					continue
				}
				if _, exempt := safeIDExemptFields[name]; exempt {
					continue
				}
				candidate := scope.Lookup(name)
				if candidate == nil || !candidate.Exported() {
					continue
				}

				resolved := types.Unalias(candidate.Type())
				if resolved == wireType {
					diags = append(diags, Diagnostic{
						Message: fmt.Sprintf(
							"SAFEID-UPSTREAM-FUNNEL-HARD-01/AliasReExport: "+
								"%s.%s is an exported alias of %s — "+
								"`type %s = %s` makes the unexported envelope cross-package constructible "+
								"and json.Unmarshal-targetable, breaking the upstream Hard seal",
							p.Pkg.Path(), name, wireMessageType,
							name, wireMessageType,
						),
					})
					continue
				}

				candNamed, ok := resolved.(*types.Named)
				if !ok {
					continue
				}
				candStrct, ok := candNamed.Underlying().(*types.Struct)
				if !ok {
					continue
				}

				if candNamed.Underlying() == wireUnderlying {
					diags = append(diags, Diagnostic{
						Message: fmt.Sprintf(
							"SAFEID-UPSTREAM-FUNNEL-HARD-01/ReShapeReExport: "+
								"%s.%s is an exported defined type sharing %s's underlying struct "+
								"(`type %s %s`) — same constructibility leak as an alias",
							p.Pkg.Path(), name, wireMessageType,
							name, wireMessageType,
						),
					})
					continue
				}

				hasSchemaVersion := false
				canonicalMatchCount := 0
				for i := 0; i < candStrct.NumFields(); i++ {
					fn := candStrct.Field(i).Name()
					if fn == "SchemaVersion" {
						hasSchemaVersion = true
					}
					if _, ok := canonicalSet[fn]; ok {
						canonicalMatchCount++
					}
				}
				const reShapeMatchThreshold = 7
				if hasSchemaVersion && canonicalMatchCount >= reShapeMatchThreshold {
					diags = append(diags, Diagnostic{
						Message: fmt.Sprintf(
							"SAFEID-UPSTREAM-FUNNEL-HARD-01/ReShapeReExport: "+
								"%s.%s is an exported struct with SchemaVersion + %d/%d canonical wireMessage fields "+
								"— parallel wire envelope re-introduced under a different name; "+
								"either rename to extend wireMessage or add to safeIDExemptFields / safeIDBlindSpotAllowlist with rationale",
							p.Pkg.Path(), name,
							canonicalMatchCount, len(wireMessageCanonicalFields),
						),
					})
				}
			}
			return nil
		})

	return diags
}

// checkSafeIDFields walks the guarded types and emits a diagnostic for every
// exported field that is neither SafeID-typed nor explicitly exempted.
func checkSafeIDFields(pkg *types.Package) []Diagnostic { //nolint:gocognit,lll // R2-approved: moved from _test.go (was waived there); field-walk is linear
	var diags []Diagnostic

	// Deterministic iteration: sort type names so failure messages are stable.
	typeNames := make([]string, 0, len(safeIDExemptFields))
	for tn := range safeIDExemptFields {
		typeNames = append(typeNames, tn)
	}
	sort.Strings(typeNames)

	for _, typeName := range typeNames {
		exempt := safeIDExemptFields[typeName]
		obj := pkg.Scope().Lookup(typeName)
		if obj == nil {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf("type %q not found in %s", typeName, pkg.Path()),
			})
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf("%s.%s is not a named type", pkg.Path(), typeName),
			})
			continue
		}
		strct, ok := named.Underlying().(*types.Struct)
		if !ok {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf("%s.%s is not a struct", pkg.Path(), typeName),
			})
			continue
		}

		for i := 0; i < strct.NumFields(); i++ {
			f := strct.Field(i)
			// wireMessage is unexported (Hard upstream seal), but json.Unmarshal can
			// only populate EXPORTED fields — so the SafeID funnel invariant applies
			// only to exported fields. Skip unexported fields: they cannot carry wire
			// data so can't bypass SafeID.UnmarshalJSON.
			if !f.Exported() {
				continue
			}
			if _, exempted := exempt[f.Name()]; exempted {
				continue
			}
			if isSafeIDType(f.Type()) {
				continue
			}
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"SAFEID-WIREMESSAGE-USAGE-01: %s.%s.%s has type %s; "+
						"must be idutil.SafeID or registered in safeIDExemptFields with rationale "+
						"(deny-by-default — new fields default to SafeID required to keep CWE-117 funnel intact)",
					pkg.Path(), typeName, f.Name(), f.Type().String(),
				),
			})
		}
	}
	return diags
}

// isSafeIDType reports whether t resolves to github.com/ghbvf/gocell/framework/pkg/idutil.SafeID.
// Aliases flatten to the same TypeName, so `type Foo = idutil.SafeID` is
// treated as SafeID.
func isSafeIDType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	tn := named.Obj()
	if tn == nil || tn.Pkg() == nil {
		return false
	}
	return tn.Pkg().Path() == safeIDPkgPath && tn.Name() == safeIDTypeName
}

// checkSafeIDBlindSpotNewWireStruct scans all structs in pkg (other than the
// guarded types and known allowlist) for ≥2 string-typed canonical
// ID-shaped fields, flagging potential parallel wire envelopes.
func checkSafeIDBlindSpotNewWireStruct(pkg *types.Package) []Diagnostic { //nolint:gocognit,lll // R2-approved: moved from _test.go (was waived there); struct-scan is linear
	var diags []Diagnostic
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if name == wireMessageType || name == observabilityType {
			continue
		}
		if _, ok := safeIDBlindSpotAllowlist[name]; ok {
			continue
		}
		obj := scope.Lookup(name)
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		strct, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}

		suspectCount := 0
		suspectFields := []string{}
		for i := 0; i < strct.NumFields(); i++ {
			f := strct.Field(i)
			if !f.Exported() {
				continue
			}
			if _, ok := safeIDWireFieldNames[f.Name()]; !ok {
				continue
			}
			if isSafeIDType(f.Type()) {
				continue
			}
			if basic, ok := f.Type().(*types.Basic); ok && basic.Kind() == types.String {
				suspectFields = append(suspectFields, f.Name())
				suspectCount++
			}
		}
		if suspectCount >= 2 {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"SAFEID-WIREMESSAGE-USAGE-01/BlindSpot: %s.%s looks like a parallel wire envelope "+
						"(string-typed fields %s) — change the fields to idutil.SafeID or register the struct in safeIDBlindSpotAllowlist",
					pkg.Path(), name, strings.Join(suspectFields, ", "),
				),
			})
		}
	}
	return diags
}
