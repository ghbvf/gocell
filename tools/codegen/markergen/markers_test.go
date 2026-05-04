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
			name:    "error: missing subPath",
			kv:      "slice=ordercreate",
			wantErr: `missing required field "subPath"`,
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

// ---- parseSubscribe -------------------------------------------------------

func TestParseSubscribe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		kv          string
		wantSlice   string
		wantTopic   string
		wantHandler string
		wantGroup   string
		wantErr     string
	}{
		{
			name:        "happy: all fields",
			kv:          "slice=configsubscribe,topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=configcore",
			wantSlice:   "configsubscribe",
			wantTopic:   "event.config.entry-upserted.v1",
			wantHandler: "HandleEntryUpserted",
			wantGroup:   "configcore",
		},
		{
			name:    "error: missing slice",
			kv:      "topic=event.foo.v1,handler=Handle,group=grp",
			wantErr: `missing required field "slice"`,
		},
		{
			name:    "error: missing topic",
			kv:      "slice=s,handler=Handle,group=grp",
			wantErr: `missing required field "topic"`,
		},
		{
			name:    "error: missing handler",
			kv:      "slice=s,topic=t,group=grp",
			wantErr: `missing required field "handler"`,
		},
		{
			name:    "error: missing group",
			kv:      "slice=s,topic=t,handler=Handle",
			wantErr: `missing required field "group"`,
		},
		{
			name:    "error: unknown field",
			kv:      "slice=s,topic=t,handler=Handle,group=g,extra=x",
			wantErr: `has unknown field "extra"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := makeFieldMarker("slice:subscribe", tc.kv, "ConfigSub")
			got, err := parseSubscribe(m)
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
				t.Errorf("Slice=%q", got.Slice)
			}
			if got.Topic != tc.wantTopic {
				t.Errorf("Topic=%q", got.Topic)
			}
			if got.Handler != tc.wantHandler {
				t.Errorf("Handler=%q", got.Handler)
			}
			if got.Group != tc.wantGroup {
				t.Errorf("Group=%q", got.Group)
			}
		})
	}
}

// ---- buildBundle dispatch closed-set -------------------------------------

func TestBuildBundle_Dispatch(t *testing.T) {
	t.Parallel()
	t.Run("all three marker types round-trip", func(t *testing.T) {
		t.Parallel()
		markers := []collectedMarker{
			makeMarker("cell:listener", "ref=cell.PrimaryListener,prefix=/api/v1"),
			makeFieldMarker("slice:route", "slice=ordercreate,subPath=/orders", "CreateHandler"),
			makeFieldMarker("slice:subscribe", "slice=sub,topic=t,handler=H,group=g", "SubField"),
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
		if len(bundle.Subscribes) != 1 || bundle.Subscribes[0].Topic != "t" {
			t.Errorf("subscribes=%v", bundle.Subscribes)
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
