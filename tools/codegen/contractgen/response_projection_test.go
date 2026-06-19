package contractgen

import (
	"strings"
	"testing"
)

// TestNullableField_JSONTag_GoType pins the nullable-column codegen (#1875): a
// column declared `type: ["<scalar>", "null"]` becomes a POINTER GoType and DROPS
// the ",omitempty" tag suffix (it is always present on the wire, serializing as
// JSON null when nil), while a plain optional column keeps ",omitempty" and its
// value GoType. This is the source signal that lets the full-column-set ToMap emit
// a schema-valid null for a format-constrained optional column without re-opening
// the masking presence side channel.
func TestNullableField_JSONTag_GoType(t *testing.T) {
	t.Parallel()
	root := &Schema{
		Type:          "object",
		Required:      []string{"id"},
		PropertyOrder: []string{"id", "label", "occurredAt"},
		Properties: map[string]*Schema{
			"id":         {Type: "string"},
			"label":      {Type: "string"},                                      // plain optional → ",omitempty", string
			"occurredAt": {Type: "string", Format: "date-time", Nullable: true}, // nullable → *string, no omitempty
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
	if f := byName["OccurredAt"]; f.GoType != "*string" || f.Nullable != true || strings.Contains(f.JSONTag, ",omitempty") {
		t.Errorf("OccurredAt: GoType=%q Nullable=%v JSONTag=%q — want *string, Nullable=true, no omitempty",
			f.GoType, f.Nullable, f.JSONTag)
	}
	if f := byName["Label"]; f.GoType != "string" || f.Nullable != false || !strings.Contains(f.JSONTag, ",omitempty") {
		t.Errorf("Label: GoType=%q Nullable=%v JSONTag=%q — want string, Nullable=false, ,omitempty",
			f.GoType, f.Nullable, f.JSONTag)
	}
}

// TestNullableField_GoType_PointerEdges pins the pointer-derivation edges of a
// nullable column (#1875): (a) a nullable named string enum becomes *<EnumType>
// (the pointer wraps the named type, not a bare string); (b) a nullable OPTIONAL
// bool stays *bool — NOT **bool — because the optional-bool→*bool conversion
// already made it a pointer and the nullable guard must not double up.
func TestNullableField_GoType_PointerEdges(t *testing.T) {
	t.Parallel()
	root := &Schema{
		Type:          "object",
		Required:      []string{"id"},
		PropertyOrder: []string{"id", "status", "flag"},
		Properties: map[string]*Schema{
			"id":     {Type: "string"},
			"status": {Type: "string", Enum: []string{"active", "inactive"}, Nullable: true}, // nullable enum → *ResponseDataStatus
			"flag":   {Type: "boolean", Nullable: true},                                      // nullable optional bool → *bool (not **bool)
		},
	}
	dtos, err := schemaToDTOs("ResponseData", root)
	if err != nil {
		t.Fatalf("schemaToDTOs: %v", err)
	}
	byName := map[string]DTOField{}
	for _, f := range dtos[0].Fields {
		byName[f.Name] = f
	}
	if f := byName["Status"]; f.GoType != "*ResponseDataStatus" || !f.Nullable {
		t.Errorf("Status: GoType=%q Nullable=%v — want *ResponseDataStatus, Nullable=true", f.GoType, f.Nullable)
	}
	if f := byName["Flag"]; f.GoType != "*bool" {
		t.Errorf("Flag: GoType=%q — want *bool (nullable optional bool must NOT become **bool)", f.GoType)
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

// TestApplyResponseProjection_RejectsOptionalColumn pins the #2359 invariant: a
// responseProjection item schema's `required` MUST list every item property (no
// optional / not-in-required column). The full-column-set ToMap (#1875) emits every
// key unconditionally, so an "optional" projection column is a category error — its
// key is never actually absent on the wire, yet schema-validating clients are told it
// MAY be absent, and its Go zero/nil value ("" / null) reaches the wire where it can
// silently violate the schema. The codegen build-time check (Hard) is the schema
// truth-source closure; its CI mirror is the archtest
// PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01.
func TestApplyResponseProjection_RejectsOptionalColumn(t *testing.T) {
	t.Parallel()
	// Response.Data -> ResponseData item DTO whose `label` column is NOT required.
	spec := &ContractGenSpec{
		ContractID: "http.test.optional.v1",
		Endpoint:   &httpEndpointSpec{ResponseProjection: true},
		DTOs: []DTOSpec{
			{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "*ResponseData", ItemDTO: "ResponseData"}}},
			{Name: "ResponseData", Fields: []DTOField{
				{Name: "ID", BareJSONTag: "id", GoType: "string", Required: true},
				{Name: "Label", BareJSONTag: "label", GoType: "string", Required: false}, // optional → violation
			}},
		},
	}
	err := applyResponseProjection(spec)
	if err == nil {
		t.Fatal("applyResponseProjection must reject a responseProjection item with an optional (not-in-required) column")
	}
	for _, want := range []string{"http.test.optional.v1", "label", "required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %q (contract id + offending column + the required-completeness reason)", err.Error(), want)
		}
	}
}

