package contractgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findModuleRoot walks up from the directory of this test file until it finds
// a go.mod, returning the directory that contains it. This lets tests locate
// the repo root without hard-coding an absolute path.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("findModuleRoot: filepath.Abs: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("findModuleRoot: go.mod not found")
		}
		dir = parent
	}
}

// writeSchema writes content to dir/name.
func writeSchema(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// parseFromDir calls Parse with rootDir=dir and refPath=name.
func parseFromDir(t *testing.T, dir, name string) (*Schema, error) {
	t.Helper()
	return Parse(dir, name)
}

// ---- happy path tests -------------------------------------------------------

func TestParse_SimpleStringField(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("type: got %q, want %q", s.Type, "object")
	}
	if _, ok := s.Properties["name"]; !ok {
		t.Error("property 'name' missing")
	}
	if s.Properties["name"].Type != "string" {
		t.Errorf("name.type: got %q, want string", s.Properties["name"].Type)
	}
}

func TestParse_MultipleTypes(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"count":   {"type": "integer"},
			"ratio":   {"type": "number"},
			"active":  {"type": "boolean"},
			"label":   {"type": "string"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"count":  "integer",
		"ratio":  "number",
		"active": "boolean",
		"label":  "string",
	}
	for k, wantType := range want {
		if s.Properties[k].Type != wantType {
			t.Errorf("property %q type: got %q, want %q", k, s.Properties[k].Type, wantType)
		}
	}
}

func TestParse_NestedObject(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"address": {
				"type": "object",
				"properties": {
					"street": {"type": "string"},
					"city":   {"type": "string"}
				}
			}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	addr := s.Properties["address"]
	if addr == nil || addr.Type != "object" {
		t.Fatal("address property missing or wrong type")
	}
	if addr.Properties["street"] == nil {
		t.Error("street missing in nested object")
	}
	if addr.Properties["city"] == nil {
		t.Error("city missing in nested object")
	}
}

func TestParse_ArrayOfStrings(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "array",
		"items": {"type": "string"}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Type != "array" {
		t.Errorf("type: got %q, want array", s.Type)
	}
	if s.Items == nil || s.Items.Type != "string" {
		t.Error("items.type should be string")
	}
}

func TestParse_ArrayOfObjects(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "array",
		"items": {
			"type": "object",
			"properties": {
				"id": {"type": "string"}
			}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Items == nil || s.Items.Type != "object" {
		t.Fatal("items should be object")
	}
	if s.Items.Properties["id"] == nil {
		t.Error("items.properties.id missing")
	}
}

func TestParse_Required(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "string"}
		},
		"required": ["a", "b"]
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Required) != 2 {
		t.Errorf("required len: got %d, want 2", len(s.Required))
	}
}

func TestParse_FormatUUID(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "string",
		"format": "uuid"
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Format != "uuid" {
		t.Errorf("format: got %q, want uuid", s.Format)
	}
}

func TestParse_FormatDateTime(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "string",
		"format": "date-time"
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Format != "date-time" {
		t.Errorf("format: got %q, want date-time", s.Format)
	}
}

func TestParse_FormatInt64(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "integer",
		"format": "int64"
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Format != "int64" {
		t.Errorf("format: got %q, want int64", s.Format)
	}
}

func TestParse_AdditionalPropertiesFalse(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {"x": {"type": "string"}},
		"additionalProperties": false
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.AdditionalProperties != false {
		t.Error("additionalProperties should be false")
	}
}

func TestParse_AdditionalPropertiesDefaultTrue(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {"x": {"type": "string"}}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.AdditionalProperties != true {
		t.Error("additionalProperties should default to true when absent")
	}
}

func TestParse_MinLengthMaxLength(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "string",
		"minLength": 1,
		"maxLength": 256
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.MinLength == nil || *s.MinLength != 1 {
		t.Errorf("minLength: got %v, want 1", s.MinLength)
	}
	if s.MaxLength == nil || *s.MaxLength != 256 {
		t.Errorf("maxLength: got %v, want 256", s.MaxLength)
	}
}

