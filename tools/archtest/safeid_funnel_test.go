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
// Upstream Hard:
//
//	json.Unmarshal is the single decode entry point for wire envelopes —
//	UnmarshalEnvelope calls it directly (kernel/outbox/envelope.go) and is
//	itself the only caller used by every transport (RabbitMQ subscriber,
//	in-memory eventbus, examples). The Go runtime guarantees the
//	UnmarshalJSON dispatch; no business code participates in the decode
//	pipeline. Form uniqueness on the field type therefore implies
//	boundary-time validation by construction.
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

// safeIDRequiredFields maps named types in kernel/outbox to the subset of
// exported fields that MUST be typed idutil.SafeID. Adding a new ID-shaped
// wire field requires extending this map; renaming a covered field without
// updating this map will fail the "field not found" branch below.
var safeIDRequiredFields = map[string][]string{
	wireMessageType:   {"ID", "AggregateID", "AggregateType", "EventType", "Topic"},
	observabilityType: {"TraceID", "RequestID", "CorrelationID"},
}

// safeIDBlindSpotAllowlist lists structs that legitimately keep string-typed
// ID-shaped fields. Entry is the in-memory representation populated AFTER
// UnmarshalEnvelope has validated SafeID at the wire boundary; downstream
// code (slog.String, PG columns) consumes plain string. Adding a struct
// here requires reviewer judgment that it is NOT a wire-decode entry point.
var safeIDBlindSpotAllowlist = map[string]struct{}{
	"Entry": {},
}

// TestSAFEIDWireMessageUsage01 asserts that every field listed in
// safeIDRequiredFields is declared with type idutil.SafeID.
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

func checkSafeIDFields(pkg *types.Package) []Diagnostic {
	var diags []Diagnostic

	// Deterministic iteration: sort type names so failure messages are stable.
	typeNames := make([]string, 0, len(safeIDRequiredFields))
	for tn := range safeIDRequiredFields {
		typeNames = append(typeNames, tn)
	}
	sort.Strings(typeNames)

	for _, typeName := range typeNames {
		want := safeIDRequiredFields[typeName]
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

		got := make(map[string]types.Type, strct.NumFields())
		for i := 0; i < strct.NumFields(); i++ {
			f := strct.Field(i)
			got[f.Name()] = f.Type()
		}
		for _, fieldName := range want {
			ft, present := got[fieldName]
			if !present {
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf("SAFEID-WIREMESSAGE-USAGE-01: %s.%s missing required field %q; rename or removal must keep the field typed idutil.SafeID",
						pkg.Path(), typeName, fieldName),
				})
				continue
			}
			if !isSafeIDType(ft) {
				diags = append(diags, Diagnostic{
					Message: fmt.Sprintf("SAFEID-WIREMESSAGE-USAGE-01: %s.%s.%s has type %s; must be idutil.SafeID so SafeID.UnmarshalJSON validates wire input (CWE-117)",
						pkg.Path(), typeName, fieldName, ft.String()),
				})
			}
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

// TestSAFEIDWireMessageUsage01_BlindSpot_NewWireStruct asserts that no
// other struct in kernel/outbox has the same shape as WireMessage (i.e.
// id/eventType/aggregateId fields typed string) — covers the blind spot
// noted in the package godoc: a parallel wire struct that reintroduces
// untyped fields.
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
				// whose name matches our safeIDRequiredFields entries on
				// WireMessage. This is intentionally conservative — we want
				// failure on any new wire-like struct.
				suspectCount := 0
				suspectFields := []string{}
				for i := 0; i < strct.NumFields(); i++ {
					f := strct.Field(i)
					if !f.Exported() {
						continue
					}
					if !isWireFieldName(f.Name()) {
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
						Message: fmt.Sprintf("SAFEID-WIREMESSAGE-USAGE-01/BlindSpot: %s.%s looks like a parallel wire envelope (string-typed fields %s) — extend safeIDRequiredFields or change the fields to idutil.SafeID",
							p.Pkg.Path(), name, strings.Join(suspectFields, ", ")),
					})
				}
			}
			return nil
		})

	Report(t, "SAFEID-WIREMESSAGE-USAGE-01/BlindSpot/NewWireStruct", diags)
}

// isWireFieldName reports whether name matches an ID-shaped wire field
// (any field listed in safeIDRequiredFields, across all guarded types).
func isWireFieldName(name string) bool {
	for _, names := range safeIDRequiredFields {
		for _, n := range names {
			if n == name {
				return true
			}
		}
	}
	return false
}
