package contractgen

import (
	"strings"
	"testing"
)

// TestApplyResponseProjection_FailClosed pins the fail-closed branches of the
// responseProjection rewrite (epic #1337 PR-12): a marker set on a response that
// has no projectable `data` resource is a codegen error, never a silent no-op —
// so a misplaced marker can never produce an un-guarded full view. Also covers
// the marker-off no-op and the happy single/list rewrites.
func TestApplyResponseProjection_FailClosed(t *testing.T) {
	mk := func(dtos []DTOSpec) *ContractGenSpec {
		return &ContractGenSpec{
			ContractID: "http.test.x.v1",
			Endpoint:   &httpEndpointSpec{ResponseProjection: true},
			DTOs:       dtos,
		}
	}
	// respWith builds a single-field Response DTO for the fail-closed cases.
	respWith := func(field, goType string) []DTOSpec {
		return []DTOSpec{{Name: "Response", Fields: []DTOField{{Name: field, GoType: goType}}}}
	}
	errCases := []struct {
		name    string
		spec    *ContractGenSpec
		wantErr string
	}{
		{"no Response DTO", mk([]DTOSpec{{Name: "Request"}}), "no Response DTO"},
		{"Response has no Data field", mk(respWith("Other", "string")), "no `data` resource field"},
		{"data not projectable (scalar)", mk(respWith("Data", "string")), "object or array-of-object"},
		{"item DTO missing", mk(respWith("Data", "*ResponseData")), "item DTO"},
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
			{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "*ResponseData"}}},
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
			{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "[]*ResponseDataItem"}}},
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
			DTOs:     []DTOSpec{{Name: "Response", Fields: []DTOField{{Name: "Data", GoType: "*Foo"}}}},
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

// TestProjectionItemType pins the resource-item extraction: array-of-pointer and
// single-pointer are the only projectable shapes schemaGoType emits for an
// object-valued `data` field; anything else is rejected (fail-closed in caller).
func TestProjectionItemType(t *testing.T) {
	cases := []struct {
		in         string
		wantItem   string
		wantIsList bool
		wantOK     bool
	}{
		{"[]*Foo", "Foo", true, true},
		{"*Foo", "Foo", false, true},
		{"string", "", false, false},
		{"[]string", "", false, false},
		{"int64", "", false, false},
	}
	for _, c := range cases {
		item, isList, ok := projectionItemType(c.in)
		if item != c.wantItem || isList != c.wantIsList || ok != c.wantOK {
			t.Errorf("projectionItemType(%q) = (%q, %v, %v), want (%q, %v, %v)",
				c.in, item, isList, ok, c.wantItem, c.wantIsList, c.wantOK)
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