func TestParse_MinLengthZeroDistinctFromUnset(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"type": "string", "minLength": 0}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.MinLength == nil {
		t.Fatal("minLength pointer should not be nil for value 0")
	}
	if *s.MinLength != 0 {
		t.Errorf("minLength: got %d, want 0", *s.MinLength)
	}
}

func TestParse_MinimumMaximum(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "integer",
		"minimum": 0,
		"maximum": 100
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Minimum == nil || *s.Minimum != 0 {
		t.Errorf("minimum: got %v, want 0", s.Minimum)
	}
	if s.Maximum == nil || *s.Maximum != 100 {
		t.Errorf("maximum: got %v, want 100", s.Maximum)
	}
}

func TestParse_Title(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"title": "My Schema",
		"type": "object"
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Title != "My Schema" {
		t.Errorf("title: got %q, want %q", s.Title, "My Schema")
	}
}

func TestParse_RefSameFileDefs(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"$defs": {
			"Address": {
				"type": "object",
				"properties": {
					"street": {"type": "string"},
					"city":   {"type": "string"}
				}
			}
		},
		"properties": {
			"home": {"$ref": "#/$defs/Address"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	home := s.Properties["home"]
	if home == nil {
		t.Fatal("home property missing")
	}
	if home.Type != "object" {
		t.Errorf("home.type: got %q, want object", home.Type)
	}
	if home.Properties["street"] == nil {
		t.Error("home.street missing after $ref resolution")
	}
	if home.Ref != "#/$defs/Address" {
		t.Errorf("home.Ref: got %q, want %q", home.Ref, "#/$defs/Address")
	}
}

func TestParse_RefSiblingFile(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "shared/address.json", `{
		"type": "object",
		"properties": {
			"street": {"type": "string"}
		}
	}`)
	writeSchema(t, dir, "order.json", `{
		"type": "object",
		"properties": {
			"addr": {"$ref": "shared/address.json"}
		}
	}`)
	s, err := parseFromDir(t, dir, "order.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	addr := s.Properties["addr"]
	if addr == nil || addr.Type != "object" {
		t.Fatal("addr property missing or wrong type")
	}
	if addr.Properties["street"] == nil {
		t.Error("addr.street missing after sibling $ref resolution")
	}
}

func TestParse_PropertyOrderPreserved(t *testing.T) {
	dir := t.TempDir()
	// Intentionally non-alphabetical order: z, a, m
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"zebra":   {"type": "string"},
			"alpha":   {"type": "integer"},
			"middle":  {"type": "boolean"},
			"delta":   {"type": "number"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"zebra", "alpha", "middle", "delta"}
	if len(s.PropertyOrder) != len(want) {
		t.Fatalf("PropertyOrder len: got %d, want %d", len(s.PropertyOrder), len(want))
	}
	for i, w := range want {
		if s.PropertyOrder[i] != w {
			t.Errorf("PropertyOrder[%d]: got %q, want %q", i, s.PropertyOrder[i], w)
		}
	}
}

func TestParse_RealRequestSchema(t *testing.T) {
	// Smoke test against the actual todoorder create request schema.
	root := findModuleRoot(t)
	refPath := "examples/todoorder/contracts/http/order/create/v1/request.schema.json"
	s, err := Parse(root, refPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Type != "object" {
		t.Errorf("type: got %q, want object", s.Type)
	}
	item := s.Properties["item"]
	if item == nil {
		t.Fatal("property 'item' missing")
	}
	if item.MinLength == nil || *item.MinLength != 1 {
		t.Errorf("item.minLength: got %v, want 1", item.MinLength)
	}
	if item.MaxLength == nil || *item.MaxLength != 256 {
		t.Errorf("item.maxLength: got %v, want 256", item.MaxLength)
	}
	if s.AdditionalProperties != false {
		t.Error("additionalProperties should be false")
	}
}

// ---- fail-fast tests --------------------------------------------------------

func TestParse_FailOneOf(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"oneOf": [{"type": "string"}, {"type": "integer"}]
	}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for oneOf")
	}
	if !strings.Contains(err.Error(), `"oneOf"`) {
		t.Errorf("error should mention 'oneOf', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailAnyOf(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"anyOf": [{"type": "string"}]}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for anyOf")
	}
	if !strings.Contains(err.Error(), `"anyOf"`) {
		t.Errorf("error should mention 'anyOf', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailAllOf(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"allOf": [{"type": "string"}]}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for allOf")
	}
	if !strings.Contains(err.Error(), `"allOf"`) {
		t.Errorf("error should mention 'allOf', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailEnum(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"type": "string", "enum": ["a", "b"]}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for enum")
	}
	if !strings.Contains(err.Error(), `"enum"`) {
		t.Errorf("error should mention 'enum', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailConst(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"const": "fixed"}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for const")
	}
	if !strings.Contains(err.Error(), `"const"`) {
		t.Errorf("error should mention 'const', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailPatternProperties(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"patternProperties": {"^S_": {"type": "string"}}}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for patternProperties")
	}
	if !strings.Contains(err.Error(), `"patternProperties"`) {
		t.Errorf("error should mention 'patternProperties', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailAdditionalPropertiesSchemaObject(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"additionalProperties": {"type": "string"}
	}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for additionalProperties as schema object")
	}
	if !strings.Contains(err.Error(), `"additionalProperties"`) {
		t.Errorf("error should mention 'additionalProperties', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailTypeArray(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"type": ["string", "null"]}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for type as array")
	}
	if !strings.Contains(err.Error(), `"type"`) {
		t.Errorf("error should mention 'type', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailRefAbsoluteURL(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"$ref": "https://example.com/schema.json"}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for absolute URL $ref")
	}
	if !strings.Contains(err.Error(), "absolute URL") {
		t.Errorf("error should mention 'absolute URL', got: %v", err)
	}
	if !strings.Contains(err.Error(), "s.json") {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestParse_FailRefMissingFile(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"$ref": "nonexistent.json"}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for missing $ref file")
	}
	if !strings.Contains(err.Error(), "nonexistent.json") {
		t.Errorf("error should mention missing file, got: %v", err)
	}
}

func TestParse_FailRefUnsupportedFragment(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"$ref": "#/properties/foo"}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for unsupported fragment ref")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error should mention 'not supported', got: %v", err)
	}
}

