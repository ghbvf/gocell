package markergen

import (
	"strings"
	"testing"
)

// makeMarker is a test helper that builds a collectedMarker at a fixed line.
func makeMarker(name, kv string) collectedMarker {
	return collectedMarker{Name: name, KVLine: kv, Line: 10, Target: typeLevel}
}

// makeFieldMarker is a test helper that builds a field-level collectedMarker.
func makeFieldMarker(name, kv, field string) collectedMarker {
	return collectedMarker{Name: name, KVLine: kv, Line: 20, Target: fieldLevel, FieldName: field}
}

// ---- parseListener --------------------------------------------------------

func TestParseListener(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		kv      string
		wantRef string
		wantPfx string
		wantErr string
	}{
		{
			name:    "happy: ref + prefix",
			kv:      "ref=cell.PrimaryListener,prefix=/api/v1",
			wantRef: "cell.PrimaryListener",
			wantPfx: "/api/v1",
		},
		{
			name:    "happy: ref only (no prefix)",
			kv:      "ref=cell.InternalListener",
			wantRef: "cell.InternalListener",
			wantPfx: "",
		},
		{
			name:    "error: missing ref",
			kv:      "prefix=/api/v1",
			wantErr: `missing required field "ref"`,
		},
		{
			name:    "error: unknown field",
			kv:      "ref=cell.PrimaryListener,bogus=x",
			wantErr: `has unknown field "bogus"`,
		},
		{
			name:    "error: empty ref value",
			kv:      "ref=",
			wantErr: `missing required field "ref"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := makeMarker("cell:listener", tc.kv)
			got, err := parseListener(m)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Ref != tc.wantRef {
				t.Errorf("Ref=%q, want %q", got.Ref, tc.wantRef)
			}
			if got.Prefix != tc.wantPfx {
				t.Errorf("Prefix=%q, want %q", got.Prefix, tc.wantPfx)
			}
		})
	}
}

// ---- parseRoute -----------------------------------------------------------

func TestParseRoute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		kv           string
		wantSlice    string
		wantListener string
		wantSubPath  string
		wantErr      string
	}{
		{
			name:         "happy: all fields",
			kv:           "slice=ordercreate,listener=cell.InternalListener,subPath=/orders",
			wantSlice:    "ordercreate",
			wantListener: "cell.InternalListener",
			wantSubPath:  "/orders",
		},
		{
			name:         "happy: listener defaults to cell.PrimaryListener",
			kv:           "slice=ordercreate,subPath=/orders",
			wantSlice:    "ordercreate",
			wantListener: "cell.PrimaryListener",
			wantSubPath:  "/orders",
		},
		{
			name:    "error: missing slice",
			kv:      "subPath=/orders",
			wantErr: `missing required field "slice"`,
		},
		{
			name:    "error: unknown field",
			kv:      "slice=ordercreate,subPath=/orders,extra=oops",
			wantErr: `has unknown field "extra"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := makeFieldMarker("slice:route", tc.kv, "CreateHandler")
			got, err := parseRoute(m)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Slice != tc.wantSlice {
				t.Errorf("Slice=%q, want %q", got.Slice, tc.wantSlice)
			}
			if got.Listener != tc.wantListener {
				t.Errorf("Listener=%q, want %q", got.Listener, tc.wantListener)
			}
			if got.SubPath != tc.wantSubPath {
				t.Errorf("SubPath=%q, want %q", got.SubPath, tc.wantSubPath)
			}
		})
	}
}

// ---- buildBundle dispatch closed-set -------------------------------------

