package contractgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// --- Naming helper tests ---

func TestGoPascalCase(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"create", "Create"},
		{"order-created", "OrderCreated"},
		{"user_id", "UserID"},
		{"id", "ID"},
		{"api_key", "APIKey"},
		{"get", "Get"},
		{"list", "List"},
		{"orderGet", "OrderGet"},
		{"a", "A"},
		{"", ""},
		{"item-sub-type", "ItemSubType"},
		{"url", "URL"},
		{"http_status", "HTTPStatus"},
		// camelCase inputs (no underscore delimiter — must split on case boundary)
		{"eventId", "EventID"},
		{"userId", "UserID"},
		{"requestId", "RequestID"},
		{"httpStatus", "HTTPStatus"},
		{"urlPath", "URLPath"},
		{"name", "Name"},
		// mixed (underscore + camelCase in same token is uncommon but safe)
		{"event_id", "EventID"},
		// no initialism match — keep Title-case capitalisation
		{"iso8601", "Iso8601"},
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
		// "internal" path segment must be renamed to "internalapi" so generated
		// packages are importable from cells/ and examples/ (Go internal package rule).
		// Contract IDs and URL prefixes (/internal/v1/...) are unchanged.
		{"http.internal.devicecommands.list.v1", "generated/contracts/http/internalapi/devicecommands/list/v1"},
		{"http.config.internal.get.v1", "generated/contracts/http/config/internalapi/get/v1"},
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

// TestSchemaToDTOs_StringEnum_TopLevel covers a top-level string field carrying a
// closed value-set (#1935, mirrors event.devicecert-rotation-resolved.v1 outcome).
// The field type becomes the generated named type <Parent><Field> and the DTO
// carries an EnumSpec whose const names follow <TypeName><PascalCaseValue>.
func TestSchemaToDTOs_StringEnum_TopLevel(t *testing.T) {
	s := &Schema{
		Type:          "object",
		PropertyOrder: []string{"outcome"},
		Properties: map[string]*Schema{
			"outcome": {Type: "string", Enum: []string{"succeeded", "failed", "rejected"}},
		},
		Required: []string{"outcome"},
	}
	dtos, err := schemaToDTOs("Payload", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dtos) != 1 {
		t.Fatalf("expected 1 DTO, got %d: %v", len(dtos), dtoNames(dtos))
	}
	dto := dtos[0]
	if got := dto.Fields[0].GoType; got != "PayloadOutcome" {
		t.Errorf("outcome GoType = %q, want PayloadOutcome", got)
	}
	if len(dto.Enums) != 1 {
		t.Fatalf("expected 1 enum on Payload, got %d", len(dto.Enums))
	}
	en := dto.Enums[0]
	if en.TypeName != "PayloadOutcome" {
		t.Errorf("enum TypeName = %q, want PayloadOutcome", en.TypeName)
	}
	wantConsts := []EnumValue{
		{ConstName: "PayloadOutcomeSucceeded", Value: "succeeded"},
		{ConstName: "PayloadOutcomeFailed", Value: "failed"},
		{ConstName: "PayloadOutcomeRejected", Value: "rejected"},
	}
	if len(en.Values) != len(wantConsts) {
		t.Fatalf("enum Values = %v, want %v", en.Values, wantConsts)
	}
	for i, w := range wantConsts {
		if en.Values[i] != w {
			t.Errorf("Values[%d] = %+v, want %+v", i, en.Values[i], w)
		}
	}
}