// TestApplyResponseProjection_AllRequiredColumnsOK is the GREEN control: an item DTO
// whose every column is required (including a required-nullable column, whose
// schema-valid "no value" is JSON null) passes the #2359 check and is flagged
// EmitToMap. A nullable column is `Required: true` in the schema-required sense (its
// key is always present) — value-optionality is expressed by Nullable, not by absence
// from `required`.
func TestApplyResponseProjection_AllRequiredColumnsOK(t *testing.T) {
	t.Parallel()
	spec := &ContractGenSpec{
		ContractID: "http.test.allrequired.v1",
		Endpoint:   &httpEndpointSpec{ResponseProjection: true},
		DTOs: []DTOSpec{
			{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "*ResponseData", ItemDTO: "ResponseData"}}},
			{Name: "ResponseData", Fields: []DTOField{
				{Name: "ID", BareJSONTag: "id", GoType: "string", Required: true},
				{Name: "OccurredAt", BareJSONTag: "occurredAt", GoType: "*string", Required: true, Nullable: true},
			}},
		},
	}
	if err := applyResponseProjection(spec); err != nil {
		t.Fatalf("all-required (incl. required-nullable) item must pass: %v", err)
	}
	if !spec.DTOs[1].EmitToMap {
		t.Error("item DTO must still be flagged EmitToMap on the happy path")
	}
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

// TestToMap_FullColumnSet verifies the generated ToMap emits the FULL, STABLE
// column set: a single `return map[string]any{ … }` literal with EVERY field
// present unconditionally and NO `if` guard. This is the Decision-2 stable-column
// invariant restored after the #2159 omitempty-fission regression (#1875): the
// masking funnel can only redact a key it can see, so an always-present column set
// is what keeps field presence from leaking whether a masked column held data.
// Nullable columns are pointers whose nil marshals to JSON null (schema-valid "no
// value" with the key still present and maskable).
//
// Scope (#2340 F3): this asserts column PRESENCE (the stable-column invariant). That
// each column's ZERO VALUE is schema-valid on the wire is proven at the contract
// level — TestHttpAuditListV1Serve_ZeroOccurredAt_NullValidates validates a full
// zero-value row against the real response schema (string→"", date-time→null). The
// systematic guard that no optional projection column can carry a schema-invalid zero
// is now closed (#2359): requireProjectionItemFullColumnSet (codegen build-fail) +
// archtest PROJECTION-OPTIONAL-COLUMN-ZERO-SCHEMA-VALID-01 forbid any optional
// (not-in-`required`) projection column, so value-optionality is expressible only as a
// schema-valid nullable. The residual (a required array/object whose producer emits
// nil→null) is documented in ADR 202606112000-1350 §Amendment #2359.
func TestToMap_FullColumnSet(t *testing.T) {
	t.Parallel()

	// This spec is rendered directly via renderTypes — it does NOT pass through
	// applyResponseProjection, so the optional (Required:false) columns below are
	// intentional ToMap-rendering inputs and do not trip requireProjectionItemFullColumnSet
	// (#2359). The point here is that ToMap emits EVERY column unconditionally regardless
	// of omitempty; full-column-set required-completeness is covered separately by
	// TestApplyResponseProjection_RejectsOptionalColumn.
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
					{Name: "ID", JSONTag: "id", BareJSONTag: "id", GoType: "string"},                                          // required
					{Name: "Description", JSONTag: "description,omitempty", BareJSONTag: "description", GoType: "string"},     // plain optional
					{Name: "Tags", JSONTag: "tags,omitempty", BareJSONTag: "tags", GoType: "[]string"},                        // optional slice
					{Name: "Count", JSONTag: "count,omitempty", BareJSONTag: "count", GoType: "int64"},                        // optional int64
					{Name: "Payload", JSONTag: "payload,omitempty", BareJSONTag: "payload", GoType: "any"},                    // optional any
					{Name: "OccurredAt", JSONTag: "occurredAt", BareJSONTag: "occurredAt", GoType: "*string", Nullable: true}, // nullable
				},
			},
		},
	}

	out, err := renderTypes(spec)
	if err != nil {
		t.Fatalf("renderTypes: %v", err)
	}
	// gofmt aligns map-literal values with padding, so normalize whitespace to
	// single spaces before substring matching.
	norm := strings.Join(strings.Fields(string(out)), " ")

	// Every field — required, plain optional, AND nullable — is an unconditional entry.
	for _, want := range []string{
		`"id": i.ID,`,
		`"description": i.Description,`,
		`"tags": i.Tags,`,
		`"count": i.Count,`,
		`"payload": i.Payload,`,
		`"occurredAt": i.OccurredAt,`,
	} {
		if !strings.Contains(norm, want) {
			t.Errorf("ToMap must contain unconditional entry %q (full column set)", want)
		}
	}

	// NO conditional omission anywhere in the generated ToMap — the omitempty
	// fission shape (#2159) must never reappear. Scope the search to the ToMap
	// method body only (up to the next top-level func), not the whole file.
	start := strings.Index(norm, "func (i ResponseData) ToMap()")
	if start < 0 {
		t.Fatal("rendered output is missing the ResponseData ToMap method")
	}
	body := norm[start:]
	if next := strings.Index(body[1:], "func "); next >= 0 {
		body = body[:next+1]
	}
	if strings.Contains(body, "if ") {
		t.Error("ToMap must not contain any `if` guard — full column set is unconditional (#1875)")
	}
}
