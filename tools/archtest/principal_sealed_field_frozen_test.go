// INVARIANT: PRINCIPAL-SEALED-FIELD-FROZEN-01
//
// This file owns ONE invariant: the kernel/outbox.PrincipalMetadata schema is
// frozen at the field-set + JSON-tag + method-set level. The companion lock
// SAFEID-WIREMESSAGE-USAGE-01 already enforces that every exported field on
// PrincipalMetadata is idutil.SafeID-typed (via the safeIDExemptFields
// "PrincipalMetadata: {}" carve-out registration). This file adds the
// orthogonal axes that SAFEID-WIREMESSAGE-USAGE-01 does not cover:
//
//  1. Exact field-name set: ActorID / SubjectID / TenantID / SessionID — a
//     rename (ActorID → Actor) keeps SafeID typing but rewires the wire
//     contract under JSON producers/consumers without forcing reviewer
//     attention. Reflect over the production type and assert NumField + name
//     set, mirroring the OUTBOX-HANDLERESULT-FIELDS-FROZEN-01 reflect lock.
//
//  2. JSON tag stability: actorId / subjectId / tenantId / sessionId. JSON
//     tag drift is a wire-compat break invisible to compilers and to the
//     SafeID type funnel.
//
//  3. Method set: IsZero / Validate / RestoreToContext on PrincipalMetadata;
//     ContextPrincipal free function; InjectPrincipalFromContext as a method
//     on *Entry. The five-symbol set forms the sealed write/read funnel
//     mirroring ObservabilityMetadata; removing one or renaming one without
//     reviewer attention breaks the producer-clock injection / consumer-side
//     restore contract.
//
// AI-robust rating (per .claude/rules/gocell/ai-robust.md):
//
//   - Hard (downstream): SAFEID-WIREMESSAGE-USAGE-01 enforces SafeID typing
//     for every exported PrincipalMetadata field; this archtest enforces
//     field names, JSON tags, and method set via reflect + AST. Together
//     they leave no AST-expressible drift undetected at PR time.
//   - Hard (upstream): wireMessage is unexported and PrincipalMetadata sits
//     inside it; SAFEID-UPSTREAM-FUNNEL-HARD-01 enforces no re-export under
//     any name. Package-external code cannot construct a wireMessage envelope
//     that bypasses MarshalEnvelope's Principal.Validate fail-fast.
//
// Tool blind spots (per AI-robust §载体决策原则 "强制盲区自检"):
//
//   - reflect.TypeOf does not see method bodies — a bug where Validate
//     returns nil unconditionally would still pass this archtest. Method
//     behavior is covered by kernel/outbox/principal_test.go table-driven
//     tests; this archtest only enforces the symbol set.
//   - AST walk over principal.go does not see methods declared in other
//     files in package outbox — the canonical methods live in principal.go
//     by convention (mirror of observability.go). A future split that moves
//     PrincipalMetadata.Validate to a sibling file would silently miss the
//     AST check. The ScannerFires self-check below proves the AST anchor
//     fires on a synthetic AST that removes a required method declaration.
//   - Field tag parsing uses reflect.StructTag.Get("json") — a malformed
//     tag (e.g. missing comma) would return "" and silently match the empty
//     expectation. The expectation set explicitly lists each tag, so empty
//     never matches a real entry.
//   - Tag DRIFT (wrong tag value, e.g., actor_id instead of actorId) IS
//     covered by the reflect tag check in TestPrincipalSealedFieldFrozen01:
//     each field's f.Tag.Get("json") is compared against the canonical value
//     in principalCanonicalFields. This is NOT a blind spot.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// principalCanonicalFields maps PrincipalMetadata's exact exported field
// names to their canonical JSON tags. Drift here is a wire-compat break
// invisible to compilers and to SAFEID-WIREMESSAGE-USAGE-01.
var principalCanonicalFields = map[string]string{
	"ActorID":   "actorId,omitempty",
	"SubjectID": "subjectId,omitempty",
	"TenantID":  "tenantId,omitempty",
	"SessionID": "sessionId,omitempty",
}