// TestSchemaToDTOs_StringEnum_Nested covers a string enum on a nested object field
// (#1935, mirrors http.orderfulfillment.orderstatus.v1 data.status). The enum
// belongs to the nested DTO (ResponseData), proving the <Parent><Field> naming
// composes with the nested-object flattening (parent = ResponseData).
func TestSchemaToDTOs_StringEnum_Nested(t *testing.T) {
	s := &Schema{
		Type:          "object",
		PropertyOrder: []string{"data"},
		Properties: map[string]*Schema{
			"data": {
				Type:          "object",
				PropertyOrder: []string{"status"},
				Properties: map[string]*Schema{
					"status": {Type: "string", Enum: []string{"accepted", "running"}},
				},
				Required: []string{"status"},
			},
		},
		Required: []string{"data"},
	}
	dtos, err := schemaToDTOs("Response", s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Response (no enum) + ResponseData (carries the status enum).
	if len(dtos) != 2 {
		t.Fatalf("expected 2 DTOs, got %d: %v", len(dtos), dtoNames(dtos))
	}
	if len(dtos[0].Enums) != 0 {
		t.Errorf("Response should carry no enums, got %d", len(dtos[0].Enums))
	}
	rd := dtos[1]
	if rd.Name != "ResponseData" {
		t.Fatalf("dtos[1].Name = %q, want ResponseData", rd.Name)
	}
	if got := rd.Fields[0].GoType; got != "ResponseDataStatus" {
		t.Errorf("status GoType = %q, want ResponseDataStatus", got)
	}
	if len(rd.Enums) != 1 || rd.Enums[0].TypeName != "ResponseDataStatus" {
		t.Fatalf("expected ResponseDataStatus enum on ResponseData, got %+v", rd.Enums)
	}
	if rd.Enums[0].Values[0].ConstName != "ResponseDataStatusAccepted" {
		t.Errorf("first const = %q, want ResponseDataStatusAccepted", rd.Enums[0].Values[0].ConstName)
	}
}

// TestSchemaToDTOs_EnumConstNameCollision asserts two enum values that PascalCase
// to the same Go identifier (e.g. "in-progress" and "in_progress" → "InProgress")
// are a fail-fast error rather than a silently shadowed const (#1935).
func TestSchemaToDTOs_EnumConstNameCollision(t *testing.T) {
	s := &Schema{
		Type:          "object",
		PropertyOrder: []string{"phase"},
		Properties: map[string]*Schema{
			"phase": {Type: "string", Enum: []string{"in-progress", "in_progress"}},
		},
		Required: []string{"phase"},
	}
	_, err := schemaToDTOs("Payload", s)
	if err == nil {
		t.Fatal("expected error for colliding enum const names")
	}
	if !strings.Contains(err.Error(), "in-progress") || !strings.Contains(err.Error(), "PayloadPhaseInProgress") {
		t.Errorf("error should name the colliding values + const, got: %v", err)
	}
}

// TestSchemaToDTOs_ArrayOfEnum_Rejected asserts enum on array items is rejected
// fail-fast: schemaGoType would emit "[]<Parent><Field>" while no const block is
// collected, producing a reference to an undefined type (#1935 array-of-enum guard).
func TestSchemaToDTOs_ArrayOfEnum_Rejected(t *testing.T) {
	s := &Schema{
		Type:          "object",
		PropertyOrder: []string{"tags"},
		Properties: map[string]*Schema{
			"tags": {Type: "array", Items: &Schema{Type: "string", Enum: []string{"a", "b"}}},
		},
		Required: []string{"tags"},
	}
	_, err := schemaToDTOs("Payload", s)
	if err == nil {
		t.Fatal("expected error for enum on array items")
	}
	if !strings.Contains(err.Error(), "array items") || !strings.Contains(err.Error(), "tags") {
		t.Errorf("error should explain array-of-enum is unsupported, got: %v", err)
	}
}

// TestSchemaToDTOs_EnumInvalidConstName asserts enum values whose derived Go const
// identifier is illegal are rejected fail-fast at codegen time rather than emitted
// as un-buildable Go (#1935 F2). goPascalCase passes spaces/punctuation through and
// maps "" to "", so the const name must be guarded explicitly. Each error must name
// the offending wire value and field so the schema author can fix the source enum.
func TestSchemaToDTOs_EnumInvalidConstName(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		wantInErr string // a fragment proving the offending value is surfaced
	}{
		{"empty string → empty suffix shadows type", "", "empty Go const suffix"},
		{"punctuation → invalid identifier", "!", `"PayloadFlag!"`},
		{"embedded space → invalid identifier", "a b", `"PayloadFlagA b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Schema{
				Type:          "object",
				PropertyOrder: []string{"flag"},
				Properties: map[string]*Schema{
					"flag": {Type: "string", Enum: []string{tc.value}},
				},
				Required: []string{"flag"},
			}
			_, err := schemaToDTOs("Payload", s)
			if err == nil {
				t.Fatalf("expected error for enum value %q", tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error should contain %q, got: %v", tc.wantInErr, err)
			}
			// The field key must always be named so the author can locate the source.
			if !strings.Contains(err.Error(), `"flag"`) {
				t.Errorf("error should name the field %q, got: %v", "flag", err)
			}
		})
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

// --- pkgNameFromContractID tests ---

func TestPkgNameFromContractID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http.order.create.v1", "create"},
		{"http.order.get.v1", "get"},
		{"http.order.list.v1", "list"},
		{"event.order-created.v1", "ordercreated"},
		{"event.item-created.v1", "itemcreated"},
		{"http.audit.list.v2", "list"},
		// D1: keyword / builtin collision → prepend previous domain segment
		{"http.config.delete.v1", "configdelete"},
		{"http.user.range.v1", "userrange"},
		{"event.foo-bar.delete.v1", "foobardelete"},
		// D1: http stdlib collision
		{"http.gateway.http.v1", "gatewayhttp"},
		// 2-segment edge case (<3 parts): fallback uses raw last segment regardless of keyword.
		{"http.v1", "v1"}, // <3 parts → goPackageName(last) = "v1"
		// 3-segment with reserved penultimate and no preceding domain: appends "pkg".
		{"http.delete.v1", "deletepkg"}, // len=3, parts[-2]="delete" reserved, len<4 → "deletepkg"
	}
	for _, c := range cases {
		got := pkgNameFromContractID(c.in)
		if got != c.want {
			t.Errorf("pkgNameFromContractID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- mergeParamsIntoRequest conflict detection tests (A.6) ---

func TestMergeParamsIntoRequest_ConflictDetected(t *testing.T) {
	// Body schema has field "item"; path param also named "item" → conflict.
	existing := []DTOSpec{
		{
			Name: "Request",
			Fields: []DTOField{
				{Name: "Item", JSONTag: "item", GoType: "string"},
			},
		},
	}
	http := &metadata.HTTPTransportMeta{
		Method: "GET",
		Path:   "/api/v1/orders/{item}",
		PathParams: map[string]metadata.ParamSchema{
			"item": {Type: "string"},
		},
	}
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)
	_, err := mergeParamsIntoRequest(existing, pathParams, queryParams, nil, "http.test.conflict.v1")
	if err == nil {
		t.Fatal("expected error for field name conflict between path param and body schema")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error should mention 'conflict', got: %v", err)
	}
}

func TestMergeParamsIntoRequest_QueryConflictDetected(t *testing.T) {
	// Body schema has field "cursor"; query param also named "cursor" → conflict.
	existing := []DTOSpec{
		{
			Name: "Request",
			Fields: []DTOField{
				{Name: "Cursor", JSONTag: "cursor", GoType: "string"},
			},
		},
	}
	ptrFalse := false
	http := &metadata.HTTPTransportMeta{
		Method: "GET",
		Path:   "/api/v1/orders",
		QueryParams: map[string]metadata.ParamSchema{
			"cursor": {Type: "string", Required: &ptrFalse},
		},
	}
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)
	_, err := mergeParamsIntoRequest(existing, pathParams, queryParams, nil, "http.test.queryconflict.v1")
	if err == nil {
		t.Fatal("expected error for field name conflict between query param and body schema")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error should mention 'conflict', got: %v", err)
	}
}

func TestMergeParamsIntoRequest_NoConflict(t *testing.T) {
	// No conflict — different field names should succeed.
	existing := []DTOSpec{
		{
			Name: "Request",
			Fields: []DTOField{
				{Name: "Name", JSONTag: "name", GoType: "string"},
			},
		},
	}
	http := &metadata.HTTPTransportMeta{
		Method: "GET",
		Path:   "/api/v1/orders/{id}",
		PathParams: map[string]metadata.ParamSchema{
			"id": {Type: "string"},
		},
	}
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)
	dtos, err := mergeParamsIntoRequest(existing, pathParams, queryParams, nil, "http.test.noconflict.v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have ID (from path param) + Name (from body) in the Request DTO.
	req := dtos[0]
	if len(req.Fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(req.Fields))
	}
}

func TestMergeParamsIntoRequest_HeaderConflictDetected(t *testing.T) {
	// Body schema has field "tenant"; header param GoName also "Tenant" → conflict.
	existing := []DTOSpec{
		{
			Name: "Request",
			Fields: []DTOField{
				{Name: "Tenant", JSONTag: "tenant", GoType: "string"},
			},
		},
	}
	truthy := true
	http := &metadata.HTTPTransportMeta{
		Method: "POST",
		Path:   "/api/v1/access/sessions/login",
		Headers: map[string]metadata.ParamSchema{
			"Tenant": {Type: "string", Required: &truthy},
		},
	}
	headerParams := buildHeaderParams(http)
	_, err := mergeParamsIntoRequest(existing, nil, nil, headerParams, "http.test.headerconflict.v1")
	if err == nil {
		t.Fatal("expected error for field name conflict between header param and body schema")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error should mention 'conflict', got: %v", err)
	}
}

// TestMergeParamsIntoRequest_ParamVsParamCollision covers #1494 review F2: two
// params (here a path param and a header) that fold to the SAME goPascalCase Go
// field must error — not silently emit a duplicate Request field. The error must
// name BOTH colliding sources.
func TestMergeParamsIntoRequest_ParamVsParamCollision(t *testing.T) {
	pathParams := []ParamSpec{{Name: "tenantId", GoName: "TenantID", GoType: "string", Required: true}}
	headerParams := []ParamSpec{{Name: "X-Tenant-ID", GoName: "TenantID", GoType: "string", Required: true}}
	_, err := mergeParamsIntoRequest(nil, pathParams, nil, headerParams, "http.test.paramcollision.v1")
	if err == nil {
		t.Fatal("expected error for path/header params folding to the same Go field name")
	}
	msg := err.Error()
	for _, want := range []string{"conflict", "TenantID", `path param "tenantId"`, `header param "X-Tenant-ID"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("collision error must contain %q, got: %v", want, err)
		}
	}
}

// TestMergeParamsIntoRequest_PathVsQueryCollision covers the pre-existing
// path-vs-query GoName collision that #1494 review F2 generalised (the param
// merge previously only checked param-vs-body).
func TestMergeParamsIntoRequest_PathVsQueryCollision(t *testing.T) {
	pathParams := []ParamSpec{{Name: "id", GoName: "ID", GoType: "string", Required: true}}
	queryParams := []ParamSpec{{Name: "id", GoName: "ID", GoType: "string"}}
	_, err := mergeParamsIntoRequest(nil, pathParams, queryParams, nil, "http.test.pathquerycollision.v1")
	if err == nil {
		t.Fatal("expected error for path/query params folding to the same Go field name")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error should mention 'conflict', got: %v", err)
	}
}

// TestBuildHTTPSpec_NonStringHeaderFailsClosed covers #1494 review F1/F4: a
// non-string header type is rejected at codegen time (fail-closed) BEFORE any
// generation, sharing metadata.ValidateHTTPHeaders with governance FMT-40 — so a
// "legal contract → uncompilable Go" can never be produced even if `gocell
// validate` was skipped.
func TestBuildHTTPSpec_NonStringHeaderFailsClosed(t *testing.T) {
	contract := &metadata.ContractMeta{
		ID: "http.test.badheader.v1",
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/api/v1/test",
				Headers:       map[string]metadata.ParamSchema{"X-Count": {Type: "integer"}},
				SuccessStatus: 200,
			},
		},
	}
	err := buildHTTPSpec(&ContractGenSpec{}, t.TempDir(), contract, ".")
	if err == nil {
		t.Fatal("expected buildHTTPSpec to fail closed on a non-string header type")
	}
	if !strings.Contains(err.Error(), "endpoints.http.headers") || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("fail-closed error must point at the invalid header, got: %v", err)
	}
}

