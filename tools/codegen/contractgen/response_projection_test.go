package contractgen

import (
	"strings"
	"testing"
)

// TestOmitEmpty_JSONTag_Invariant verifies that DTOField.OmitEmpty and the
// presence of ",omitempty" in DTOField.JSONTag are always co-derived: both come
// from !required in the JSON schema (via collectDTOs / schemaToDTOs). A field
// that is in the schema required list must have OmitEmpty=false and a JSONTag
// without ",omitempty", and vice versa for optional fields.
//
// This round-trip test guards against future divergence if nullable fields or a
// new schema concept introduces a path where one is set without the other.
func TestOmitEmpty_JSONTag_Invariant(t *testing.T) {
	t.Parallel()
	root := &Schema{
		Type:          "object",
		Required:      []string{"id", "score"},
		PropertyOrder: []string{"id", "score", "label", "count", "tags"},
		Properties: map[string]*Schema{
			"id":    {Type: "string"},
			"score": {Type: "integer"},
			"label": {Type: "string"},
			"count": {Type: "integer"},
			"tags":  {Type: "array", Items: &Schema{Type: "string"}},
		},
	}
	dtos, err := schemaToDTOs("ResponseData", root)
	if err != nil {
		t.Fatalf("schemaToDTOs: %v", err)
	}
	if len(dtos) == 0 {
		t.Fatal("expected at least one DTO")
	}
	for _, f := range dtos[0].Fields {
		hasOmitEmpty := strings.Contains(f.JSONTag, ",omitempty")
		if f.OmitEmpty != hasOmitEmpty {
			t.Errorf("field %s: OmitEmpty=%v but JSONTag %q has omitempty=%v — both must co-derive from !required",
				f.Name, f.OmitEmpty, f.JSONTag, hasOmitEmpty)
		}
	}
}

// TestApplyResponseProjection_FailClosed pins the fail-closed branches of the
// responseProjection rewrite (epic #1337 PR-12): a marker set on a response that
// has no projectable `data` resource is a codegen error, never a silent no-op —
// so a misplaced marker can never produce an un-guarded full view. Also covers
// the marker-off no-op and the happy single/list rewrites. Projectability is now
// read from the structured DTOField.ItemDTO / IsList (set in collectDTOs), not
// parsed from the rendered GoType string (F5).
func TestApplyResponseProjection_FailClosed(t *testing.T) {
	mk := func(dtos []DTOSpec) *ContractGenSpec {
		return &ContractGenSpec{
			ContractID: "http.test.x.v1",
			Endpoint:   &httpEndpointSpec{ResponseProjection: true},
			DTOs:       dtos,
		}
	}
	// dataField builds a single-field Response DTO whose field carries the given
	// structured projection metadata (itemDTO empty ⇒ not projectable).
	dataField := func(name, goType, itemDTO string, isList bool) []DTOSpec {
		return []DTOSpec{{Name: "Response", Fields: []DTOField{
			{Name: name, GoType: goType, ItemDTO: itemDTO, IsList: isList},
		}}}
	}
	errCases := []struct {
		name    string
		spec    *ContractGenSpec
		wantErr string
	}{
		{"no Response DTO", mk([]DTOSpec{{Name: "Request"}}), "no Response DTO"},
		{"Response has no Data field", mk(dataField("Other", "string", "", false)), "no `data` resource field"},
		{"data not projectable (scalar)", mk(dataField("Data", "string", "", false)), "object or array-of-object"},
		{"item DTO not found in DTOs", mk(dataField("Data", "*ResponseData", "ResponseData", false)), "item DTO"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			err := applyResponseProjection(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	t.Run("happy single object", func(t *testing.T) {
		spec := mk([]DTOSpec{
			{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "*ResponseData", ItemDTO: "ResponseData"}}},
			{Name: "ResponseData"},
		})
		if err := applyResponseProjection(spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := spec.DTOs[0].Fields[0].GoType; got != "projection.ResourceProjection" {
			t.Errorf("single Data GoType = %q, want projection.ResourceProjection", got)
		}
		if !spec.DTOs[1].EmitToMap {
			t.Error("item DTO ResponseData must be flagged EmitToMap")
		}
	})

	t.Run("happy list", func(t *testing.T) {
		spec := mk([]DTOSpec{
			{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "[]*ResponseDataItem", ItemDTO: "ResponseDataItem", IsList: true}}},
			{Name: "ResponseDataItem"},
		})
		if err := applyResponseProjection(spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := spec.DTOs[0].Fields[0].GoType; got != "[]projection.ResourceProjection" {
			t.Errorf("list Data GoType = %q, want []projection.ResourceProjection", got)
		}
		if !spec.DTOs[1].EmitToMap {
			t.Error("item DTO ResponseDataItem must be flagged EmitToMap")
		}
	})

	t.Run("marker off is a no-op", func(t *testing.T) {
		spec := &ContractGenSpec{
			Endpoint: &httpEndpointSpec{ResponseProjection: false},
			DTOs:     []DTOSpec{{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "*Foo", ItemDTO: "Foo"}}}},
		}
		if err := applyResponseProjection(spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.DTOs[0].Fields[0].GoType != "*Foo" {
			t.Error("marker-off must not rewrite Data")
		}
	})

	t.Run("nil endpoint is a no-op", func(t *testing.T) {
		spec := &ContractGenSpec{Endpoint: nil}
		if err := applyResponseProjection(spec); err != nil {
			t.Fatalf("nil endpoint must be a no-op, got %v", err)
		}
	})
}

