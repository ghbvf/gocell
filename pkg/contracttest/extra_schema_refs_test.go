package contracttest

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestContractYAML_ExtraSchemaRefs proves that extra (non-standard) schema ref
// keys survive YAML parsing. This was the L6 bug: the previous schemaRefsYAML
// struct lacked the Extra field, silently dropping unknown keys.
func TestContractYAML_ExtraSchemaRefs(t *testing.T) {
	dir := filepath.Join(testdataRoot(), "http", "test", "extrarefs", "v1")

	data, err := os.ReadFile(filepath.Join(dir, "contract.yaml"))
	if err != nil {
		t.Fatalf("read contract.yaml: %v", err)
	}

	var cy contractYAML
	if err := yaml.Unmarshal(data, &cy); err != nil {
		t.Fatalf("parse contract.yaml: %v", err)
	}

	if cy.SchemaRefs.Response == "" {
		t.Error("SchemaRefs.Response should be populated, got empty string")
	}

	if len(cy.SchemaRefs.Extra) == 0 {
		t.Fatal("SchemaRefs.Extra should contain the non-standard key 'customKey', got empty map")
	}

	got, ok := cy.SchemaRefs.Extra["customKey"]
	if !ok {
		t.Fatalf("SchemaRefs.Extra missing 'customKey'; Extra = %v", cy.SchemaRefs.Extra)
	}
	if got != "custom.schema.json" {
		t.Errorf("SchemaRefs.Extra[customKey] = %q, want %q", got, "custom.schema.json")
	}
}

// TestLoad_ExtraSchemaRefs confirms the full Load path with a non-standard
// schemaRefs key: the contract loads successfully, known refs are compiled,
// and extra refs are accessible via ValidateSchemaRef.
func TestLoad_ExtraSchemaRefs(t *testing.T) {
	dir := filepath.Join(testdataRoot(), "http", "test", "extrarefs", "v1")
	c := Load(t, dir)

	if c.ID != "http.test.extrarefs.v1" {
		t.Errorf("ID = %q, want %q", c.ID, "http.test.extrarefs.v1")
	}
	// Prove that the response schema was compiled by validating a minimal document.
	c.ValidateResponse(t, []byte(`{"data":{}}`))

	// Prove that ValidateSchemaRef dispatches to extra schemas.
	// custom.schema.json requires {"code": <string>}.
	c.ValidateSchemaRef(t, "customKey", []byte(`{"code":"ok"}`))

	// Prove that ValidateSchemaRef rejects invalid data against the extra schema.
	mockT := &mockTB{}
	c.ValidateSchemaRef(mockT, "customKey", []byte(`{}`)) // missing required "code"
	if !mockT.failed {
		t.Error("expected ValidateSchemaRef to fail for missing required field, but it passed")
	}

	// Prove that ValidateSchemaRef is a no-op for unknown keys.
	c.ValidateSchemaRef(t, "nonexistentKey", []byte(`{"anything":"goes"}`))

	// Prove that ValidateSchemaRef dispatches well-known keys correctly.
	c.ValidateSchemaRef(t, "response", []byte(`{"data":{}}`))
}