// TestParse_NestedUnsupportedKeyword verifies that fail-fast also works for
// unsupported keywords nested inside properties.
func TestParse_NestedUnsupportedKeyword(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"foo": {"anyOf": [{"type": "string"}]}
		}
	}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for anyOf in nested property")
	}
	if !strings.Contains(err.Error(), `"anyOf"`) {
		t.Errorf("error should mention 'anyOf', got: %v", err)
	}
}

// TestParse_SourcePathPopulated verifies SourcePath is set on the root schema.
func TestParse_SourcePathPopulated(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"type": "string"}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(s.SourcePath, "s.json") {
		t.Errorf("SourcePath should end with s.json, got: %q", s.SourcePath)
	}
}

// TestParse_PropertyOrderNestedPreserved verifies that nested object property
// order is also preserved from source.
func TestParse_PropertyOrderNestedPreserved(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"outer": {
				"type": "object",
				"properties": {
					"z": {"type": "string"},
					"a": {"type": "string"},
					"m": {"type": "string"}
				}
			}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outer := s.Properties["outer"]
	if outer == nil {
		t.Fatal("outer property missing")
	}
	want := []string{"z", "a", "m"}
	if len(outer.PropertyOrder) != len(want) {
		t.Fatalf("nested PropertyOrder len: got %d, want %d", len(outer.PropertyOrder), len(want))
	}
	for i, w := range want {
		if outer.PropertyOrder[i] != w {
			t.Errorf("nested PropertyOrder[%d]: got %q, want %q", i, outer.PropertyOrder[i], w)
		}
	}
}