// TestCollectDTOs_ProjectionItemMetadata pins the STRUCTURED projection-item
// derivation at its source: collectDTOs must set DTOField.ItemDTO (the generated
// item DTO name) and IsList directly from the schema shape, so the downstream
// applyResponseProjection never has to re-parse a rendered Go type string (F5).
// A single object and an array-of-object are projectable (ItemDTO set); a scalar
// and an array-of-scalar are not (ItemDTO empty), while IsList still tracks the
// array-ness independently.
func TestCollectDTOs_ProjectionItemMetadata(t *testing.T) {
	str := &Schema{Type: "string"}
	obj := &Schema{Type: "object", PropertyOrder: []string{"id"}, Properties: map[string]*Schema{"id": str}}
	root := &Schema{
		Type:          "object",
		PropertyOrder: []string{"single", "list", "scalar", "scalarList"},
		Properties: map[string]*Schema{
			"single":     obj,                         // object → projectable, not a list
			"list":       {Type: "array", Items: obj}, // array-of-object → projectable list
			"scalar":     str,                         // scalar → not projectable
			"scalarList": {Type: "array", Items: str}, // array-of-scalar → not projectable, IS a list
		},
	}
	dtos, err := schemaToDTOs("Response", root)
	if err != nil {
		t.Fatalf("schemaToDTOs: %v", err)
	}
	byName := map[string]DTOField{}
	for _, f := range dtos[0].Fields {
		byName[f.Name] = f
	}
	cases := []struct {
		field      string
		wantItem   string
		wantIsList bool
	}{
		{"Single", "ResponseSingle", false},
		{"List", "ResponseListItem", true},
		{"Scalar", "", false},
		{"ScalarList", "", true},
	}
	for _, c := range cases {
		f := byName[c.field]
		if f.ItemDTO != c.wantItem || f.IsList != c.wantIsList {
			t.Errorf("field %s: ItemDTO=%q IsList=%v, want ItemDTO=%q IsList=%v",
				c.field, f.ItemDTO, f.IsList, c.wantItem, c.wantIsList)
		}
	}
}

// TestIndexOfDTO covers the hit and miss paths of the DTO lookup helper.
func TestIndexOfDTO(t *testing.T) {
	dtos := []DTOSpec{{Name: "Request"}, {Name: "Response"}}
	if got := indexOfDTO(dtos, "Response"); got != 1 {
		t.Errorf("indexOfDTO(Response) = %d, want 1", got)
	}
	if got := indexOfDTO(dtos, "Missing"); got != -1 {
		t.Errorf("indexOfDTO(Missing) = %d, want -1", got)
	}
}

