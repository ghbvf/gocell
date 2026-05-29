package archtest

// reconcile_invariants_test.go locks kernel/reconcile's public surface: the
// three-piece minimal core (Reconciler interface method set + Request / Result
// field sets) via reflect golden freezes, plus the transition-window guard that
// no code outside the package bare-constructs the scheduling Loop.
//
//   - INVARIANT: RECONCILE-INTERFACE-FROZEN-01
//   - INVARIANT: RECONCILE-REQUEST-FIELDS-FROZEN-01
//   - INVARIANT: RECONCILE-RESULT-FIELDS-FROZEN-01
//   - INVARIANT: RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01
//
// The design ADR (docs/architecture/202605291600-661-adr-kernel-reconcile-design.md)
// promises a deliberately minimal public surface: a Reconciler whose only method
// is Reconcile(ctx, Request) (Result, error), a Request that carries exactly one
// opaque EntityID (no NamespacedName), and a Result that carries exactly one
// RequeueAfter (no Requeue bool, no Priority int — both are controller-runtime /
// K8s-scheduler residue GoCell drops). These reflect freezes are what make that
// promise Hard rather than a godoc convention: any drift (added/removed/renamed/
// retyped field, extra interface method, changed signature, embedding) trips an
// exact-set assertion in CI.
//
// AI-robust rating (.claude/rules/gocell/ai-robust.md §Hard 范本目录 "codegen
// funnel + golden" sibling = reflect field-freeze; cf. PEER-IDENTITY-FIELDS-
// FROZEN-01 / DETAILS-SEALED-FIELD-FROZEN-01 / OUTBOX-HANDLERESULT-FIELDS-
// FROZEN-01): Hard. A field/method-set drift is inexpressible without a
// CI-visible failure. Scope boundary: these freeze the type SHAPE only; the
// scheduling Loop's behavior and the PermanentError marker's seal are enforced
// elsewhere (PROD-CLOCK-INJECTION-01 and the unexported permanentError type
// respectively).
//
// Blind spots of a reflect freeze, each covered by the reverse self-checks
// below (non-vacuous proof the detectors flag malformed shapes):
//   - Embedding: an anonymous field would smuggle a whole struct's surface past
//     a name-keyed check → guarded explicitly (f.Anonymous → violation).
//   - Type alias re-shape: reflect resolves an alias to its underlying type, so
//     an alias of the same type (type D = time.Duration) is not a re-shape and is
//     not flagged, while a distinct named type (type D time.Duration) IS flagged
//     — both directions proven by the alias-same-type / named-retype self-checks.
//   - Wrong type / unexported / extra / missing field or method: covered by the
//     exact name→type maps + NumField / NumMethod checks.

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/reconcile"
)

// -----------------------------------------------------------------------------
// RECONCILE-INTERFACE-FROZEN-01 — Reconciler method set
// -----------------------------------------------------------------------------

// checkReconcilerInterface returns violation messages for it against the frozen
// Reconciler shape (exactly one method: Reconcile(context.Context, Request)
// (Result, error)). An empty result means it conforms. Extracted so the reverse
// self-check can prove the detector is non-vacuous.
func checkReconcilerInterface(it reflect.Type) []string {
	if it == nil || it.Kind() != reflect.Interface {
		return []string{fmt.Sprintf("Kind = %v, want interface", it)}
	}
	var v []string
	if it.NumMethod() != 1 {
		v = append(v, fmt.Sprintf("NumMethod = %d, want exactly 1 (Reconcile)", it.NumMethod()))
	}
	m, ok := it.MethodByName("Reconcile")
	if !ok {
		return append(v, "missing method Reconcile")
	}
	mt := m.Type // interface method Type has NO receiver
	if mt.IsVariadic() {
		v = append(v, "Reconcile must not be variadic")
	}
	wantIn := []string{"context.Context", "reconcile.Request"}
	if mt.NumIn() != len(wantIn) {
		v = append(v, fmt.Sprintf("Reconcile NumIn = %d, want %d", mt.NumIn(), len(wantIn)))
	} else {
		for i, w := range wantIn {
			if got := mt.In(i).String(); got != w {
				v = append(v, fmt.Sprintf("Reconcile arg %d = %q, want %q", i, got, w))
			}
		}
	}
	wantOut := []string{"reconcile.Result", "error"}
	if mt.NumOut() != len(wantOut) {
		v = append(v, fmt.Sprintf("Reconcile NumOut = %d, want %d", mt.NumOut(), len(wantOut)))
	} else {
		for i, w := range wantOut {
			if got := mt.Out(i).String(); got != w {
				v = append(v, fmt.Sprintf("Reconcile result %d = %q, want %q", i, got, w))
			}
		}
	}
	return v
}