// principalSealedMethodSet is the closed set of methods that form the
// PrincipalMetadata read/write/restore funnel. AST anchored to principal.go
// — the canonical home (sibling of observability.go).
var principalSealedMethodSet = []string{
	// Methods on PrincipalMetadata value receiver.
	"IsZero",
	"Validate",
	"RestoreToContext",
	// Free function (no receiver) — ctx → PrincipalMetadata adapter.
	"ContextPrincipal",
	// Method on *Entry pointer receiver — sealed producer write path.
	"InjectPrincipalFromContext",
}

// TestPrincipalSealedFieldFrozen01 reflectively asserts the field set + JSON
// tags + method set on kernel/outbox.PrincipalMetadata. Together with
// SAFEID-WIREMESSAGE-USAGE-01 (deny-by-default SafeID typing) and
// SAFEID-UPSTREAM-FUNNEL-HARD-01 (wireMessage seal), this freezes the
// Principal-namespace wire envelope schema at PR time.
func TestPrincipalSealedFieldFrozen01(t *testing.T) {
	t.Parallel()

	// Reflect lock: field set + JSON tags.
	pt := reflect.TypeOf(outbox.PrincipalMetadata{})
	if pt.Kind() != reflect.Struct {
		t.Fatalf("PRINCIPAL-SEALED-FIELD-FROZEN-01: PrincipalMetadata Kind = %s, want Struct", pt.Kind())
	}
	if got, want := pt.NumField(), len(principalCanonicalFields); got != want {
		t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: PrincipalMetadata NumField = %d, want %d "+
			"— add/remove a field requires updating principalCanonicalFields, the wire envelope ADR, "+
			"and re-evaluating producer-side InjectPrincipalFromContext", got, want)
	}

	seenNames := make(map[string]struct{}, pt.NumField())
	for i := 0; i < pt.NumField(); i++ {
		f := pt.Field(i)
		seenNames[f.Name] = struct{}{}
		wantTag, ok := principalCanonicalFields[f.Name]
		if !ok {
			t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: PrincipalMetadata has unknown field %q — "+
				"if the field is intentional, register it in principalCanonicalFields with "+
				"its JSON tag and update the wire envelope ADR", f.Name)
			continue
		}
		gotTag := f.Tag.Get("json")
		if gotTag != wantTag {
			t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: PrincipalMetadata.%s json tag = %q, want %q "+
				"— JSON tag drift breaks wire compatibility; update producers/consumers and the ADR before changing",
				f.Name, gotTag, wantTag)
		}
	}
	for want := range principalCanonicalFields {
		if _, ok := seenNames[want]; !ok {
			t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: PrincipalMetadata missing required field %q "+
				"— removing a field is a wire-compat break; coordinate with subscriber implementations "+
				"and the ADR before removing", want)
		}
	}

	// AST lock: method-set anchored to principal.go.
	root := findModuleRoot(t)
	principalPath := filepath.Join(root, "kernel", "outbox", "principal.go")
	file := mustParseGoFile(t, principalPath)
	gotMethods := collectPrincipalSealedSymbols(file)
	wantMethods := append([]string(nil), principalSealedMethodSet...)
	sort.Strings(wantMethods)
	if !reflect.DeepEqual(gotMethods, wantMethods) {
		t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01: principal.go funnel symbol set = %v, want %v "+
			"— adding/removing/renaming a method or free function in the funnel must keep the "+
			"five-symbol contract intact (IsZero/Validate/RestoreToContext on PrincipalMetadata + "+
			"ContextPrincipal + Entry.InjectPrincipalFromContext)", gotMethods, wantMethods)
	}
}

