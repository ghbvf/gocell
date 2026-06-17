package contractgen

import "testing"

// TestParse_NullableType pins the `type: ["<scalar>", "null"]` JSON-Schema
// 2020-12 form: it is the ONLY array-form `type` contractgen accepts, and it
// sets Schema.Nullable=true while keeping Schema.Type the scalar. This is the
// authoring surface for an optional column whose "no value" must serialize as
// schema-valid JSON `null` (not "" — which violates a `format` constraint) while
// the column stays present in the masked view (#1875). Any other array-form
// `type` stays an unsupported-keyword error.
func TestParse_NullableType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		typeJSON     string
		wantType     string
		wantNullable bool
		wantErr      bool
	}{
		{name: "scalar then null", typeJSON: `["string", "null"]`, wantType: "string", wantNullable: true},
		{name: "null then scalar", typeJSON: `["null", "string"]`, wantType: "string", wantNullable: true},
		{name: "integer nullable", typeJSON: `["integer", "null"]`, wantType: "integer", wantNullable: true},
		{name: "boolean nullable", typeJSON: `["boolean", "null"]`, wantType: "boolean", wantNullable: true},
		{name: "number nullable", typeJSON: `["number", "null"]`, wantType: "number", wantNullable: true},
		{name: "plain string not nullable", typeJSON: `"string"`, wantType: "string", wantNullable: false},
		{name: "two real types still unsupported", typeJSON: `["string", "integer"]`, wantErr: true},
		{name: "single-element array unsupported", typeJSON: `["string"]`, wantErr: true},
		{name: "null only unsupported", typeJSON: `["null"]`, wantErr: true},
		// #2340 F1: only scalars may be nullable — object/array/typo fail fast
		// rather than degrade to an `any` GoType.
		{name: "nullable object unsupported", typeJSON: `["object", "null"]`, wantErr: true},
		{name: "nullable array unsupported", typeJSON: `["array", "null"]`, wantErr: true},
		{name: "scalar typo unsupported", typeJSON: `["strnig", "null"]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSchema(t, dir, "s.json", `{
				"type": "object",
				"properties": {
					"col": {"type": `+tc.typeJSON+`}
				}
			}`)
			s, err := parseFromDir(t, dir, "s.json")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for type %s, got nil", tc.typeJSON)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			col := s.Properties["col"]
			if col.Type != tc.wantType {
				t.Errorf("col.Type: got %q, want %q", col.Type, tc.wantType)
			}
			if col.Nullable != tc.wantNullable {
				t.Errorf("col.Nullable: got %v, want %v", col.Nullable, tc.wantNullable)
			}
		})
	}
}