// TestBuildHeaderParams verifies headers are extracted populate-only: GoName is
// PascalCase (dash-aware), GoType derives from schema.Type, the canonical header
// name is preserved as Name (the r.Header.Get literal), and no length/numeric
// constraint is carried (headers emit no gate; FMT-40 rejects such declarations).
func TestBuildHeaderParams(t *testing.T) {
	truthy := true
	http := &metadata.HTTPTransportMeta{
		Method: "POST",
		Path:   "/api/v1/access/sessions/login",
		Headers: map[string]metadata.ParamSchema{
			"X-Tenant-ID": {Type: "string", Format: "uuid", Required: &truthy},
		},
	}
	got := buildHeaderParams(http)
	if len(got) != 1 {
		t.Fatalf("expected 1 header param, got %d", len(got))
	}
	p := got[0]
	if p.Name != "X-Tenant-ID" {
		t.Errorf("Name = %q, want %q (canonical r.Header.Get literal)", p.Name, "X-Tenant-ID")
	}
	if p.GoName != "XTenantID" {
		t.Errorf("GoName = %q, want %q", p.GoName, "XTenantID")
	}
	if p.GoType != "string" {
		t.Errorf("GoType = %q, want %q", p.GoType, "string")
	}
	if !p.Required {
		t.Errorf("Required = false, want true (documentation/client-gen metadata)")
	}
	if p.MinLength != nil || p.MaxLength != nil || p.Minimum != nil || p.Maximum != nil {
		t.Errorf("header param must carry no length/numeric constraints (populate-only): %+v", p)
	}
}

// TestParamToField_HeaderForcesJSONDash verifies a header field is never
// body-decodable: paramToField with source="header" forces JSONTag "-" so a
// client cannot spoof the header value (e.g. X-Tenant-ID) via the JSON body.
func TestParamToField_HeaderForcesJSONDash(t *testing.T) {
	f := paramToField(ParamSpec{Name: "X-Tenant-ID", GoName: "XTenantID", GoType: "string", Required: true}, "header")
	if f.JSONTag != "-" {
		t.Errorf("header field JSONTag = %q, want %q (body must not spoof headers)", f.JSONTag, "-")
	}
	if f.Source != "header" {
		t.Errorf("header field Source = %q, want %q", f.Source, "header")
	}
}

// --- ParamSpec boundary value tests (A.10) ---

func TestBuildQueryParams_BoundaryValues(t *testing.T) {
	minLenZero := 0
	maxLenZero := 0
	minLenFive := 5
	maxLenTen := 10
	minInt64Zero := 0
	minInt64Neg := -1
	maxInt64 := 500

	http := &metadata.HTTPTransportMeta{
		Method: "GET",
		Path:   "/api/v1/items",
		QueryParams: map[string]metadata.ParamSchema{
			"q1": {Type: "string", MinLength: &minLenZero, MaxLength: &maxLenZero},
			"q2": {Type: "string", MinLength: &minLenFive, MaxLength: &maxLenTen},
			"q3": {Type: "integer", Minimum: &minInt64Zero},
			"q4": {Type: "integer", Minimum: &minInt64Neg, Maximum: &maxInt64},
		},
	}
	params := buildQueryParams(http)
	if len(params) != 4 {
		t.Fatalf("expected 4 params, got %d", len(params))
	}
	// params are sorted alphabetically: q1, q2, q3, q4
	cases := map[string]ParamSpec{}
	for _, p := range params {
		cases[p.Name] = p
	}

	q1 := cases["q1"]
	if q1.MinLength == nil || *q1.MinLength != 0 {
		t.Errorf("q1.MinLength should be 0, got %v", q1.MinLength)
	}
	if q1.MaxLength == nil || *q1.MaxLength != 0 {
		t.Errorf("q1.MaxLength should be 0, got %v", q1.MaxLength)
	}

	q2 := cases["q2"]
	if q2.MinLength == nil || *q2.MinLength != 5 {
		t.Errorf("q2.MinLength should be 5, got %v", q2.MinLength)
	}
	if q2.MaxLength == nil || *q2.MaxLength != 10 {
		t.Errorf("q2.MaxLength should be 10, got %v", q2.MaxLength)
	}

	q3 := cases["q3"]
	if q3.Minimum == nil || *q3.Minimum != 0 {
		t.Errorf("q3.Minimum should be 0, got %v", q3.Minimum)
	}

	q4 := cases["q4"]
	if q4.Minimum == nil || *q4.Minimum != -1 {
		t.Errorf("q4.Minimum should be -1, got %v", q4.Minimum)
	}
	if q4.Maximum == nil || *q4.Maximum != 500 {
		t.Errorf("q4.Maximum should be 500, got %v", q4.Maximum)
	}
}

// --- buildContractSpec tests ---

// TestBuildContractSpec_ProjectionKind_Skips mirrors the command test for
// kind=projection.
func TestBuildContractSpec_ProjectionKind_Skips(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"projection.device.inventory.v1": {
				ID:   "projection.device.inventory.v1",
				Kind: "projection",
				// PROJECTION-CONSISTENCY-01 (gh #960): contractgen now requires a
				// parseable consistency level for projection contracts so the
				// types_gen.go compile-time guard renders a valid cellvocab ident.
				ConsistencyLevel: "L3",
				Codegen:          true,
				Transports:       []string{"internal"}, // mirrors parser defaultTransportsForKind("projection")
				File:             "contracts/projection/device/inventory/v1/contract.yaml",
			},
		},
	}
	spec, err := buildContractSpec("", p, "projection.device.inventory.v1")
	if err != nil {
		t.Fatalf("buildContractSpec should not error for kind=projection, got: %v", err)
	}
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.Kind != "projection" {
		t.Errorf("spec.Kind = %q, want %q", spec.Kind, "projection")
	}
	if spec.Endpoint != nil {
		t.Errorf("spec.Endpoint should be nil for kind=projection, got non-nil")
	}
	if spec.Event != nil {
		t.Errorf("spec.Event should be nil for kind=projection, got non-nil")
	}
}

// TestBuildContractSpec_GRPCKind_NoIR verifies that a kind=grpc contract builds a
// spec with NO grpc-specific IR (#1688): buildContractSpec succeeds, marks
// spec.Kind=="grpc", leaves every kind-specific IR field nil (Endpoint / Event /
// Command / Saga — and there is no longer a GRPC field), and resolves the
// generated package path kind-generically. contractgen emits nothing for grpc —
// buf's generated pb.<Svc>Server is the sole server contract — so there is no
// interface name / method set to project here. Uses the committed
// synth_grpc_minimal fixture as the root so parsing succeeds.
func TestBuildContractSpec_GRPCKind_NoIR(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_grpc_minimal"))
	if err != nil {
		t.Fatalf("abs fixture root: %v", err)
	}
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"grpc.device.command.v1": {
				ID:         "grpc.device.command.v1",
				Kind:       "grpc",
				Codegen:    true,
				Transports: []string{"grpc"}, // mirrors parser defaultTransportsForKind("grpc")
				File:       "contracts/grpc/device/command/v1/contract.yaml",
				Endpoints: metadata.EndpointsMeta{
					Server: "devicecell",
					GRPC: &metadata.GRPCTransportMeta{
						Service: "device.command.v1.DeviceCommandService",
						Proto:   "contracts/grpc/device/command/v1/device_command.proto",
					},
				},
			},
		},
	}
	spec, err := buildContractSpec(root, p, "grpc.device.command.v1")
	if err != nil {
		t.Fatalf("buildContractSpec should not error for kind=grpc, got: %v", err)
	}
	if spec.Kind != "grpc" {
		t.Errorf("spec.Kind = %q, want %q", spec.Kind, "grpc")
	}
	if spec.Endpoint != nil {
		t.Errorf("spec.Endpoint should be nil for kind=grpc, got non-nil")
	}
	if spec.Event != nil {
		t.Errorf("spec.Event should be nil for kind=grpc, got non-nil")
	}
	if spec.Command != nil {
		t.Errorf("spec.Command should be nil for kind=grpc, got non-nil")
	}
	if spec.Saga != nil {
		t.Errorf("spec.Saga should be nil for kind=grpc, got non-nil")
	}
	// Package path is still computed kind-generically (used by the
	// generated/contracts/ prefix guard) even though grpc writes no file there.
	if spec.PackagePath != "generated/contracts/grpc/device/command/v1" {
		t.Errorf("spec.PackagePath = %q, want %q", spec.PackagePath, "generated/contracts/grpc/device/command/v1")
	}
}