func TestBuildBundle_Dispatch(t *testing.T) {
	t.Parallel()
	t.Run("two marker types round-trip (subscribe removed from markergen)", func(t *testing.T) {
		t.Parallel()
		markers := []collectedMarker{
			makeMarker("cell:listener", "ref=cell.PrimaryListener,prefix=/api/v1"),
			makeFieldMarker("slice:route", "slice=ordercreate,subPath=/orders", "CreateHandler"),
		}
		bundle, errs := buildBundle(markers)
		if len(errs) != 0 {
			t.Fatalf("unexpected errors: %v", errs)
		}
		if len(bundle.Listeners) != 1 || bundle.Listeners[0].Ref != "cell.PrimaryListener" {
			t.Errorf("listeners=%v", bundle.Listeners)
		}
		if len(bundle.Routes) != 1 || bundle.Routes[0].Slice != "ordercreate" {
			t.Errorf("routes=%v", bundle.Routes)
		}
	})

	t.Run("unknown marker with suggestion", func(t *testing.T) {
		t.Parallel()
		markers := []collectedMarker{
			makeMarker("cell:listner", "ref=cell.PrimaryListener"), // typo
		}
		_, errs := buildBundle(markers)
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
		}
		if !strings.Contains(errs[0].Error(), "did you mean") {
			t.Errorf("expected suggestion in error, got %q", errs[0].Error())
		}
	})

	t.Run("unknown marker no suggestion", func(t *testing.T) {
		t.Parallel()
		markers := []collectedMarker{
			makeMarker("completely:unrelated:thing", "x=y"),
		}
		_, errs := buildBundle(markers)
		if len(errs) != 1 {
			t.Fatalf("expected 1 error, got %d", len(errs))
		}
		if strings.Contains(errs[0].Error(), "did you mean") {
			t.Errorf("should not have suggestion, got %q", errs[0].Error())
		}
	})

	t.Run("error accumulation: multiple bad markers continue", func(t *testing.T) {
		t.Parallel()
		markers := []collectedMarker{
			makeMarker("cell:listener", "prefix=/api/v1"),     // missing ref
			makeFieldMarker("slice:route", "subPath=/x", "F"), // missing slice
		}
		bundle, errs := buildBundle(markers)
		if len(errs) < 2 {
			t.Errorf("expected ≥2 errors (non-fail-fast), got %d: %v", len(errs), errs)
		}
		if len(bundle.Listeners) != 0 || len(bundle.Routes) != 0 {
			t.Errorf("expected empty bundle on errors")
		}
	})
}

// ---- slice:subscribe is now unknown (K05 W3 single-source flip) ---------------

// TestBuildBundle_SubscribeMarkerIsNowUnknown verifies that after the
// subscribe single-source flip, the slice:subscribe marker is rejected — and
// the error is a dedicated migration hint pointing at the slice.yaml
// replacement (not a generic "unknown marker" / misleading Levenshtein
// suggestion).
func TestBuildBundle_SubscribeMarkerIsNowUnknown(t *testing.T) {
	t.Parallel()
	markers := []collectedMarker{
		makeFieldMarker("slice:subscribe", "slice=sub,topic=t,handler=H,group=g", "SubField"),
	}
	_, errs := buildBundle(markers)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error for retired slice:subscribe marker, got %d: %v", len(errs), errs)
	}
	msg := errs[0].Error()
	if !strings.Contains(msg, "slice:subscribe") {
		t.Errorf("expected 'slice:subscribe' named in error, got %q", msg)
	}
	// Dedicated migration hint: must steer the author to slice.yaml, not emit a
	// generic "did you mean cell:listener?" suggestion.
	for _, want := range []string{"retired", "slice.yaml", "role: subscribe"} {
		if !strings.Contains(msg, want) {
			t.Errorf("migration hint missing %q, got %q", want, msg)
		}
	}
	if strings.Contains(msg, "did you mean") {
		t.Errorf("retired slice:subscribe must not emit a Levenshtein suggestion, got %q", msg)
	}
}

// ---- K05-04 target level enforcement tests ----------------------------------