// TestParse_ArrayItemsPropertyOrder verifies that items' property order is preserved.
func TestParse_ArrayItemsPropertyOrder(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "array",
		"items": {
			"type": "object",
			"properties": {
				"z_field": {"type": "string"},
				"a_field": {"type": "integer"},
				"m_field": {"type": "boolean"}
			}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Items == nil {
		t.Fatal("items missing")
	}
	want := []string{"z_field", "a_field", "m_field"}
	if len(s.Items.PropertyOrder) != len(want) {
		t.Fatalf("items PropertyOrder len: got %d, want %d", len(s.Items.PropertyOrder), len(want))
	}
	for i, w := range want {
		if s.Items.PropertyOrder[i] != w {
			t.Errorf("items PropertyOrder[%d]: got %q, want %q", i, s.Items.PropertyOrder[i], w)
		}
	}
}

// TestParse_RefPreservedAfterResolution verifies Ref field is kept after $ref resolution.
func TestParse_RefPreservedAfterResolution(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "a.json", `{"type": "string"}`)
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"properties": {
			"val": {"$ref": "a.json"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	val := s.Properties["val"]
	if val == nil {
		t.Fatal("val property missing")
	}
	if val.Ref != "a.json" {
		t.Errorf("Ref should be preserved: got %q, want %q", val.Ref, "a.json")
	}
	if val.Type != "string" {
		t.Errorf("type should be resolved: got %q, want string", val.Type)
	}
}

// TestParse_CyclicRefDoesNotInfiniteLoop verifies that circular $refs are handled.
func TestParse_CyclicRefDoesNotInfiniteLoop(t *testing.T) {
	dir := t.TempDir()
	// a.json references b.json which references a.json — cycle.
	writeSchema(t, dir, "a.json", `{
		"type": "object",
		"properties": {
			"next": {"$ref": "b.json"}
		}
	}`)
	writeSchema(t, dir, "b.json", `{
		"type": "object",
		"properties": {
			"prev": {"$ref": "a.json"}
		}
	}`)
	// Should not hang; may succeed (returning cached) or return an error.
	// Run in a goroutine with a channel to verify no infinite loop.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = parseFromDir(t, dir, "a.json")
	}()
	<-done // completed without infinite loop
}

// TestParse_SkipArrayValueInProperties verifies that array-type property values
// inside a schema object are skipped correctly by the token stream (e.g. "required").
func TestParse_SkipArrayValueInSchema(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"required": ["a", "b", "c"],
		"properties": {
			"a": {"type": "string"},
			"b": {"type": "integer"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Required) != 3 {
		t.Errorf("required len: got %d, want 3", len(s.Required))
	}
	// Verify property order is correct even after the 'required' array is skipped.
	want := []string{"a", "b"}
	for i, w := range want {
		if s.PropertyOrder[i] != w {
			t.Errorf("PropertyOrder[%d]: got %q, want %q", i, s.PropertyOrder[i], w)
		}
	}
}

// TestParse_RefInItems verifies $ref inside array items is resolved.
func TestParse_RefInItems(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "item.json", `{
		"type": "object",
		"properties": {
			"id": {"type": "string"}
		}
	}`)
	writeSchema(t, dir, "s.json", `{
		"type": "array",
		"items": {"$ref": "item.json"}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Items == nil {
		t.Fatal("items missing")
	}
	if s.Items.Type != "object" {
		t.Errorf("items.type: got %q, want object", s.Items.Type)
	}
	if s.Items.Properties["id"] == nil {
		t.Error("items.id missing after $ref resolution")
	}
}

// TestParse_SchemaWithNestedArrayAndObject exercises complex nesting path
// to improve skipValue and extractRawSubValue coverage.
func TestParse_ComplexNestedSchema(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title": "complex",
		"type": "object",
		"properties": {
			"data": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"id":        {"type": "string", "format": "uuid"},
						"score":     {"type": "number", "minimum": 0, "maximum": 100},
						"tags":      {"type": "array", "items": {"type": "string"}},
						"createdAt": {"type": "string", "format": "date-time"}
					},
					"required": ["id", "score"]
				}
			},
			"nextCursor": {"type": "string"},
			"hasMore":    {"type": "boolean"}
		},
		"required": ["data", "nextCursor", "hasMore"]
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Title != "complex" {
		t.Errorf("title: got %q, want complex", s.Title)
	}
	data := s.Properties["data"]
	if data == nil || data.Type != "array" {
		t.Fatal("data array missing")
	}
	item := data.Items
	if item == nil || item.Type != "object" {
		t.Fatal("data.items missing")
	}
	tags := item.Properties["tags"]
	if tags == nil || tags.Type != "array" {
		t.Fatal("data.items.tags array missing")
	}
	if tags.Items == nil || tags.Items.Type != "string" {
		t.Fatal("tags.items.type should be string")
	}
	// Verify PropertyOrder for top-level properties preserves source order.
	wantOrder := []string{"data", "nextCursor", "hasMore"}
	for i, w := range wantOrder {
		if s.PropertyOrder[i] != w {
			t.Errorf("top PropertyOrder[%d]: got %q, want %q", i, s.PropertyOrder[i], w)
		}
	}
}

// TestParse_DefsWithMultipleEntries verifies $defs with multiple definitions.
func TestParse_DefsMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"$defs": {
			"Foo": {"type": "string", "format": "uuid"},
			"Bar": {"type": "integer", "minimum": 0}
		},
		"properties": {
			"foo": {"$ref": "#/$defs/Foo"},
			"bar": {"$ref": "#/$defs/Bar"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	foo := s.Properties["foo"]
	if foo == nil || foo.Type != "string" || foo.Format != "uuid" {
		t.Errorf("foo: got type=%q format=%q, want string/uuid", foo.Type, foo.Format)
	}
	bar := s.Properties["bar"]
	if bar == nil || bar.Type != "integer" {
		t.Errorf("bar: got type=%q, want integer", bar.Type)
	}
	if bar.Minimum == nil || *bar.Minimum != 0 {
		t.Errorf("bar.minimum: got %v, want 0", bar.Minimum)
	}
}

// TestParse_FailDefsMissingName verifies error when $defs name not found.
func TestParse_FailRefDefsMissingName(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"$defs": {},
		"properties": {
			"x": {"$ref": "#/$defs/Missing"}
		}
	}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for missing $defs entry")
	}
	if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("error should mention missing def name, got: %v", err)
	}
}

// TestTokenPropertyOrder_InternalHelpers exercises tokenPropertyOrder, streamObjectKeys,
// skipValue, and extractRawSubValue via indirect paths.
func TestTokenPropertyOrder_EmptyProperties(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"type": "object", "properties": {}}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.PropertyOrder) != 0 {
		t.Errorf("PropertyOrder should be empty, got %v", s.PropertyOrder)
	}
}

func TestTokenPropertyOrder_NoProperties(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"type": "string"}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.PropertyOrder != nil {
		t.Errorf("PropertyOrder should be nil, got %v", s.PropertyOrder)
	}
}

// ---- direct internal helper tests ------------------------------------------

func TestTokenPropertyOrder_Direct(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		data := []byte(`{"properties": {"z": {}, "a": {}, "m": {}}}`)
		keys, err := tokenPropertyOrder(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"z", "a", "m"}
		if len(keys) != len(want) {
			t.Fatalf("len: got %d, want %d", len(keys), len(want))
		}
		for i, w := range want {
			if keys[i] != w {
				t.Errorf("[%d]: got %q, want %q", i, keys[i], w)
			}
		}
	})

	t.Run("no_properties_key", func(t *testing.T) {
		data := []byte(`{"type": "string"}`)
		keys, err := tokenPropertyOrder(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if keys != nil {
			t.Errorf("expected nil, got %v", keys)
		}
	})

	t.Run("empty_properties", func(t *testing.T) {
		data := []byte(`{"properties": {}}`)
		keys, err := tokenPropertyOrder(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(keys) != 0 {
			t.Errorf("expected empty, got %v", keys)
		}
	})

	t.Run("properties_with_nested_object_values", func(t *testing.T) {
		data := []byte(`{"title":"t","properties":{"b":{"type":"string","format":"uuid"},"a":{"type":"integer"}}}`)
		keys, err := tokenPropertyOrder(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"b", "a"}
		for i, w := range want {
			if keys[i] != w {
				t.Errorf("[%d]: got %q, want %q", i, keys[i], w)
			}
		}
	})
}

func TestExtractRawSubValue_Direct(t *testing.T) {
	t.Run("happy_string_value", func(t *testing.T) {
		data := []byte(`{"type":"string","format":"uuid"}`)
		raw, err := extractRawSubValue(data, "format")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(raw) != `"uuid"` {
			t.Errorf("got %q, want %q", string(raw), `"uuid"`)
		}
	})

	t.Run("happy_object_value", func(t *testing.T) {
		data := []byte(`{"properties":{"x":{"type":"string"}}}`)
		raw, err := extractRawSubValue(data, "properties")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(raw) == 0 {
			t.Error("expected non-empty raw bytes")
		}
	})

	t.Run("key_not_found", func(t *testing.T) {
		data := []byte(`{"type":"string"}`)
		_, err := extractRawSubValue(data, "missing")
		if err == nil {
			t.Fatal("expected error for missing key")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("error should mention key, got: %v", err)
		}
	})

	t.Run("happy_array_value", func(t *testing.T) {
		data := []byte(`{"required":["a","b"]}`)
		raw, err := extractRawSubValue(data, "required")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(raw) == 0 {
			t.Error("expected non-empty raw bytes for array value")
		}
	})
}

func TestSkipValue_DirectArray(t *testing.T) {
	// Exercise skipValue with array input through tokenPropertyOrder by placing
	// an array value before "properties".
	data := []byte(`{"required":["a","b"],"properties":{"x":{},"y":{}}}`)
	keys, err := tokenPropertyOrder(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"x", "y"}
	for i, w := range want {
		if keys[i] != w {
			t.Errorf("[%d]: got %q, want %q", i, keys[i], w)
		}
	}
}

func TestSkipValue_NestedObjectBeforeProperties(t *testing.T) {
	// Exercise skipValue with nested object before properties.
	data := []byte(`{"$defs":{"Foo":{"type":"string"}},"properties":{"z":{},"a":{}}}`)
	keys, err := tokenPropertyOrder(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"z", "a"}
	for i, w := range want {
		if keys[i] != w {
			t.Errorf("[%d]: got %q, want %q", i, keys[i], w)
		}
	}
}

func TestParseSchemaBytes_InvalidJSON(t *testing.T) {
	s := &Schema{}
	visited := make(map[string]*Schema)
	err := parseSchemaFromBytes("", s, []byte(`not json`), "/fake.json", visited)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "cannot parse") {
		t.Errorf("error should mention parse failure, got: %v", err)
	}
}

// TestTokenPropertyOrder_NonObjectRoot exercises the root-is-not-object error branch.
func TestTokenPropertyOrder_NonObjectRoot(t *testing.T) {
	// A JSON array at root — not a valid schema object.
	data := []byte(`["a", "b"]`)
	_, err := tokenPropertyOrder(data)
	if err == nil {
		t.Fatal("expected error for non-object root")
	}
	if !strings.Contains(err.Error(), "expected '{'") {
		t.Errorf("error should mention expected '{', got: %v", err)
	}
}

// TestExtractRawSubValue_NonObjectRoot exercises the non-object root error branch.
func TestExtractRawSubValue_NonObjectRoot(t *testing.T) {
	data := []byte(`["a", "b"]`)
	_, err := extractRawSubValue(data, "foo")
	if err == nil {
		t.Fatal("expected error for non-object root")
	}
	if !strings.Contains(err.Error(), "expected '{'") {
		t.Errorf("error should mention expected '{', got: %v", err)
	}
}

// TestStreamObjectKeys_NonObjectValue exercises the branch where "properties" value
// is not a JSON object. We call the internal function directly.
func TestStreamObjectKeys_NonArrayProperties(t *testing.T) {
	// Build a decoder positioned at "properties" value = array (not object).
	// We achieve this by calling tokenPropertyOrder on JSON where "properties" is an array.
	// tokenPropertyOrder will call streamObjectKeys which will error.
	dec := json.NewDecoder(bytes.NewReader([]byte(`["a","b"]`)))
	_, err := streamObjectKeys(dec)
	if err == nil {
		t.Fatal("expected error when properties value is not {")
	}
}

// TestSkipValue_ArrayDelimiter exercises the '[' branch in skipValue directly.
func TestSkipValue_ArrayBranch(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader([]byte(`["a","b","c"]`)))
	skipValue(dec) // should not panic
}

// TestSkipValue_ObjectBranch exercises the '{' branch in skipValue directly.
func TestSkipValue_ObjectBranch(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"x":"y"}`)))
	skipValue(dec) // should not panic
}