// collectPrincipalSealedSymbols returns the sorted set of method names + free
// functions declared in principal.go that match the canonical funnel symbol
// set. Receivers are matched on type identity (PrincipalMetadata or *Entry)
// to keep the check robust against parameter renames.
func collectPrincipalSealedSymbols(file *ast.File) []string {
	want := make(map[string]struct{}, len(principalSealedMethodSet))
	for _, s := range principalSealedMethodSet {
		want[s] = struct{}{}
	}
	seen := map[string]struct{}{}
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if _, expected := want[fn.Name.Name]; !expected {
			return
		}
		// Receiver disambiguation: PrincipalMetadata methods land at value
		// receiver; InjectPrincipalFromContext is a *Entry method;
		// ContextPrincipal has no receiver.
		switch fn.Name.Name {
		case "IsZero", "Validate", "RestoreToContext":
			if !receiverIsType(fn.Recv, "PrincipalMetadata", false) {
				return
			}
		case "InjectPrincipalFromContext":
			if !receiverIsType(fn.Recv, "Entry", true) {
				return
			}
		case "ContextPrincipal":
			if fn.Recv != nil {
				return
			}
		}
		seen[fn.Name.Name] = struct{}{}
	})
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// receiverIsType reports whether recv is a receiver whose type matches name.
// If wantPointer is true, the receiver must be a *Name; otherwise it must be
// a bare Name.
func receiverIsType(recv *ast.FieldList, name string, wantPointer bool) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	expr := recv.List[0].Type
	if wantPointer {
		star, ok := expr.(*ast.StarExpr)
		if !ok {
			return false
		}
		ident, ok := star.X.(*ast.Ident)
		return ok && ident.Name == name
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

// TestPrincipalSealedFieldFrozen01_ScannerFires is the reverse self-check
// required by AI-robust §载体决策原则: prove the AST scanner detects a
// missing method. Constructs a synthetic *ast.File missing one of the
// canonical funnel methods and asserts collectPrincipalSealedSymbols
// returns a non-equal set.
func TestPrincipalSealedFieldFrozen01_ScannerFires(t *testing.T) {
	t.Parallel()

	// Build a synthetic file that declares only 4 of the 5 funnel methods —
	// missing InjectPrincipalFromContext. The AST scanner MUST omit it from
	// the returned set, producing a deep-unequal comparison against the
	// canonical 5-method expected set.
	syntheticSrc := `package outbox

type PrincipalMetadata struct{}
type Entry struct{}
type Context interface{}

func (PrincipalMetadata) IsZero() bool { return false }
func (PrincipalMetadata) Validate() error { return nil }
func (PrincipalMetadata) RestoreToContext(ctx Context) Context { return ctx }
func ContextPrincipal(ctx Context) PrincipalMetadata { return PrincipalMetadata{} }
// InjectPrincipalFromContext intentionally absent — the scanner MUST notice.
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", syntheticSrc, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("PRINCIPAL-SEALED-FIELD-FROZEN-01 scanner self-check: parse synthetic src: %v", err)
	}
	got := collectPrincipalSealedSymbols(file)
	want := []string{"ContextPrincipal", "IsZero", "RestoreToContext", "Validate"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01 scanner self-check: synthetic AST got = %v, want = %v "+
			"— if collectPrincipalSealedSymbols was refactored to skip receiver disambiguation, "+
			"the production scanner becomes blind to missing methods", got, want)
	}

	// And confirm the production assertion would fail on this set (deep-unequal vs canonical 5-method set).
	canonical := append([]string(nil), principalSealedMethodSet...)
	sort.Strings(canonical)
	if reflect.DeepEqual(got, canonical) {
		t.Errorf("PRINCIPAL-SEALED-FIELD-FROZEN-01 scanner self-check: synthetic missing-method file "+
			"matched the canonical 5-symbol set %v — the scanner has lost the ability to detect "+
			"missing funnel symbols, defeating the freeze", canonical)
	}
}