func TestReconcileInterfaceFrozen01(t *testing.T) {
	t.Parallel()
	it := reflect.TypeOf((*reconcile.Reconciler)(nil)).Elem()
	for _, msg := range checkReconcilerInterface(it) {
		t.Errorf("RECONCILE-INTERFACE-FROZEN-01: %s. The Reconciler method set is frozen to "+
			"Reconcile(ctx, Request) (Result, error); if this change is intentional, update the "+
			"design ADR §3 and this golden in the same PR.", msg)
	}
}

// -----------------------------------------------------------------------------
// RECONCILE-REQUEST-FIELDS-FROZEN-01 — Request field set
// -----------------------------------------------------------------------------

var reconcileRequestWantFields = map[string]string{
	"EntityID": "string",
}

func checkReconcileStructShape(dt reflect.Type, want map[string]string) []string {
	if dt == nil || dt.Kind() != reflect.Struct {
		return []string{fmt.Sprintf("Kind = %v, want struct", dt)}
	}
	var v []string
	if dt.NumField() != len(want) {
		v = append(v, fmt.Sprintf("NumField = %d, want exactly %d", dt.NumField(), len(want)))
	}
	for i := 0; i < dt.NumField(); i++ {
		f := dt.Field(i)
		if f.Anonymous {
			v = append(v, fmt.Sprintf("field %q is embedded (embedding re-opens the frozen surface)", f.Name))
			continue
		}
		if !f.IsExported() {
			v = append(v, fmt.Sprintf("field %q is unexported (the minimal core surfaces only exported fields)", f.Name))
		}
		ts := f.Type.String()
		// Explicit rejection of the controller-runtime / K8s-scheduler residue the
		// ADR §0 lists as dropped, with a pointed message.
		switch f.Name {
		case "Requeue":
			v = append(v, fmt.Sprintf("field %q (%s) is dropped K8s residue — express requeue via RequeueAfter==0", f.Name, ts))
		case "Priority":
			v = append(v, fmt.Sprintf("field %q (%s) is dropped K8s-scheduler residue — reconcile has no priority dimension", f.Name, ts))
		}
		wantType, ok := want[f.Name]
		if !ok {
			v = append(v, fmt.Sprintf("unexpected field %q (%s)", f.Name, ts))
			continue
		}
		if ts != wantType {
			v = append(v, fmt.Sprintf("field %q type = %q, want %q", f.Name, ts, wantType))
		}
	}
	return v
}

func TestReconcileRequestFieldsFrozen01(t *testing.T) {
	t.Parallel()
	for _, msg := range checkReconcileStructShape(reflect.TypeOf(reconcile.Request{}), reconcileRequestWantFields) {
		t.Errorf("RECONCILE-REQUEST-FIELDS-FROZEN-01: %s. The frozen set is %v; if intentional, "+
			"update the design ADR §3 and this golden in the same PR.", msg, reconcileRequestWantFields)
	}
}

// -----------------------------------------------------------------------------
// RECONCILE-RESULT-FIELDS-FROZEN-01 — Result field set
// -----------------------------------------------------------------------------

var reconcileResultWantFields = map[string]string{
	"RequeueAfter": "time.Duration",
}

func TestReconcileResultFieldsFrozen01(t *testing.T) {
	t.Parallel()
	for _, msg := range checkReconcileStructShape(reflect.TypeOf(reconcile.Result{}), reconcileResultWantFields) {
		t.Errorf("RECONCILE-RESULT-FIELDS-FROZEN-01: %s. The frozen set is %v; if intentional, "+
			"update the design ADR §3 and this golden in the same PR.", msg, reconcileResultWantFields)
	}
}

// -----------------------------------------------------------------------------
// Reverse blind-spot self-checks — prove the detectors are non-vacuous.
// -----------------------------------------------------------------------------

func TestReconcileInterfaceFrozen01_ReverseBlindSpot(t *testing.T) {
	t.Parallel()

	type good interface {
		Reconcile(context.Context, reconcile.Request) (reconcile.Result, error)
	}
	if v := checkReconcilerInterface(reflect.TypeOf((*good)(nil)).Elem()); len(v) != 0 {
		t.Errorf("RECONCILE-INTERFACE-FROZEN-01 self-test: detector flagged a conforming interface "+
			"(vacuous-pass risk): %v", v)
	}

	type extraMethod interface {
		Reconcile(context.Context, reconcile.Request) (reconcile.Result, error)
		Close() error
	}
	type wrongArity interface {
		Reconcile(context.Context) error
	}
	type wrongReturn interface {
		Reconcile(context.Context, reconcile.Request) error
	}
	bad := map[string]reflect.Type{
		"extra-method": reflect.TypeOf((*extraMethod)(nil)).Elem(),
		"wrong-arity":  reflect.TypeOf((*wrongArity)(nil)).Elem(),
		"wrong-return": reflect.TypeOf((*wrongReturn)(nil)).Elem(),
	}
	for name, it := range bad {
		if v := checkReconcilerInterface(it); len(v) == 0 {
			t.Errorf("RECONCILE-INTERFACE-FROZEN-01 self-test: detector passed malformed interface %q "+
				"(blind spot): expected at least one violation", name)
		}
	}
}