// TestParseSchemaFromBytes_RefNonString exercises the $ref non-string error branch.
func TestParseSchemaFromBytes_RefNonString(t *testing.T) {
	s := &Schema{}
	visited := make(map[string]*Schema)
	err := parseSchemaFromBytes("", s, []byte(`{"$ref": 42}`), "/fake.json", visited)
	if err == nil {
		t.Fatal("expected error for non-string $ref")
	}
	if !strings.Contains(err.Error(), "$ref must be a string") {
		t.Errorf("error should mention $ref string requirement, got: %v", err)
	}
}

// TestParseSchemaFromBytes_AdditionalPropertiesUnexpected exercises the unexpected
// additionalProperties type branch (e.g. a number — neither bool nor object).
func TestParseSchemaFromBytes_AdditionalPropertiesUnexpected(t *testing.T) {
	s := &Schema{}
	visited := make(map[string]*Schema)
	err := parseSchemaFromBytes("", s, []byte(`{"additionalProperties": 42}`), "/fake.json", visited)
	if err == nil {
		t.Fatal("expected error for unexpected additionalProperties type")
	}
	if !strings.Contains(err.Error(), `"additionalProperties"`) {
		t.Errorf("error should mention additionalProperties, got: %v", err)
	}
}

// TestParseSchemaFromBytes_TypeUnexpected exercises the default/unexpected type value branch.
func TestParseSchemaFromBytes_TypeUnexpected(t *testing.T) {
	// JSON number for "type" decodes as float64, hitting the default case.
	s := &Schema{}
	visited := make(map[string]*Schema)
	err := parseSchemaFromBytes("", s, []byte(`{"type": 42}`), "/fake.json", visited)
	if err == nil {
		t.Fatal("expected error for unexpected type value")
	}
	if !strings.Contains(err.Error(), `"type"`) {
		t.Errorf("error should mention type, got: %v", err)
	}
}