// TestValidateGRPCProtoPath covers the shared proto-path guard, including the
// filepath.IsLocal traversal check that HasPrefix alone does not catch.
func TestValidateGRPCProtoPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		proto   string
		wantErr string // substring; "" means expect success
	}{
		{"ok", "contracts/grpc/device/command/v1/device_command.proto", ""},
		{"empty", "", "requires proto"},
		{"wrong prefix", "contracts/http/x.proto", "must be rooted under"},
		{"control char", "contracts/grpc/x\n.proto", "control character"},
		{"traversal escape above root", "contracts/grpc/../../../etc/passwd", "local path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateGRPCProtoPath("grpc.test.v1", tc.proto)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// grpc structural validation (kind=grpc requires endpoints.grpc with a valid
// service + proto, no control runes, proto rooted under contracts/grpc/) is NOT
// a buildContractSpec concern since #1688 — contractgen builds no grpc IR. It is
// owned by governance FMT-37 (validateFMT37, the gocell validate gate) and, at
// the codegen layer, by the checkGRPCProtoCollisions pre-pass: proto-path guards
// via TestValidateGRPCProtoPath (above) + production rejection via
// TestCheckGRPCProtoCollisions_RejectsBadProtoPath (generator_test.go); service
// validity via the protoreader_test.go ReadProtoServiceInfo suite.

// --- BuildHTTPEndpointSpec HasBody tests ---

// TestBuildHTTPSpec_ModelessRejected is the RED test for the #2020 mandatory-AuthZ-mode
// Hard gate inside buildHTTPSpec. An active+codegen HTTP contract that declares no
// endpoints.http.permission and no explicit opt-out flag is "modeless" — the gate
// must reject it so no artifact is ever emitted regardless of entry point.
// This proves ClassifyHTTPAuthMode→HTTPAuthModeModeless → error containing
// "declares no AuthZ mode" before any template is rendered.
func TestBuildHTTPSpec_ModelessRejected(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.synth.modeless.v1": {
				ID:         "http.synth.modeless.v1",
				Kind:       "http",
				Lifecycle:  "active",
				Codegen:    true,
				Transports: []string{"http"},
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          "/api/v1/synth/modeless",
						SuccessStatus: 200,
						// Permission is intentionally empty; Auth has no opt-out flag.
						// This is the modeless state the gate must reject.
					},
				},
			},
		},
	}
	root := findRepoRoot()
	_, err := buildContractSpec(root, p, "http.synth.modeless.v1")
	if err == nil {
		t.Fatal("expected buildContractSpec to reject a modeless HTTP contract, got nil error")
	}
	if !strings.Contains(err.Error(), "declares no AuthZ mode") {
		t.Errorf("error should contain %q, got: %v", "declares no AuthZ mode", err)
	}
}

// TestBuildHTTPEndpointSpec_HasBody_PostWithoutRequestSchema verifies that
// HasBody=false when the contract is POST but declares no schemaRefs.request.
// This is the "body-less POST" case (path-param-only endpoints).
func TestBuildHTTPEndpointSpec_HasBody_PostWithoutRequestSchema(t *testing.T) {
	t.Parallel()
	contract := &metadata.ContractMeta{
		ID:         "http.order.activate.v1",
		Kind:       "http",
		SchemaRefs: metadata.SchemaRefsMeta{Request: ""}, // no request body schema
	}
	http := &metadata.HTTPTransportMeta{
		Method:        "POST",
		Path:          "/api/v1/orders/{id}/activate",
		SuccessStatus: 204,
		NoContent:     true,
		Responses: map[int]metadata.HTTPResponseMeta{
			400: {Description: "Bad Request"},
		},
	}
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)
	spec, err := buildHTTPEndpointSpec(contract, http, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.HasBody {
		t.Errorf("HasBody should be false for POST without request schema, got true")
	}
}

// TestBuildHTTPEndpointSpec_HasBody_PostWithRequestSchema verifies that
// HasBody=true when the contract is POST and declares a schemaRefs.request.
func TestBuildHTTPEndpointSpec_HasBody_PostWithRequestSchema(t *testing.T) {
	t.Parallel()
	contract := &metadata.ContractMeta{
		ID:         "http.order.create.v1",
		Kind:       "http",
		SchemaRefs: metadata.SchemaRefsMeta{Request: "request-schema.json"},
	}
	http := &metadata.HTTPTransportMeta{
		Method:        "POST",
		Path:          "/api/v1/orders",
		SuccessStatus: 201,
		Responses: map[int]metadata.HTTPResponseMeta{
			400: {Description: "Bad Request"},
		},
	}
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)
	spec, err := buildHTTPEndpointSpec(contract, http, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spec.HasBody {
		t.Errorf("HasBody should be true for POST with request schema, got false")
	}
}

// TestBuildContractSpec_Event_TopicAndHandlerMethod verifies that buildContractSpec
// correctly populates the EventEndpointSpec.Topic and HandlerMethod fields for an
// event contract. Topic == ContractID after PR-CODEGEN-FULL-MIGRATION-FU,
// HandlerMethod is "Handle" + PascalCase(domainLastSegment).
func TestBuildContractSpec_Event_TopicAndHandlerMethod(t *testing.T) {
	t.Parallel()
	root, p := setupEventRoot(t)

	spec, err := buildContractSpec(root, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}
	if spec.Event == nil {
		t.Fatal("expected Event spec to be populated for kind=event")
	}
	// Topic == ContractID after PR-CODEGEN-FULL-MIGRATION-FU
	wantTopic := "event.item-created.v1"
	if spec.Event.Topic != wantTopic {
		t.Errorf("Event.Topic = %q, want %q", spec.Event.Topic, wantTopic)
	}
	// domainLastSegment("event.item-created.v1") = "item-created"
	// goPascalCase("item-created") = "ItemCreated" → "HandleItemCreated"
	wantMethod := "HandleItemCreated"
	if spec.Event.HandlerMethod != wantMethod {
		t.Errorf("Event.HandlerMethod = %q, want %q", spec.Event.HandlerMethod, wantMethod)
	}
}

// --- Auth.Bootstrap closed-set extension tests (Batch 0 RED, SEC-SETUP-CLOSURE) ---

