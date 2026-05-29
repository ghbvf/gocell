package archtest

// reconcile_invariants_test.go locks the three-piece minimal core of
// kernel/reconcile — the Reconciler interface method set and the Request /
// Result field sets — via reflect golden freezes.
//
//   - INVARIANT: RECONCILE-INTERFACE-FROZEN-01
//   - INVARIANT: RECONCILE-REQUEST-FIELDS-FROZEN-01
//   - INVARIANT: RECONCILE-RESULT-FIELDS-FROZEN-01
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
	"reflect"
	"testing"
	"time"

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