// ---- tokenPropertyOrder / streamObjectKeys error-path tests -----------------
// These use a truncated JSON input to trigger mid-stream decode errors.

// TestTokenPropertyOrder_TruncatedBeforeValue exercises error when the value
// after a non-"properties" key is missing (truncated input).
func TestTokenPropertyOrder_TruncatedAtRoot(t *testing.T) {
	// Truncated after opening brace — dec.Token() in the key position will fail.
	data := []byte(`{`)
	// This returns EOF — should not error (More() returns false).
	keys, err := tokenPropertyOrder(data)
	if err != nil {
		// EOF from dec.More() is fine; some JSON decoders propagate it.
		t.Logf("got error (acceptable): %v", err)
	}
	_ = keys
}

// TestTokenPropertyOrder_TruncatedMidValue exercises error when value after key
// is truncated (dec.Token() will return error in skipValue).
func TestTokenPropertyOrder_TruncatedMidValue(t *testing.T) {
	// "type" key present but its string value is truncated.
	data := []byte(`{"type":`)
	_, _ = tokenPropertyOrder(data)
	// We accept either nil or error — the point is it doesn't panic.
}

// TestStreamObjectKeys_TruncatedInput exercises streamObjectKeys with truncated input.
func TestStreamObjectKeys_TruncatedInsideObject(t *testing.T) {
	// Properties object is truncated after opening brace.
	dec := json.NewDecoder(bytes.NewReader([]byte(`{`)))
	keys, err := streamObjectKeys(dec)
	// EOF from first Token() call is an error.
	if err != nil {
		t.Logf("got expected error: %v", err)
	} else if len(keys) != 0 {
		t.Errorf("expected no keys, got %v", keys)
	}
}