// TestBuildHTTPEndpointSpec_AuthBootstrap_FieldPropagated verifies that
// contract.yaml auth.bootstrap:true is correctly propagated to httpEndpointSpec.AuthBootstrap.
// This test is RED until Batch 1 / Agent-A integrates auth.bootstrap into the real setup
// contract and the template renders it. The field propagation through buildHTTPEndpointSpec
// is already GREEN from the schema extension in Batch 0.
func TestBuildHTTPEndpointSpec_AuthBootstrap_FieldPropagated(t *testing.T) {
	t.Parallel()

	root, p := setupHTTPMinimalRoot(t)
	// Inject a synthetic bootstrap-auth contract into the parsed project.
	// We patch it directly on the ProjectMeta to avoid needing a real testdata file.
	bootstrapContractID := "http.auth.setup.admin.v1"
	trueCodegen := true
	p.Contracts[bootstrapContractID] = &metadata.ContractMeta{
		ID:               bootstrapContractID,
		Kind:             "http",
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Codegen:          trueCodegen,
		Transports:       []string{"http"}, // mirrors parser defaultTransportsForKind("http")
		Endpoints: metadata.EndpointsMeta{
			Server:  metadatatest.CellIDAccessCore,
			Clients: []string{metadatatest.CellIDEdgeBFF},
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "POST",
				Path:          "/api/v1/access/setup/admin",
				SuccessStatus: 201,
				NoContent:     false,
				Auth: metadata.HTTPAuthMeta{
					Bootstrap: true,
					Reason:    "bootstrap admin endpoint uses HTTP Basic credentials, not JWT",
				},
				Responses: map[int]metadata.HTTPResponseMeta{
					400: {Description: "Bad Request"},
					401: {Description: "Unauthorized"},
				},
			},
		},
		File: "contracts/http/auth/setup/admin/v1/contract.yaml",
	}

	spec, err := buildContractSpec(root, p, bootstrapContractID)
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}
	if spec.Endpoint == nil {
		t.Fatal("expected Endpoint spec to be populated for kind=http")
	}
	if !spec.Endpoint.AuthBootstrap {
		t.Errorf("AuthBootstrap must be true when contract declares auth.bootstrap:true, got false")
	}
	if spec.Endpoint.AuthPublic {
		t.Errorf("AuthPublic must be false when contract declares auth.bootstrap:true (mutually exclusive)")
	}
	if spec.Endpoint.AuthPasswordResetExempt {
		t.Errorf("AuthPasswordResetExempt must be false when contract declares auth.bootstrap:true (mutually exclusive)")
	}
}

func TestBuildHTTPEndpointSpec_ServiceOwnedAllowsPasswordResetExempt(t *testing.T) {
	t.Parallel()

	contract := &metadata.ContractMeta{
		ID:   "http.auth.session.delete.v1",
		Kind: "http",
		Endpoints: metadata.EndpointsMeta{
			Server: metadatatest.CellIDAccessCore,
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "DELETE",
				Path:          "/api/v1/access/sessions/{id}",
				SuccessStatus: 204,
				NoContent:     true,
				Auth: metadata.HTTPAuthMeta{
					ServiceOwned:        true,
					PasswordResetExempt: true,
				},
				Responses: map[int]metadata.HTTPResponseMeta{
					400: {Description: "Bad Request", SchemaRef: "error.schema.json"},
				},
			},
		},
	}
	http := contract.Endpoints.HTTP
	spec, err := buildHTTPEndpointSpec(contract, http, buildPathParams(http), buildQueryParams(http), buildHeaderParams(http))
	if err != nil {
		t.Fatalf("expected serviceOwned + passwordResetExempt to build, got: %v", err)
	}
	if !spec.AuthServiceOwned {
		t.Error("AuthServiceOwned should be true")
	}
	if !spec.AuthPasswordResetExempt {
		t.Error("AuthPasswordResetExempt should remain true")
	}
	if spec.AuthPublic || spec.AuthBootstrap || spec.AuthClientsOnly {
		t.Errorf("serviceOwned endpoint must not imply public/bootstrap/clientsOnly: public=%v bootstrap=%v clientsOnly=%v",
			spec.AuthPublic, spec.AuthBootstrap, spec.AuthClientsOnly)
	}
}

func TestBuildHTTPEndpointSpec_ServiceOwnedRejectsExclusiveModes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		auth    metadata.HTTPAuthMeta
		path    string
		clients []string
	}{
		{
			name: "public",
			auth: metadata.HTTPAuthMeta{ServiceOwned: true, Public: true},
			path: "/api/v1/service-owned/public",
		},
		{
			name: "bootstrap",
			auth: metadata.HTTPAuthMeta{ServiceOwned: true, Bootstrap: true},
			path: "/api/v1/access/setup/admin",
		},
		{
			name:    "clientsOnly",
			auth:    metadata.HTTPAuthMeta{ServiceOwned: true, ClientsOnly: true},
			path:    "/internal/v1/service-owned/clients-only",
			clients: []string{metadatatest.CellIDEdgeBFF},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			contract := &metadata.ContractMeta{
				ID:   "http.auth.bad.serviceowned." + tc.name + ".v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: tc.clients,
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          tc.path,
						SuccessStatus: 200,
						Auth:          tc.auth,
					},
				},
			}
			http := contract.Endpoints.HTTP
			_, err := buildHTTPEndpointSpec(contract, http, buildPathParams(http), buildQueryParams(http), buildHeaderParams(http))
			if err == nil {
				t.Fatal("expected serviceOwned mutual-exclusion error, got nil")
			}
			if !strings.Contains(err.Error(), "auth.serviceOwned:true") {
				t.Fatalf("error should mention auth.serviceOwned:true, got: %v", err)
			}
		})
	}
}

// TestBuildHTTPEndpointSpec_AuthBootstrap_MutuallyExclusive_WithPublic verifies that
// a contract declaring both auth.bootstrap:true and auth.public:true is a configuration
// error that produces distinguishable field values (both would be true, which FMT-27
// catches at governance level). This test documents expected field propagation behavior.
func TestBuildHTTPEndpointSpec_AuthBootstrap_MutuallyExclusive_WithPublic(t *testing.T) {
	t.Parallel()

	// auth.Bootstrap and auth.Public are both present in the metadata struct —
	// the builder propagates both, governance (FMT-27) catches the violation.
	// Verify that AuthBootstrap field is set when bootstrap:true regardless of Public.
	httpMeta := &metadata.HTTPTransportMeta{
		Method:        "POST",
		Path:          "/api/v1/access/setup/admin",
		SuccessStatus: 201,
		Auth: metadata.HTTPAuthMeta{
			Bootstrap: true,
		},
		Responses: map[int]metadata.HTTPResponseMeta{
			400: {Description: "Bad Request"},
			401: {Description: "Unauthorized"},
		},
	}
	contract := &metadata.ContractMeta{
		ID:      "http.auth.setup.admin.v1",
		Kind:    "http",
		Codegen: true,
		Endpoints: metadata.EndpointsMeta{
			Server: metadatatest.CellIDAccessCore,
			HTTP:   httpMeta,
		},
		File: "contracts/http/auth/setup/admin/v1/contract.yaml",
	}

	pathParams := buildPathParams(httpMeta)
	queryParams := buildQueryParams(httpMeta)
	spec, err := buildHTTPEndpointSpec(contract, httpMeta, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("buildHTTPEndpointSpec: %v", err)
	}
	if !spec.AuthBootstrap {
		t.Errorf("AuthBootstrap must propagate from auth.bootstrap:true, got false")
	}
}

// --- Batch 1 RED (PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE) ---
//
// These tests assert the new IR shapes introduced for typed response envelope
// generation: PaginationShape (replaces IsPagination bool with structured form
// that carries cursor/limit + non-pagination query params) and ResponseSpec
// (lifts contract.yaml http.responses[] into the IR so handler/types/governance
// share one declaration table).

