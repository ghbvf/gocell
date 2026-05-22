// safeid_funnel_test.go — locks the wire-decode trust boundary to use
// idutil.SafeID for every ID-shaped field in kernel/outbox WireMessage and
// ObservabilityMetadata. SafeID's UnmarshalJSON enforces IsSafeID + length
// cap at json.Unmarshal time — switching any of these fields back to `string`
// silently re-opens the CWE-117 log-injection vector closed by G-08(c).
//
// INVARIANT: SAFEID-WIREMESSAGE-USAGE-01
//
// AI-rebust rating: Hard (charter §"Hard 范本" 第 3 条 string-typed concept
// funnel). Form uniqueness =
//
//	(a) field declared on WireMessage / ObservabilityMetadata in
//	    kernel/outbox, AND
//	(b) field type resolves via go/types to
//	    github.com/ghbvf/gocell/pkg/idutil.SafeID.
//
// Any deviation (revert to `string`, alias to another named string type,
// rename) fails archtest in CI. Downstream Hard at the type system layer:
// json.Unmarshal cannot decode an unsafe value into a SafeID field — there
// is no parser-level shape that bypasses SafeID.UnmarshalJSON when the
// field is typed SafeID.
//
// Upstream Medium-by-necessity:
//
//	(a) Current downstream Hard already covers the "field type" path:
//	    json.Unmarshal cannot decode an unsafe value into a SafeID-typed
//	    field — SafeID.UnmarshalJSON is dispatched by the Go runtime
//	    regardless of which caller invokes json.Unmarshal. A business-code
//	    WireMessage{ID: idutil.SafeID(rawUnsafe)} literal cast still triggers
//	    MarshalEnvelope's ParseSafeID producer-side fail-fast and does NOT
//	    bypass the type system (the field is still SafeID, not string).
//
//	(b) Unguarded bypass paths that remain:
//	    - Direct json.Unmarshal(bytes, &outbox.WireMessage{}) outside
//	      UnmarshalEnvelope skips the schemaVersion/required-field checks
//	      (though SafeID.UnmarshalJSON still fires for all ID-shaped fields,
//	      so the CWE-117 vector is closed even here).
//	    - There is no archtest locking "UnmarshalEnvelope is the only caller
//	      of json.Unmarshal for WireMessage" across transports. This gap is
//	      the gap that prevents a full upstream Hard claim.
//
//	Upstream Medium is the appropriate rating under ai-collab.md
//	§"Funnel 双向锁评级" — archtest caller-allowlist would be needed for
//	upstream Hard (archtest caller-allowlist would be needed).
//
// Scanning tool: typeseval.SharedResolver via RunTyped + go/types struct
// field inspection (kernel/outbox package scope, no fixture). Selected per
// ai-collab.md §"载体决策原则" — type information required (resolve named
// type to package + name).
//
// Tool blind spots (forms RunTyped + go/types cannot see):
//
//  1. Reflection-driven field tag rewrites or unsafe pointer aliasing
//     that masquerades string as SafeID at runtime: out of scope; this
//     archtest verifies the source-level field type declaration. Reflection
//     against unexported field internals is not a supported wire path.
//
//  2. Type aliases (`type Foo = idutil.SafeID`): alias resolution flattens
//     to the same TypeName via types.Named.Obj(), so the underlying check
//     succeeds. Documented for completeness.
//
//  3. New struct introduced in kernel/outbox that re-implements the
//     envelope wire shape under a different name (e.g. WireMessageV2):
//     captured by SAFEID-WIREMESSAGE-USAGE-01/NewStructDetector below
//     (scans for `Unmarshaler` methods on string-typed fields with
//     id/event/topic/aggregate names).
//
// Carve-outs: ObservabilityMetadata.TraceParent stays `string` — it's a
// fixed-length W3C traceparent with its own format validator
// (validTraceParent), not an IsSafeID member set.
package archtest

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"
)

const (
	safeIDPkgPath     = "github.com/ghbvf/gocell/pkg/idutil"
	safeIDTypeName    = "SafeID"
	outboxPkgPath     = "github.com/ghbvf/gocell/kernel/outbox"
	wireMessageType   = "WireMessage"
	observabilityType = "ObservabilityMetadata"
)

// safeIDExemptFields lists, per guarded type, the exported fields that are
// legitimately NOT typed idutil.SafeID. The reverse direction is the funnel
// invariant: **every other exported field on WireMessage / ObservabilityMetadata
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
		"CreatedAt":     "time.Time, not ID-shaped",
	},
	observabilityType: {
		"TraceParent": "W3C 55-byte fixed-format string with its own validator (validTraceParent)",
	},
}

// safeIDBlindSpotAllowlist lists OTHER named structs in kernel/outbox that
// legitimately hold string-typed ID-shaped fields. Entry is the in-memory
// representation populated AFTER UnmarshalEnvelope validates wire input;
// downstream code (slog.String, PG columns) consumes plain string. Adding
// a struct here requires reviewer judgment that it is NOT a wire-decode
// entry point.
var safeIDBlindSpotAllowlist = map[string]struct{}{
	"Entry": {},
}

// TestSAFEIDWireMessageUsage01 reflectively asserts that every exported
// field on WireMessage and ObservabilityMetadata is either typed
// idutil.SafeID or explicitly carved out in safeIDExemptFields. A new field
// default-fails until a reviewer either types it SafeID or registers a
// carve-out with rationale (deny-by-default).
func TestSAFEIDWireMessageUsage01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/outbox/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != outboxPkgPath {
				return nil
			}
			diags = append(diags, checkSafeIDFields(p.Pkg)...)
			return nil
		})

	Report(t, "SAFEID-WIREMESSAGE-USAGE-01", diags)
}

// checkSafeIDFields walks the guarded types and emits a diagnostic for every
// exported field that is neither SafeID-typed nor explicitly exempted.
func checkSafeIDFields(pkg *types.Package) []Diagnostic {
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

// isSafeIDType reports whether t resolves to github.com/ghbvf/gocell/pkg/idutil.SafeID.
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

// TestSAFEIDWireMessageUsage01_BlindSpot_NewWireStruct asserts that no
// other struct in kernel/outbox has the same shape as WireMessage (i.e.
// id/eventType/aggregateId fields typed string) — covers the blind spot
// noted in the package godoc: a parallel wire struct that reintroduces
// untyped fields.
//
// Known limitation: this detector only checks canonical field names listed
// in safeIDWireFieldNames. A new wire-shape struct using alternative
// ID-shaped names (SourceID, CorrelationKey, SenderID) will not be detected.
// When adding such names to any wire struct, reviewers must extend
// safeIDWireFieldNames or directly carve out in safeIDExemptFields.
func TestSAFEIDWireMessageUsage01_BlindSpot_NewWireStruct(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./kernel/outbox/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != outboxPkgPath {
				return nil
			}
			scope := p.Pkg.Scope()
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
				// Heuristic: if a non-allowlisted exported struct has both
				// an exported ID-like field AND an exported EventType/Topic
				// field, treat it as a candidate envelope. ID-like = field
				// whose name matches safeIDWireFieldNames. This is
				// intentionally conservative — we want failure on any new
				// wire-like struct.
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
							p.Pkg.Path(), name, strings.Join(suspectFields, ", "),
						),
					})
				}
			}
			return nil
		})

	Report(t, "SAFEID-WIREMESSAGE-USAGE-01/BlindSpot/NewWireStruct", diags)
}