// TestSkipValue_TruncatedScalar exercises skipValue with empty input (no-op).
func TestSkipValue_TruncatedInput(t *testing.T) {
	// Empty input — Token() returns EOF; skipValue returns without panicking.
	dec := json.NewDecoder(bytes.NewReader([]byte(``)))
	skipValue(dec) // should not panic
}

// TestExtractRawSubValue_TruncatedAfterKey exercises the mid-stream error when
// the value after the target key is missing.
func TestExtractRawSubValue_TruncatedAfterKey(t *testing.T) {
	data := []byte(`{"foo":`)
	_, err := extractRawSubValue(data, "foo")
	if err == nil {
		t.Fatal("expected error for truncated input")
	}
}

// TestResolveRef_HttpRef exercises the https:// rejection branch via Parse.
func TestResolveRef_HttpRef(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, "s.json", `{"$ref": "http://example.com/schema.json"}`)
	_, err := parseFromDir(t, dir, "s.json")
	if err == nil {
		t.Fatal("expected error for http:// ref")
	}
	if !strings.Contains(err.Error(), "absolute URL") {
		t.Errorf("error should mention absolute URL, got: %v", err)
	}
}

// TestParse_RefPathTraversal_Rejected verifies that a $ref attempting to escape
// the rootDir via path traversal (e.g. "../../../../etc/passwd") is rejected with
// an error that mentions "outside root".
func TestParse_RefPathTraversal_Rejected(t *testing.T) {
	rootDir := t.TempDir()

	// Write an entry schema that tries to traverse outside rootDir.
	writeSchema(t, rootDir, "entry.json", `{
		"type": "object",
		"properties": {
			"val": {"$ref": "../../../../etc/passwd"}
		}
	}`)

	_, err := Parse(rootDir, "entry.json")
	if err == nil {
		t.Fatal("expected error for path-traversal $ref, got nil")
	}
	if !strings.Contains(err.Error(), "outside root") {
		t.Errorf("error should mention 'outside root', got: %v", err)
	}
	// Error message should include the attempted traversal path.
	if !strings.Contains(err.Error(), "../../../../etc/passwd") {
		t.Errorf("error should include the traversal ref path, got: %v", err)
	}
}

