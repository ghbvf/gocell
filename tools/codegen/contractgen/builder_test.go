package contractgen

import (
	"strings"
	"testing"
)

// --- Naming helper tests ---

func TestGoPascalCase(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"create", "Create"},
		{"order-created", "OrderCreated"},
		{"user_id", "UserId"},
		{"get", "Get"},
		{"list", "List"},
		{"orderGet", "OrderGet"},
		{"a", "A"},
		{"", ""},
		{"item-sub-type", "ItemSubType"},
	}
	for _, c := range cases {
		got := goPascalCase(c.in)
		if got != c.want {
			t.Errorf("goPascalCase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGoPackageName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"create", "create"},
		{"order-created", "ordercreated"},
		{"v1", "v1"},
		{"OrderCreated", "ordercreated"},
		{"item_list", "itemlist"},
	}
	for _, c := range cases {
		got := goPackageName(c.in)
		if got != c.want {
			t.Errorf("goPackageName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContractIDToPackagePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http.order.create.v1", "generated/contracts/http/order/create/v1"},
		{"http.order.get.v1", "generated/contracts/http/order/get/v1"},
		{"http.order.list.v1", "generated/contracts/http/order/list/v1"},
		{"event.order-created.v1", "generated/contracts/event/order-created/v1"},
		{"event.item-created.v1", "generated/contracts/event/item-created/v1"},
	}
	for _, c := range cases {
		got := contractIDToPackagePath(c.in)
		if got != c.want {
			t.Errorf("contractIDToPackagePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDomainLastSegment(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http.order.create.v1", "create"},
		{"http.order.get.v1", "get"},
		{"http.order.list.v1", "list"},
		{"event.order-created.v1", "order-created"},
		{"event.item-created.v1", "item-created"},
	}
	for _, c := range cases {
		got := domainLastSegment(c.in)
		if got != c.want {
			t.Errorf("domainLastSegment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripVersionSuffix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"event.order-created.v1", "event.order-created"},
		{"http.order.create.v1", "http.order.create"},
		{"event.item-created.v2", "event.item-created"},
		{"event.order-created", "event.order-created"},
	}
	for _, c := range cases {
		got := stripVersionSuffix(c.in)
		if got != c.want {
			t.Errorf("stripVersionSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPathParamNamesFromPath(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"/api/v1/orders/{id}", []string{"id"}},
		{"/api/v1/items/{itemId}/sub/{subId}", []string{"itemId", "subId"}},
		{"/api/v1/orders/", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := pathParamNamesFromPath(c.in)
		if len(got) != len(c.want) {
			t.Errorf("pathParamNamesFromPath(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("pathParamNamesFromPath(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// --- schemaToDTOs tests ---

func TestSchemaToDTOs_SimpleObject(t *testing.T) {
	s := &Schema{
		Type:          "object",
		Title:         "test request",
		PropertyOrder: []string{"name", "age"},
		Properties: map[string]*Schema{
			"name": {Type: "string"},
			"age":  {Type: "integer"},
		},
		Required: []string{"name"},
	}
	dtos, err := schemaToDTOs("Request", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dtos) != 1 {
		t.Fatalf("expected 1 DTO, got %d", len(dtos))
	}
	dto := dtos[0]
	if dto.Name != "Request" {
		t.Errorf("name = %q, want Request", dto.Name)
	}
	if len(dto.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(dto.Fields))
	}
	// name is required — no omitempty
	if dto.Fields[0].JSONTag != "name" {
		t.Errorf("fields[0].JSONTag = %q, want %q", dto.Fields[0].JSONTag, "name")
	}
	// age is optional — has omitempty
	if dto.Fields[1].JSONTag != "age,omitempty" {
		t.Errorf("fields[1].JSONTag = %q, want %q", dto.Fields[1].JSONTag, "age,omitempty")
	}
}

func TestSchemaToDTOs_NestedObject(t *testing.T) {
	s := &Schema{
		Type:          "object",
		Title:         "response",
		PropertyOrder: []string{"data"},
		Properties: map[string]*Schema{
			"data": {
				Type:          "object",
				PropertyOrder: []string{"id", "name"},
				Properties: map[string]*Schema{
					"id":   {Type: "string"},
					"name": {Type: "string"},
				},
				Required: []string{"id", "name"},
			},
		},
		Required: []string{"data"},
	}
	dtos, err := schemaToDTOs("Response", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should produce Response + ResponseData
	if len(dtos) != 2 {
		t.Fatalf("expected 2 DTOs, got %d: %v", len(dtos), dtoNames(dtos))
	}
	if dtos[0].Name != "Response" {
		t.Errorf("dtos[0].Name = %q, want Response", dtos[0].Name)
	}
	if dtos[1].Name != "ResponseData" {
		t.Errorf("dtos[1].Name = %q, want ResponseData", dtos[1].Name)
	}
	// Response.Data should be *ResponseData
	if dtos[0].Fields[0].GoType != "*ResponseData" {
		t.Errorf("Response.Data GoType = %q, want *ResponseData", dtos[0].Fields[0].GoType)
	}
}

func TestSchemaToDTOs_ArrayOfObject(t *testing.T) {
	s := &Schema{
		Type:          "object",
		PropertyOrder: []string{"items"},
		Properties: map[string]*Schema{
			"items": {
				Type: "array",
				Items: &Schema{
					Type:          "object",
					PropertyOrder: []string{"id"},
					Properties: map[string]*Schema{
						"id": {Type: "string"},
					},
					Required: []string{"id"},
				},
			},
		},
		Required: []string{"items"},
	}
	dtos, err := schemaToDTOs("Response", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Response + ResponseItemsItem (array of object items)
	if len(dtos) != 2 {
		t.Fatalf("expected 2 DTOs, got %d: %v", len(dtos), dtoNames(dtos))
	}
	if dtos[0].Name != "Response" {
		t.Errorf("dtos[0].Name = %q, want Response", dtos[0].Name)
	}
	// The array field GoType should be []*ResponseItemsItem
	if !strings.HasPrefix(dtos[0].Fields[0].GoType, "[]*") {
		t.Errorf("items field GoType = %q, want []*... prefix", dtos[0].Fields[0].GoType)
	}
}

func TestSchemaToDTOs_NonObjectRoot(t *testing.T) {
	s := &Schema{Type: "string"}
	_, err := schemaToDTOs("Request", s)
	if err == nil {
		t.Fatal("expected error for non-object root schema")
	}
}

func TestSchemaToDTOs_FormatHint(t *testing.T) {
	s := &Schema{
		Type:          "object",
		PropertyOrder: []string{"createdAt"},
		Properties: map[string]*Schema{
			"createdAt": {Type: "string", Format: "date-time"},
		},
		Required: []string{"createdAt"},
	}
	dtos, err := schemaToDTOs("Response", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dtos[0].Fields[0].Doc != "format: date-time" {
		t.Errorf("Doc = %q, want %q", dtos[0].Fields[0].Doc, "format: date-time")
	}
}

func TestSchemaToDTOs_EmptyObject(t *testing.T) {
	s := &Schema{Type: "object"}
	dtos, err := schemaToDTOs("Request", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dtos) != 1 {
		t.Fatalf("expected 1 DTO, got %d", len(dtos))
	}
	if len(dtos[0].Fields) != 0 {
		t.Errorf("expected 0 fields for empty object, got %d", len(dtos[0].Fields))
	}
}

// dtoNames returns names for display in test output.
func dtoNames(dtos []DTOSpec) []string {
	names := make([]string, len(dtos))
	for i, d := range dtos {
		names[i] = d.Name
	}
	return names
}