// TestCollectDTOs_OmitEmptyField verifies that DTOField.OmitEmpty is set for
// non-required fields and unset for required fields. This is the source signal
// that drives the conditional ToMap entry generation.
func TestCollectDTOs_OmitEmptyField(t *testing.T) {
	root := &Schema{
		Type:          "object",
		Required:      []string{"id"},
		PropertyOrder: []string{"id", "label", "count"},
		Properties: map[string]*Schema{
			"id":    {Type: "string"},
			"label": {Type: "string"},
			"count": {Type: "integer"},
		},
	}
	dtos, err := schemaToDTOs("ResponseData", root)
	if err != nil {
		t.Fatalf("schemaToDTOs: %v", err)
	}
	if len(dtos) == 0 {
		t.Fatal("expected at least one DTO")
	}
	byName := map[string]DTOField{}
	for _, f := range dtos[0].Fields {
		byName[f.Name] = f
	}
	cases := []struct {
		field         string
		wantOmitEmpty bool
	}{
		{"ID", false},   // in required → no omitempty
		{"Label", true}, // not required → omitempty
		{"Count", true}, // not required → omitempty
	}
	for _, c := range cases {
		f, ok := byName[c.field]
		if !ok {
			t.Errorf("field %s not found", c.field)
			continue
		}
		if f.OmitEmpty != c.wantOmitEmpty {
			t.Errorf("field %s: OmitEmpty=%v, want %v", c.field, f.OmitEmpty, c.wantOmitEmpty)
		}
	}
}

// TestToMap_OmitEmptyBehavior verifies that the generated ToMap omits zero-value
// optional fields, matching the struct json.Marshal serialization path (the delta
// bug in #2159). This test operates at the render level: it builds a spec that has
// an EmitToMap DTO with mixed required/optional fields, renders types.tmpl, and
// checks the rendered output for conditional guards.
//
// Covered types:
//   - string (required and optional)
//   - []T slice (optional) → len() > 0 guard
//   - *T pointer (optional) → != nil guard
//   - int64 optional (zero value 0) → != 0 guard
//   - any optional (nil is zero value) → != nil guard
//
// Masking compatibility: ToMap omitting zero-value optional keys is semantically
// identical to json.Marshal with omitempty, so the masking funnel
// (projection.NewProjection / NewProjectionList) receives a map where absent
// optional keys match the wire-serialized struct. Masking is an allowlist filter
// (only removes disallowed keys, never adds), so a "missing key" cannot be
// misinterpreted as "key present but masked" — projection omitempty is a safe
// wire-alignment, not a masking bypass.
func TestToMap_OmitEmptyBehavior(t *testing.T) {
	// Build a minimal spec with an EmitToMap DTO that covers all type branches of
	// omitEmptyCheck: string, []T, *T, int64, any.
	spec := &ContractGenSpec{
		PackageName: "testpkg",
		ContractID:  "http.test.x.v1",
		Kind:        "http",
		Endpoint:    &httpEndpointSpec{ResponseProjection: true},
		DTOs: []DTOSpec{
			{
				Name: "Response",
				Fields: []DTOField{
					{Name: "Data", JSONTag: "data", BareJSONTag: "data", GoType: "projection.ResourceProjection"},
				},
			},
			{
				Name:      "ResponseData",
				EmitToMap: true,
				Fields: []DTOField{
					{Name: "ID", JSONTag: "id", BareJSONTag: "id", GoType: "string", OmitEmpty: false},
					{Name: "Description", JSONTag: "description,omitempty", BareJSONTag: "description", GoType: "string", OmitEmpty: true},
					{Name: "Tags", JSONTag: "tags,omitempty", BareJSONTag: "tags", GoType: "[]string", OmitEmpty: true},
					{Name: "Meta", JSONTag: "meta,omitempty", BareJSONTag: "meta", GoType: "*ResponseDataMeta", OmitEmpty: true},
					// int64 optional: zero value is 0, must use != 0 guard.
					{Name: "Count", JSONTag: "count,omitempty", BareJSONTag: "count", GoType: "int64", OmitEmpty: true},
					// any optional: nil is the zero value, must use != nil guard.
					{Name: "Payload", JSONTag: "payload,omitempty", BareJSONTag: "payload", GoType: "any", OmitEmpty: true},
				},
			},
		},
	}

	out, err := renderTypes(spec)
	if err != nil {
		t.Fatalf("renderTypes: %v", err)
	}
	rendered := string(out)

	// Required field must be unconditional in the initial map literal.
	if !strings.Contains(rendered, `"id": i.ID,`) {
		t.Error(`want unconditional "id": i.ID, in map literal`)
	}
	// Optional string field must use != "" guard.
	if !strings.Contains(rendered, `i.Description != ""`) {
		t.Error(`want conditional guard i.Description != ""`)
	}
	// Optional slice field must use len() > 0 guard.
	if !strings.Contains(rendered, `len(i.Tags) > 0`) {
		t.Error(`want conditional guard len(i.Tags) > 0`)
	}
	// Optional pointer field must use != nil guard.
	if !strings.Contains(rendered, `i.Meta != nil`) {
		t.Error(`want conditional guard i.Meta != nil`)
	}
	// Optional int64 field must use != 0 guard (zero value is 0, not nil).
	if !strings.Contains(rendered, `i.Count != 0`) {
		t.Error(`want conditional guard i.Count != 0 for int64 optional field`)
	}
	// Optional any field must use != nil guard (nil is the zero value for any).
	if !strings.Contains(rendered, `i.Payload != nil`) {
		t.Error(`want conditional guard i.Payload != nil for any optional field`)
	}
	// Optional fields must NOT appear unconditionally in the map literal.
	if strings.Contains(rendered, `"description": i.Description,`) {
		t.Error(`must not have unconditional "description" entry in map literal`)
	}
	if strings.Contains(rendered, `"count": i.Count,`) {
		t.Error(`must not have unconditional "count" entry in map literal`)
	}
	if strings.Contains(rendered, `"payload": i.Payload,`) {
		t.Error(`must not have unconditional "payload" entry in map literal`)
	}
}