// TestBuildHTTPEndpointSpec_PaginationShape_PureCursorLimit verifies that an
// endpoint with exactly cursor+limit query params produces a non-nil
// Pagination shape with HasCursor/HasLimit set and an empty ExtraQueryParams.
// IsPagination() method must mirror Pagination != nil for template back-compat.
func TestBuildHTTPEndpointSpec_PaginationShape_PureCursorLimit(t *testing.T) {
	t.Parallel()
	httpMeta := &metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/orders",
		SuccessStatus: 200,
		QueryParams: map[string]metadata.ParamSchema{
			"cursor": {Type: "string"},
			"limit":  {Type: "integer"},
		},
		Responses: map[int]metadata.HTTPResponseMeta{
			400: {Description: "Bad Request"},
		},
	}
	contract := &metadata.ContractMeta{ID: "http.order.list.v1", Kind: "http", Endpoints: metadata.EndpointsMeta{HTTP: httpMeta}}
	pathParams := buildPathParams(httpMeta)
	queryParams := buildQueryParams(httpMeta)
	spec, err := buildHTTPEndpointSpec(contract, httpMeta, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("buildHTTPEndpointSpec: %v", err)
	}
	if spec.Pagination == nil {
		t.Fatal("Pagination shape must be populated for cursor+limit endpoint, got nil")
	}
	if !spec.Pagination.HasCursor || !spec.Pagination.HasLimit {
		t.Errorf("Pagination must report HasCursor=true HasLimit=true, got HasCursor=%v HasLimit=%v",
			spec.Pagination.HasCursor, spec.Pagination.HasLimit)
	}
	if len(spec.Pagination.ExtraQueryParams) != 0 {
		t.Errorf("ExtraQueryParams must be empty for pure cursor+limit, got %d", len(spec.Pagination.ExtraQueryParams))
	}
	if !spec.IsPagination() {
		t.Errorf("IsPagination() must return true when Pagination != nil")
	}
}

// TestBuildHTTPEndpointSpec_PaginationShape_CursorLimitPlusFilter verifies
// that an endpoint declaring cursor+limit alongside additional filter query
// params (e.g. role, since) is recognized as paginated (Pagination != nil)
// and the extras are routed into ExtraQueryParams, so the handler can drive
// cursor+limit through pkg/httputil.ParsePageParams while the extras parse
// per-param. This is the F4 absorb path: a single limit error envelope across
// the entire HTTP surface.
func TestBuildHTTPEndpointSpec_PaginationShape_CursorLimitPlusFilter(t *testing.T) {
	t.Parallel()
	httpMeta := &metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/auth/roles",
		SuccessStatus: 200,
		QueryParams: map[string]metadata.ParamSchema{
			"cursor": {Type: "string"},
			"limit":  {Type: "integer"},
			"role":   {Type: "string"},
		},
		Responses: map[int]metadata.HTTPResponseMeta{
			400: {Description: "Bad Request"},
		},
	}
	contract := &metadata.ContractMeta{ID: "http.auth.role.list.v1", Kind: "http", Endpoints: metadata.EndpointsMeta{HTTP: httpMeta}}
	pathParams := buildPathParams(httpMeta)
	queryParams := buildQueryParams(httpMeta)
	spec, err := buildHTTPEndpointSpec(contract, httpMeta, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("buildHTTPEndpointSpec: %v", err)
	}
	if spec.Pagination == nil {
		t.Fatal("Pagination must be populated when cursor+limit present even with filters, got nil")
	}
	if !spec.Pagination.HasCursor || !spec.Pagination.HasLimit {
		t.Errorf("Pagination flags wrong: HasCursor=%v HasLimit=%v", spec.Pagination.HasCursor, spec.Pagination.HasLimit)
	}
	if got := len(spec.Pagination.ExtraQueryParams); got != 1 {
		t.Fatalf("ExtraQueryParams length = %d, want 1 (role)", got)
	}
	if name := spec.Pagination.ExtraQueryParams[0].Name; name != "role" {
		t.Errorf("ExtraQueryParams[0].Name = %q, want %q", name, "role")
	}
}

// TestBuildHTTPEndpointSpec_PaginationShape_NoPagination verifies that an
// endpoint with arbitrary query params (no cursor or no limit) produces a
// nil Pagination shape and IsPagination() returns false.
func TestBuildHTTPEndpointSpec_PaginationShape_NoPagination(t *testing.T) {
	t.Parallel()
	httpMeta := &metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/items",
		SuccessStatus: 200,
		QueryParams: map[string]metadata.ParamSchema{
			"name": {Type: "string"},
		},
		Responses: map[int]metadata.HTTPResponseMeta{
			400: {Description: "Bad Request"},
		},
	}
	contract := &metadata.ContractMeta{ID: "http.item.search.v1", Kind: "http", Endpoints: metadata.EndpointsMeta{HTTP: httpMeta}}
	pathParams := buildPathParams(httpMeta)
	queryParams := buildQueryParams(httpMeta)
	spec, err := buildHTTPEndpointSpec(contract, httpMeta, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("buildHTTPEndpointSpec: %v", err)
	}
	if spec.Pagination != nil {
		t.Errorf("Pagination must be nil for non-cursor+limit endpoint, got %+v", spec.Pagination)
	}
	if spec.IsPagination() {
		t.Errorf("IsPagination() must return false when Pagination == nil")
	}
}

// TestBuildHTTPEndpointSpec_Responses_LiftFromContract verifies that
// contract.yaml http.responses[] is lifted into the IR Responses slice.
// Each declared error status produces a ResponseSpec with IsError=true,
// the success status (from SuccessStatus) is also represented, and
// GoTypeName follows the {HandlerMethod}{Status}{Suffix} convention.
func TestBuildHTTPEndpointSpec_Responses_LiftFromContract(t *testing.T) {
	t.Parallel()
	httpMeta := &metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/sessions/{id}",
		SuccessStatus: 200,
		PathParams: map[string]metadata.ParamSchema{
			"id": {Type: "string", Format: "uuid"},
		},
		Responses: map[int]metadata.HTTPResponseMeta{
			401: {Description: "Unauthorized", SchemaRef: "../../../shared/errors/error-response-v1.schema.json"},
			404: {Description: "Not Found", SchemaRef: "../../../shared/errors/error-response-v1.schema.json"},
			503: {Description: "Service Unavailable", SchemaRef: "../../../shared/errors/error-response-v1.schema.json"},
		},
	}
	contract := &metadata.ContractMeta{ID: "http.access.session.get.v1", Kind: "http", Endpoints: metadata.EndpointsMeta{HTTP: httpMeta}}
	pathParams := buildPathParams(httpMeta)
	queryParams := buildQueryParams(httpMeta)
	spec, err := buildHTTPEndpointSpec(contract, httpMeta, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("buildHTTPEndpointSpec: %v", err)
	}

	// Expect 4 entries (success 200 + 401/404/503 errors), sorted by status ascending.
	if got, want := len(spec.Responses), 4; got != want {
		t.Fatalf("Responses length = %d, want %d (success + 3 errors)", got, want)
	}
	wantOrder := []int{200, 401, 404, 503}
	for i, status := range wantOrder {
		if spec.Responses[i].Status != status {
			t.Errorf("Responses[%d].Status = %d, want %d (must be sorted ascending)", i, spec.Responses[i].Status, status)
		}
	}

	// Success entry: IsError=false, SchemaRef empty (success body is the response schema, not declared in responses[]).
	if spec.Responses[0].IsError {
		t.Errorf("success entry IsError = true, want false")
	}
	// Error entries: IsError=true.
	for _, idx := range []int{1, 2, 3} {
		if !spec.Responses[idx].IsError {
			t.Errorf("Responses[%d] (status %d) IsError = false, want true", idx, spec.Responses[idx].Status)
		}
		if spec.Responses[idx].SchemaRef == "" {
			t.Errorf("Responses[%d] (status %d) SchemaRef empty, want lifted from contract", idx, spec.Responses[idx].Status)
		}
	}

	// GoTypeName convention: HandlerMethod="Get" → "Get200JSONResponse", "Get401ErrorResponse", etc.
	wantGoNames := map[int]string{
		200: "Get200JSONResponse",
		401: "Get401ErrorResponse",
		404: "Get404ErrorResponse",
		503: "Get503ErrorResponse",
	}
	for _, r := range spec.Responses {
		if got := r.GoTypeName; got != wantGoNames[r.Status] {
			t.Errorf("Responses[status=%d].GoTypeName = %q, want %q", r.Status, got, wantGoNames[r.Status])
		}
	}
}