func TestReconcileStructShape_ReverseBlindSpot(t *testing.T) {
	t.Parallel()

	type goodRequest struct{ EntityID string }
	if v := checkReconcileStructShape(reflect.TypeOf(goodRequest{}), reconcileRequestWantFields); len(v) != 0 {
		t.Errorf("RECONCILE-REQUEST self-test: detector flagged a conforming Request (vacuous-pass risk): %v", v)
	}
	type goodResult struct{ RequeueAfter time.Duration }
	if v := checkReconcileStructShape(reflect.TypeOf(goodResult{}), reconcileResultWantFields); len(v) != 0 {
		t.Errorf("RECONCILE-RESULT self-test: detector flagged a conforming Result (vacuous-pass risk): %v", v)
	}

	// Type-alias re-shape blind spot, proven non-vacuous in both directions: an
	// alias to the SAME underlying type resolves back via reflect and must NOT be
	// flagged (over-fire guard) ...
	type aliasDuration = time.Duration
	type aliasResult struct{ RequeueAfter aliasDuration }
	if v := checkReconcileStructShape(reflect.TypeOf(aliasResult{}), reconcileResultWantFields); len(v) != 0 {
		t.Errorf("RECONCILE-RESULT self-test: detector flagged an alias-of-same-type Result (vacuous over-fire): %v", v)
	}

	type requeueBoolResidue struct {
		RequeueAfter time.Duration
		Requeue      bool
	}
	type priorityResidue struct {
		RequeueAfter time.Duration
		Priority     int
	}
	type namespacedRequest struct {
		EntityID  string
		Namespace string
	}
	type embeddedResult struct {
		time.Duration
	}
	type unexportedRequest struct{ entityID string }
	_ = unexportedRequest{}.entityID // read the field so 'unused' is satisfied; the shape check flags its unexported visibility
	type wrongTypeResult struct{ RequeueAfter int64 }
	// ... while a distinct named type (NOT an alias) over the same underlying
	// kind IS a re-shape and MUST be flagged — reflect reports its declared name,
	// not time.Duration, so the exact-type check fires.
	type namedDuration time.Duration
	type namedRetypeResult struct{ RequeueAfter namedDuration }

	badResult := map[string]reflect.Type{
		"requeue-bool-residue": reflect.TypeOf(requeueBoolResidue{}),
		"priority-residue":     reflect.TypeOf(priorityResidue{}),
		"embedded-field":       reflect.TypeOf(embeddedResult{}),
		"wrong-type":           reflect.TypeOf(wrongTypeResult{}),
		"named-retype":         reflect.TypeOf(namedRetypeResult{}),
	}
	for name, dt := range badResult {
		if v := checkReconcileStructShape(dt, reconcileResultWantFields); len(v) == 0 {
			t.Errorf("RECONCILE-RESULT self-test: detector passed malformed Result %q (blind spot)", name)
		}
	}
	badRequest := map[string]reflect.Type{
		"extra-namespace": reflect.TypeOf(namespacedRequest{}),
		"unexported":      reflect.TypeOf(unexportedRequest{}),
	}
	for name, dt := range badRequest {
		if v := checkReconcileStructShape(dt, reconcileRequestWantFields); len(v) == 0 {
			t.Errorf("RECONCILE-REQUEST self-test: detector passed malformed Request %q (blind spot)", name)
		}
	}
}