// TestParse_DefsPropertyOrderPreservedSourceOrder verifies that properties within
// a $defs definition retain their source document order rather than being
// re-sorted alphabetically (B.3: $defs order loss fix).
func TestParse_DefsPropertyOrderPreservedSourceOrder(t *testing.T) {
	dir := t.TempDir()
	// Properties z_field, a_field, m_field are intentionally non-alphabetical to
	// detect if json.Marshal re-sort would silently reorder them.
	writeSchema(t, dir, "s.json", `{
		"type": "object",
		"$defs": {
			"Item": {
				"type": "object",
				"properties": {
					"z_field": {"type": "string"},
					"a_field": {"type": "integer"},
					"m_field": {"type": "boolean"}
				}
			}
		},
		"properties": {
			"item": {"$ref": "#/$defs/Item"}
		}
	}`)
	s, err := parseFromDir(t, dir, "s.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	item := s.Properties["item"]
	if item == nil {
		t.Fatal("item property missing")
	}
	if item.Type != "object" {
		t.Errorf("item.type: got %q, want object", item.Type)
	}
	want := []string{"z_field", "a_field", "m_field"}
	if len(item.PropertyOrder) != len(want) {
		t.Fatalf("PropertyOrder len: got %d (%v), want %d", len(item.PropertyOrder), item.PropertyOrder, len(want))
	}
	for i, w := range want {
		if item.PropertyOrder[i] != w {
			t.Errorf("PropertyOrder[%d]: got %q, want %q (full order: %v)", i, item.PropertyOrder[i], w, item.PropertyOrder)
		}
	}
}

// TestParse_UnsupportedKeywordUsesRelativePath verifies that error messages for
// unsupported keywords use a repo-relative path rather than the full absolute
// sandbox path (B.4: relPath error messages).
func TestParse_UnsupportedKeywordUsesRelativePath(t *testing.T) {
	rootDir := t.TempDir()
	writeSchema(t, rootDir, "sub/request.schema.json", `{
		"type": "object",
		"properties": {
			"foo": {"oneOf": [{"type": "string"}, {"type": "integer"}]}
		}
	}`)
	_, err := Parse(rootDir, "sub/request.schema.json")
	if err == nil {
		t.Fatal("expected error for oneOf keyword")
	}
	// Error must not contain the absolute rootDir prefix.
	if strings.Contains(err.Error(), rootDir) {
		t.Errorf("error should not contain absolute rootDir %q, got: %v", rootDir, err)
	}
	// Error must contain the repo-relative path.
	if !strings.Contains(err.Error(), "sub/request.schema.json") {
		t.Errorf("error should contain repo-relative path, got: %v", err)
	}
}