// TestDetectPagination_TypeMismatch verifies that detectPagination fails
// fast when cursor is not declared as a string or limit is not declared as
// an integer. The two error branches in builder.detectPagination are the
// only place where the IR rejects an otherwise-syntactically-valid pagination
// declaration; without these tests a future relaxation of the type check
// could silently slip through.
func TestDetectPagination_TypeMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		queryParams map[string]metadata.ParamSchema
		wantErrMsg  string
	}{
		{
			name: "cursor as integer rejected",
			queryParams: map[string]metadata.ParamSchema{
				"cursor": {Type: "integer"},
				"limit":  {Type: "integer"},
			},
			wantErrMsg: "cursor param must be string type",
		},
		{
			name: "limit as string rejected",
			queryParams: map[string]metadata.ParamSchema{
				"cursor": {Type: "string"},
				"limit":  {Type: "string"},
			},
			wantErrMsg: "limit param must be integer type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			httpMeta := &metadata.HTTPTransportMeta{
				Method:      "GET",
				Path:        "/api/v1/items",
				QueryParams: tc.queryParams,
			}
			contract := &metadata.ContractMeta{
				ID:        "http.x.list.v1",
				Kind:      "http",
				Endpoints: metadata.EndpointsMeta{HTTP: httpMeta},
			}
			pathParams := buildPathParams(httpMeta)
			queryParams := buildQueryParams(httpMeta)
			_, err := buildHTTPEndpointSpec(contract, httpMeta, pathParams, queryParams, nil)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrMsg)
			}
		})
	}
}

// TestBuildHTTPEndpointSpec_Responses_NoContent verifies that a 204 NoContent
// success endpoint generates a *NoContentResponse Go type rather than the
// JSONResponse suffix used for body-bearing success responses.
func TestBuildHTTPEndpointSpec_Responses_NoContent(t *testing.T) {
	t.Parallel()
	httpMeta := &metadata.HTTPTransportMeta{
		Method:        "DELETE",
		Path:          "/api/v1/sessions/{id}",
		SuccessStatus: 204,
		NoContent:     true,
		PathParams: map[string]metadata.ParamSchema{
			"id": {Type: "string", Format: "uuid"},
		},
		Responses: map[int]metadata.HTTPResponseMeta{
			401: {Description: "Unauthorized", SchemaRef: "../../../shared/errors/error-response-v1.schema.json"},
		},
	}
	contract := &metadata.ContractMeta{ID: "http.access.session.delete.v1", Kind: "http", Endpoints: metadata.EndpointsMeta{HTTP: httpMeta}}
	pathParams := buildPathParams(httpMeta)
	queryParams := buildQueryParams(httpMeta)
	spec, err := buildHTTPEndpointSpec(contract, httpMeta, pathParams, queryParams, nil)
	if err != nil {
		t.Fatalf("buildHTTPEndpointSpec: %v", err)
	}

	// First entry is success (204), should use NoContentResponse suffix.
	if spec.Responses[0].Status != 204 {
		t.Fatalf("Responses[0].Status = %d, want 204", spec.Responses[0].Status)
	}
	if got, want := spec.Responses[0].GoTypeName, "Delete204NoContentResponse"; got != want {
		t.Errorf("204 GoTypeName = %q, want %q (no-content success uses NoContentResponse suffix)", got, want)
	}
	if spec.Responses[0].IsError {
		t.Errorf("204 success IsError = true, want false")
	}
}

// --- collectAndValidateStatuses tests (C18 tighten: require ≥1 4xx/5xx) ---

// TestCollectAndValidateStatuses exercises the C18-tightened rule that every
// HTTP endpoint must declare at least one 4xx/5xx response code.
func TestCollectAndValidateStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		http        *metadata.HTTPTransportMeta
		wantErr     bool
		wantErrFrag string
		wantLen     int // expected length of returned statuses when wantErr==false
	}{
		{
			name: "success-only no responses",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 200,
				Responses:     nil,
			},
			wantErr:     true,
			wantErrFrag: "must declare at least one 4xx/5xx",
		},
		{
			name: "success+400",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					400: {Description: "Bad Request"},
				},
			},
			wantErr: false,
			wantLen: 2, // 200 + 400
		},
		{
			name: "only-401 no successStatus",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 0,
				Responses: map[int]metadata.HTTPResponseMeta{
					401: {Description: "Unauthorized"},
				},
			},
			wantErr: false,
			wantLen: 1, // 401 only
		},
		{
			name: "successStatus+only-3xx",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					304: {Description: "Not Modified"},
				},
			},
			wantErr:     true,
			wantErrFrag: "must be 4xx/5xx",
		},
		{
			name: "empty everything",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 0,
				Responses:     map[int]metadata.HTTPResponseMeta{},
			},
			wantErr:     true,
			wantErrFrag: "must declare at least one response",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := collectAndValidateStatuses(tc.http, "http.test.contract.v1")
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("expected error containing %q, got nil", tc.wantErrFrag)
			case tc.wantErr && tc.wantErrFrag != "" && !strings.Contains(err.Error(), tc.wantErrFrag):
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrFrag)
			case !tc.wantErr && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case !tc.wantErr && len(got) != tc.wantLen:
				t.Errorf("statuses length = %d, want %d; got %v", len(got), tc.wantLen, got)
			}
		})
	}
}

// --- contractIDToKebab unit tests (P2-07) ---

// TestContractIDToKebab verifies that contractIDToKebab converts contract IDs to
// kebab-case by replacing all dots with dashes. The function is the single source
// used by buildContractSpec to pre-compute all panicregister.Approved reason literals.
func TestContractIDToKebab(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple_dotted_id", "http.order.create.v1", "http-order-create-v1"},
		{"v_prefixed_version", "http.config.flags.evaluate.v1", "http-config-flags-evaluate-v1"},
		{"single_segment", "foo", "foo"},
		{"empty", "", ""},
		{"underscore_preserved", "http.user_profile.v1", "http-user_profile-v1"},
		{"hyphen_preserved", "http.foo-bar.v1", "http-foo-bar-v1"},
		{"consecutive_dots", "http..baz", "http--baz"},
		{"trailing_dot", "http.bar.", "http-bar-"},
		{"leading_dot", ".http.bar.v1", "-http-bar-v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := contractIDToKebab(tc.in)
			if got != tc.want {
				t.Errorf("contractIDToKebab(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- liftHTTPResponses status validation tests (C5 / C18) ---

// TestLiftHTTPResponses_Validation covers the C5 (status range) and C18
// (at-least-one-response) validation rules added to liftHTTPResponses.
func TestLiftHTTPResponses_Validation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		http        *metadata.HTTPTransportMeta
		wantErr     bool
		wantErrFrag string // substring that must appear in the error message
	}{
		{
			name: "valid success=200 plus 4xx/5xx errors",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					400: {Description: "Bad Request"},
					500: {Description: "Internal Server Error"},
				},
			},
			wantErr: false,
		},
		{
			name: "responses[] contains 3xx — rejected (C5)",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					301: {Description: "Moved Permanently"},
				},
			},
			wantErr:     true,
			wantErrFrag: "must be 4xx/5xx",
		},
		{
			name: "responses[] contains 600 — rejected (C5)",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					600: {Description: "Out of range"},
				},
			},
			wantErr:     true,
			wantErrFrag: "must be 4xx/5xx",
		},
		{
			name: "no SuccessStatus and no responses[] — rejected (C18)",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 0,
				Responses:     map[int]metadata.HTTPResponseMeta{},
			},
			wantErr:     true,
			wantErrFrag: "must declare at least one response",
		},
		{
			name: "SuccessStatus=99 (too low) — rejected (C5)",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 99,
				Responses: map[int]metadata.HTTPResponseMeta{
					400: {Description: "Bad Request"},
				},
			},
			wantErr:     true,
			wantErrFrag: "must be 1xx/2xx/3xx",
		},
		{
			name: "SuccessStatus=400 (not 1xx-3xx) — rejected (C5)",
			http: &metadata.HTTPTransportMeta{
				SuccessStatus: 400,
				Responses:     map[int]metadata.HTTPResponseMeta{},
			},
			wantErr:     true,
			wantErrFrag: "must be 1xx/2xx/3xx",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := liftHTTPResponses(tc.http, "Get", "http.test.contract.v1")
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("expected error containing %q, got nil", tc.wantErrFrag)
			case tc.wantErr && tc.wantErrFrag != "" && !strings.Contains(err.Error(), tc.wantErrFrag):
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrFrag)
			case !tc.wantErr && err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestBuildHTTPEndpointSpec_RejectsPublicBypassOnInternalPath is the codegen-side