func TestDispatchMarker_TargetEnforcement(t *testing.T) {
	t.Parallel()

	t.Run("cell:listener on field is rejected", func(t *testing.T) {
		t.Parallel()
		// cell:listener placed on a field (fieldLevel) — must fail.
		m := collectedMarker{
			Name:      "cell:listener",
			KVLine:    "ref=cell.PrimaryListener",
			Line:      5,
			Target:    fieldLevel,
			FieldName: "MyField",
		}
		var bundle WireBundle
		err := dispatchMarker(m, &bundle)
		if err == nil {
			t.Fatal("expected error for cell:listener on field, got nil")
		}
		if !strings.Contains(err.Error(), "cell:listener marker must be on a type declaration") {
			t.Errorf("unexpected error message: %v", err)
		}
		if !strings.Contains(err.Error(), "MyField") {
			t.Errorf("error should name the field, got: %v", err)
		}
	})

	t.Run("cell:listener on type is accepted", func(t *testing.T) {
		t.Parallel()
		m := makeMarker("cell:listener", "ref=cell.PrimaryListener")
		var bundle WireBundle
		if err := dispatchMarker(m, &bundle); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(bundle.Listeners) != 1 {
			t.Errorf("expected 1 listener, got %d", len(bundle.Listeners))
		}
	})

	t.Run("slice:route on type declaration is rejected", func(t *testing.T) {
		t.Parallel()
		m := makeMarker("slice:route", "slice=ordercreate,subPath=/orders")
		// makeMarker sets Target=typeLevel, FieldName=""
		var bundle WireBundle
		err := dispatchMarker(m, &bundle)
		if err == nil {
			t.Fatal("expected error for slice:route on type, got nil")
		}
		if !strings.Contains(err.Error(), "slice:route marker must be on a named struct field") {
			t.Errorf("unexpected error message: %v", err)
		}
		if !strings.Contains(err.Error(), "type declaration") {
			t.Errorf("error should mention type declaration, got: %v", err)
		}
	})

	t.Run("slice:route on anonymous field is rejected", func(t *testing.T) {
		t.Parallel()
		// fieldLevel but FieldName=="" (anonymous/embedded field)
		m := collectedMarker{
			Name:      "slice:route",
			KVLine:    "slice=ordercreate,subPath=/orders",
			Line:      7,
			Target:    fieldLevel,
			FieldName: "",
		}
		var bundle WireBundle
		err := dispatchMarker(m, &bundle)
		if err == nil {
			t.Fatal("expected error for slice:route on anonymous field, got nil")
		}
		if !strings.Contains(err.Error(), "slice:route marker must be on a named struct field") {
			t.Errorf("unexpected error message: %v", err)
		}
		if !strings.Contains(err.Error(), "anonymous field") {
			t.Errorf("error should mention anonymous field, got: %v", err)
		}
	})

	t.Run("slice:route on named field is accepted", func(t *testing.T) {
		t.Parallel()
		m := makeFieldMarker("slice:route", "slice=ordercreate,subPath=/orders", "CreateHandler")
		var bundle WireBundle
		if err := dispatchMarker(m, &bundle); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(bundle.Routes) != 1 {
			t.Errorf("expected 1 route, got %d", len(bundle.Routes))
		}
	})

	t.Run("slice:subscribe is rejected with migration hint regardless of target level (type)", func(t *testing.T) {
		t.Parallel()
		m := makeMarker("slice:subscribe", "slice=s,topic=t,handler=H,group=g")
		var bundle WireBundle
		err := dispatchMarker(m, &bundle)
		if err == nil {
			t.Fatal("expected error for retired slice:subscribe marker, got nil")
		}
		if !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), "slice.yaml") {
			t.Errorf("expected migration hint (retired → slice.yaml), got: %v", err)
		}
	})

	t.Run("slice:subscribe is rejected with migration hint regardless of target level (field)", func(t *testing.T) {
		t.Parallel()
		m := makeFieldMarker("slice:subscribe", "slice=s,topic=t,handler=H,group=g", "SubField")
		var bundle WireBundle
		err := dispatchMarker(m, &bundle)
		if err == nil {
			t.Fatal("expected error for retired slice:subscribe marker on field, got nil")
		}
		if !strings.Contains(err.Error(), "retired") || !strings.Contains(err.Error(), "slice.yaml") {
			t.Errorf("expected migration hint (retired → slice.yaml), got: %v", err)
		}
		// WireBundle no longer has a Subscribes field (removed in single-source flip).
		if !strings.Contains(err.Error(), "slice:subscribe") {
			t.Errorf("expected 'slice:subscribe' in error, got: %v", err)
		}
	})
}