// TestToMap_MaskingCompatibility verifies that ToMap omitting optional zero-value
// keys is compatible with the masking funnel (projection.NewProjection /
// NewProjectionList). The masking funnel is an allowlist filter: it removes keys
// NOT in the allowed set, but never adds keys. Therefore:
//
//   - A key absent from ToMap (omitted because the field is zero) cannot be
//     "unmasked" by the funnel — the funnel only deletes, never inserts.
//   - A key present in ToMap (non-zero optional or required) follows normal masking.
//
// This confirms that omitEmptyCheck behavior is a safe wire-alignment with
// json.Marshal omitempty semantics, not a masking bypass or a masking gap.
// The generated ToMap and json.Marshal both omit zero optional keys, so the
// projected field set via the masking funnel equals the json-serialized set.
func TestToMap_MaskingCompatibility(t *testing.T) {
	t.Parallel()

	// Render a spec with required + optional fields across types.
	spec := &ContractGenSpec{
		PackageName: "testpkg",
		ContractID:  "http.test.masking.v1",
		Kind:        "http",
		Endpoint:    &httpEndpointSpec{ResponseProjection: true},
		DTOs: []DTOSpec{
			{
				Name: "Response",
				Fields: []DTOField{
					{Name: "Data", JSONTag: "data", BareJSONTag: "data", GoType: "projection.ResourceProjection"},
				},
			},
			{
				Name:      "ResponseData",
				EmitToMap: true,
				Fields: []DTOField{
					{Name: "ID", JSONTag: "id", BareJSONTag: "id", GoType: "string", OmitEmpty: false},
					{Name: "Label", JSONTag: "label,omitempty", BareJSONTag: "label", GoType: "string", OmitEmpty: true},
					{Name: "Score", JSONTag: "score,omitempty", BareJSONTag: "score", GoType: "int64", OmitEmpty: true},
				},
			},
		},
	}

	out, err := renderTypes(spec)
	if err != nil {
		t.Fatalf("renderTypes: %v", err)
	}
	rendered := string(out)

	// The required key is always in the map — masking can allow or deny it.
	if !strings.Contains(rendered, `"id": i.ID,`) {
		t.Error(`required field "id" must be unconditional in ToMap`)
	}

	// Optional keys are conditionally present — the masking funnel receives
	// a key only when the field is non-zero, matching json.Marshal omitempty.
	// Masking then filters this set; it cannot add a key that ToMap omitted.
	if !strings.Contains(rendered, `i.Label != ""`) {
		t.Error(`optional string field must use != "" guard for masking-compatible omitempty`)
	}
	if !strings.Contains(rendered, `i.Score != 0`) {
		t.Error(`optional int64 field must use != 0 guard for masking-compatible omitempty`)
	}

	// Sanity: optional fields must NOT appear unconditionally (would bypass omitempty).
	if strings.Contains(rendered, `"label": i.Label,`) {
		t.Error(`optional "label" must not be unconditional in ToMap — would diverge from json.Marshal omitempty`)
	}
	if strings.Contains(rendered, `"score": i.Score,`) {
		t.Error(`optional "score" must not be unconditional in ToMap — would diverge from json.Marshal omitempty`)
	}
}