// upstream Hard funnel for FMT-34 (kernel/governance/rules_fmt.go::validateFMT34
// is the downstream Medium half). buildHTTPEndpointSpec is the sole production
// HTTP codegen entry — so rejecting auth.public / auth.passwordResetExempt on
// /internal/v1/* here makes the violation unrepresentable at build pipeline
// level, complementing governance's static metadata enforcement.
func TestBuildHTTPEndpointSpec_RejectsPublicBypassOnInternalPath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		auth         metadata.HTTPAuthMeta
		path         string
		clients      []string
		wantErr      bool
		wantErrFrags []string
	}{
		// fail cases — internal path + bypass flag must be rejected.
		{
			name:         "public_on_internal_path",
			auth:         metadata.HTTPAuthMeta{Public: true},
			path:         "/internal/v1/foo",
			wantErr:      true,
			wantErrFrags: []string{"FMT-34", "auth.public", "/internal/v1/foo"},
		},
		{
			name:         "password_reset_exempt_on_internal_path",
			auth:         metadata.HTTPAuthMeta{PasswordResetExempt: true},
			path:         "/internal/v1/foo",
			wantErr:      true,
			wantErrFrags: []string{"FMT-34", "auth.passwordResetExempt", "/internal/v1/foo"},
		},
		{
			// both bypass flags — errors.Join collects both violations.
			name:         "both_flags_on_internal_path",
			auth:         metadata.HTTPAuthMeta{Public: true, PasswordResetExempt: true},
			path:         "/internal/v1/foo",
			wantErr:      true,
			wantErrFrags: []string{"FMT-34", "auth.public", "auth.passwordResetExempt", "/internal/v1/foo"},
		},
		{
			// exact prefix /internal/v1 boundary verification.
			name:         "exact_prefix_internal_v1",
			auth:         metadata.HTTPAuthMeta{Public: true},
			path:         "/internal/v1",
			wantErr:      true,
			wantErrFrags: []string{"FMT-34", "auth.public", "/internal/v1"},
		},

		// pass cases — FMT-34 must not over-fire on shapes outside its scope.
		// FMT-34 only inspects auth.Public + auth.PasswordResetExempt on
		// /internal/v1/* paths; bootstrap / serviceOwned / clientsOnly are
		// not its concern (separate rules — FMT-28 narrows bootstrap to
		// /api/v{N}/{cell}/setup/admin, etc.). These cases lock that scope.
		{
			// bootstrap on /internal/v1/* is NOT a legitimate shape (FMT-28
			// rejects bootstrap on non-setup-admin paths), but FMT-34 itself
			// does not inspect the Bootstrap flag → no FMT-34 finding here.
			// Separation-of-concerns: codegen-side FMT-28 enforcement runs in
			// validateAuthServiceOwned / governance FMT-28 rule.
			name:    "bootstrap_flag_not_fmt34_concern",
			auth:    metadata.HTTPAuthMeta{Bootstrap: true},
			path:    "/internal/v1/foo",
			wantErr: false,
		},
		{
			name:    "service_owned_on_internal_path_ok",
			auth:    metadata.HTTPAuthMeta{ServiceOwned: true},
			path:    "/internal/v1/foo",
			wantErr: false,
		},
		{
			name:    "clients_only_on_internal_path_ok",
			auth:    metadata.HTTPAuthMeta{ClientsOnly: true},
			path:    "/internal/v1/foo",
			clients: []string{metadatatest.CellIDEdgeBFF},
			wantErr: false,
		},
		{
			name:    "public_on_public_path_ok",
			auth:    metadata.HTTPAuthMeta{Public: true},
			path:    "/api/v1/auth/login",
			wantErr: false,
		},
		{
			// IsInternalHTTPPath is strictly /internal/v1 — future version paths
			// must not be rejected by FMT-34 (locks oracle against version drift).
			name:    "internal_v2_not_internal_path_ok",
			auth:    metadata.HTTPAuthMeta{Public: true},
			path:    "/internal/v2/foo",
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			contract := &metadata.ContractMeta{
				ID:   "http.fmt34." + tc.name + ".v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: tc.clients,
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "GET",
						Path:          tc.path,
						SuccessStatus: 200,
						Auth:          tc.auth,
						Responses: map[int]metadata.HTTPResponseMeta{
							400: {Description: "Bad Request"},
						},
					},
				},
			}
			http := contract.Endpoints.HTTP
			_, err := buildHTTPEndpointSpec(contract, http, buildPathParams(http), buildQueryParams(http), buildHeaderParams(http))
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("expected FMT-34 codegen rejection, got nil")
			case tc.wantErr && err != nil:
				for _, frag := range tc.wantErrFrags {
					if !strings.Contains(err.Error(), frag) {
						t.Errorf("error %q missing expected fragment %q", err.Error(), frag)
					}
				}
			case !tc.wantErr && err != nil:
				t.Fatalf("unexpected error on legitimate internal-path auth shape: %v", err)
			}
		})
	}
}

// TestBuildHTTPEndpointSpec_IdempotencyExempt_FieldPropagated verifies that
// contract.yaml endpoints.http.idempotency.exempt:true is correctly propagated
// to httpEndpointSpec.IdempotencyExempt, and that it is orthogonal to the FMT-27
// auth mutex (a sibling of auth, not an auth flag — #1469 review F7).
func TestBuildHTTPEndpointSpec_IdempotencyExempt_FieldPropagated(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		auth         metadata.HTTPAuthMeta
		wantPublic   bool
		wantPRExempt bool
	}{
		{
			name:       "idempotency.exempt alone",
			auth:       metadata.HTTPAuthMeta{},
			wantPublic: false, wantPRExempt: false,
		},
		{
			name:       "idempotency.exempt with public",
			auth:       metadata.HTTPAuthMeta{Public: true},
			wantPublic: true, wantPRExempt: false,
		},
		{
			name:         "idempotency.exempt with passwordResetExempt",
			auth:         metadata.HTTPAuthMeta{PasswordResetExempt: true},
			wantPublic:   false,
			wantPRExempt: true,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			contract := &metadata.ContractMeta{
				ID:   "http.test.idempotency.v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					Server: metadatatest.CellIDAccessCore,
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/sample/test",
						SuccessStatus: 200,
						Auth:          tc.auth,
						Idempotency:   metadata.HTTPIdempotencyMeta{Exempt: true},
						Responses: map[int]metadata.HTTPResponseMeta{
							400: {Description: "Bad Request"},
						},
					},
				},
			}
			http := contract.Endpoints.HTTP
			spec, err := buildHTTPEndpointSpec(contract, http, buildPathParams(http), buildQueryParams(http), buildHeaderParams(http))
			if err != nil {
				t.Fatalf("expected idempotency.exempt combo to build without error, got: %v", err)
			}
			if !spec.IdempotencyExempt {
				t.Errorf("IdempotencyExempt must be true when contract declares idempotency.exempt:true")
			}
			if spec.AuthPublic != tc.wantPublic {
				t.Errorf("AuthPublic: got %v, want %v", spec.AuthPublic, tc.wantPublic)
			}
			if spec.AuthPasswordResetExempt != tc.wantPRExempt {
				t.Errorf("AuthPasswordResetExempt: got %v, want %v", spec.AuthPasswordResetExempt, tc.wantPRExempt)
			}
		})
	}
}