// -----------------------------------------------------------------------------
// RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01 — no bare Loop{} outside the package
// -----------------------------------------------------------------------------
//
// # RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01
//
// reconcile.Loop currently exposes exported fields, so a consumer could
// bare-construct `reconcile.Loop{Reconciler: r}` and silently skip the
// metric / leader / backoff wiring a Builder would inject (threat T-BUILDER,
// design ADR §"威胁矩阵"). PR-A7 closes this for good by privatizing the Loop
// constructor behind a Builder (the Hard upstream); until then this archtest
// holds the line: a production composite literal of reconcile.Loop is forbidden
// anywhere except the home package itself and the A8 kernel/command migration
// point. Test files (`*_test.go`) construct Loop directly via exported fields and
// are out of scope (RunTypedProduction excludes them).
//
// AI-robust grade (per .claude/rules/gocell/ai-robust.md §"Funnel 双向锁评级"):
//   - Downstream: Hard. The forbidden form is a single AST shape —
//     ast.CompositeLit whose type resolves to (kernel/reconcile, "Loop") — with
//     no "looks-like-but-isn't" grey zone; the type resolver sees through import
//     aliases and `&Loop{}` address-of wrappers.
//   - Upstream: Medium (transition). The package-external ban is an archtest
//     caller-allowlist, not a type-system seal — Go visibility cannot express
//     "only package P may compose this exported struct". PR-A7's constructor
//     privatization is the Hard upstream that retires this guard (replaced by
//     RECONCILE-BUILDER-FUNNEL-01). Tracked under parent issue #661, task A7.
//
// This is the documented Medium-upstream + Hard-downstream transition form the
// charter permits with an open tracking issue (#661 / A7), named here so a
// reviewer can follow the upgrade path.
//
// Blind spots (per ai-robust.md §"工具选定后强制盲区自检"):
//   - Import alias (`import rc ".../kernel/reconcile"; rc.Loop{}`): the type
//     resolver keys on the resolved *types.Named obj package path, not the
//     source alias, so aliasing does not hide the literal.
//   - Address-of (`&reconcile.Loop{}`): EachInSubtree visits the inner
//     CompositeLit regardless of the enclosing UnaryExpr, so `&Loop{}` is caught.
//   - Reflection construction (`reflect.New(...)`): out of scope, same theoretical
//     gap accepted by BASESLICE-CTOR-FUNNEL-01.
//   - Zero-field literal `reconcile.Loop{}`: still a CompositeLit of the type —
//     flagged; the RED fixture below uses exactly this shape to prove the
//     detector is non-vacuous.

// reconcileLoopLiteralMsg is the diagnostic for a forbidden reconcile.Loop
// composite literal. Extracted as a const to keep callsites within the line cap.
const reconcileLoopLiteralMsg = "forbidden composite literal reconcile.Loop{...} outside kernel/reconcile" +
	" — construct via the package Builder (PR-A7); the exported-field form skips metric/leader/backoff wiring"

// isReconcileLoopType reports whether expr names reconcile.Loop from reconcilePkgPath.
func isReconcileLoopType(info *types.Info, expr ast.Expr, reconcilePkgPath string) bool {
	if info == nil || expr == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == reconcilePkgPath && obj.Name() == "Loop"
}

// scanReconcileLoopConstruction flags every reconcile.Loop composite literal in
// p, unless p's package is in allowedPkgPaths (the home package + the A8
// kernel/command migration point). Test files are excluded by the caller
// (RunTypedProduction with Tests:false).
func scanReconcileLoopConstruction(p *Pass, reconcilePkgPath string, allowedPkgPaths map[string]bool) []Diagnostic {
	if p.Pkg != nil && allowedPkgPaths[p.Pkg.Path()] {
		return nil
	}
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
			if !isReconcileLoopType(p.TypesInfo, lit.Type, reconcilePkgPath) {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    p.Fset.Position(lit.Pos()).Line,
				Message: reconcileLoopLiteralMsg,
			})
		})
	}
	return diags
}

// reconcileLoopConstructionAllowed returns the package paths permitted to
// compose reconcile.Loop: the home package (future Builder host) and the A8
// kernel/command migration point.
func reconcileLoopConstructionAllowed(modPath string) (reconcilePkgPath string, allowed map[string]bool) {
	reconcilePkgPath = modPath + "/kernel/reconcile"
	return reconcilePkgPath, map[string]bool{
		reconcilePkgPath:            true,
		modPath + "/kernel/command": true, // A8 migration point (design ADR)
	}
}

// TestReconcileLoopConstructionAllowlist01 is the production GREEN baseline: no
// package outside the allowlist bare-constructs reconcile.Loop.
func TestReconcileLoopConstructionAllowlist01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	reconcilePkgPath, allowed := reconcileLoopConstructionAllowed(modPath)

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		return scanReconcileLoopConstruction(p, reconcilePkgPath, allowed)
	})
	Report(t, "RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01", diags)
}

// TestReconcileLoopConstructionAllowlist01_RedFixture proves the detector is
// non-vacuous: a fixture package outside the allowlist that composes
// reconcile.Loop{} must be flagged at least once.
func TestReconcileLoopConstructionAllowlist01_RedFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	reconcilePkgPath, allowed := reconcileLoopConstructionAllowed(modPath)

	diags := RunTypedFixture(
		t,
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/reconcileloopredfixture/..."},
		func(p *Pass) []Diagnostic {
			return scanReconcileLoopConstruction(p, reconcilePkgPath, allowed)
		},
	)

	var hits int
	for _, d := range diags {
		if d.Message == reconcileLoopLiteralMsg {
			hits++
		}
	}
	if hits == 0 {
		t.Errorf("RECONCILE-LOOP-CONSTRUCTION-ALLOWLIST-01 RED fixture: scanner found 0 hits; " +
			"expected ≥ 1 from reconcileloopredfixture — the detector may be broken")
	}
}
