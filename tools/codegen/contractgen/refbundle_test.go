package contractgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/runtime/schemavalidate"
)

// TestBundleSchemaRefs tests the bundleSchemaRefs function which resolves external
// $ref entries in a request schema into a self-contained, fully-inlined document.
// The embedded runtime schema must compile with schemavalidate.NewValidator (santhosh-tekuri
// base URI "mem:///", no loader) so external $ref must NOT appear in the output.
func TestBundleSchemaRefs(t *testing.T) {
	t.Run("inlines_external_ref", func(t *testing.T) {
		root := t.TempDir()

		// Write the mixin (shared) schema.
		mixinContent := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://example.com/mixin.json",
  "title": "MyMixin",
  "description": "A CAS version guard.",
  "type": "integer",
  "minimum": 1,
  "maximum": 99999
}`
		mixinPath := filepath.Join(root, "shared", "mixin.json")
		if err := os.MkdirAll(filepath.Dir(mixinPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mixinPath, []byte(mixinContent), 0o600); err != nil {
			t.Fatal(err)
		}

		// Write the request schema that references the mixin.
		reqContent := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "request",
  "type": "object",
  "properties": {
    "name": { "type": "string" },
    "expectedVersion": { "$ref": "../shared/mixin.json" }
  },
  "required": ["name", "expectedVersion"],
  "additionalProperties": false
}`
		reqPath := filepath.Join(root, "req", "request.schema.json")
		if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reqPath, []byte(reqContent), 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := bundleSchemaRefs(root, reqPath, []byte(reqContent))
		if err != nil {
			t.Fatalf("bundleSchemaRefs: %v", err)
		}

		// Must not contain any $ref.
		if strings.Contains(string(out), `"$ref"`) {
			t.Errorf("output still contains $ref: %s", out)
		}

		// The inlined expectedVersion must carry type/minimum/maximum/description from mixin.
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("output is not valid JSON: %v", err)
		}
		props, ok := doc["properties"].(map[string]any)
		if !ok {
			t.Fatal("output has no properties object")
		}
		ev, ok := props["expectedVersion"].(map[string]any)
		if !ok {
			t.Fatal("expectedVersion property not found in output")
		}
		if ev["type"] != "integer" {
			t.Errorf("expectedVersion.type = %v, want integer", ev["type"])
		}
		if ev["description"] != "A CAS version guard." {
			t.Errorf("expectedVersion.description = %v, want 'A CAS version guard.'", ev["description"])
		}
		if min, ok := ev["minimum"].(float64); !ok || min != 1 {
			t.Errorf("expectedVersion.minimum = %v, want 1", ev["minimum"])
		}
		if max, ok := ev["maximum"].(float64); !ok || max != 99999 {
			t.Errorf("expectedVersion.maximum = %v, want 99999", ev["maximum"])
		}

		// Mixin-level $schema, $id, title must NOT appear on the inlined field.
		for _, key := range []string{"$schema", "$id", "title"} {
			if _, present := ev[key]; present {
				t.Errorf("inlined field unexpectedly contains mixin key %q", key)
			}
		}

		// The output schema must compile with schemavalidate.NewValidator.
		if _, vErr := schemavalidate.NewValidator(out); vErr != nil {
			t.Errorf("bundled schema fails to compile: %v", vErr)
		}
	})

	t.Run("passthrough_no_ref", func(t *testing.T) {
		root := t.TempDir()

		raw := []byte(`{"type":"object","properties":{"n":{"type":"string"}},"additionalProperties":false}`)
		schemaPath := filepath.Join(root, "request.schema.json")
		if err := os.WriteFile(schemaPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := bundleSchemaRefs(root, schemaPath, raw)
		if err != nil {
			t.Fatalf("bundleSchemaRefs: %v", err)
		}
		if !bytes.Equal(out, raw) {
			t.Errorf("passthrough: output differs from input\ngot:  %s\nwant: %s", out, raw)
		}
	})

	t.Run("path_traversal_rejected", func(t *testing.T) {
		root := t.TempDir()

		// Write a mixin OUTSIDE root (sibling of root).
		outsideDir := t.TempDir()
		mixinPath := filepath.Join(outsideDir, "secret.json")
		if err := os.WriteFile(mixinPath, []byte(`{"type":"integer"}`), 0o600); err != nil {
			t.Fatal(err)
		}

		// Compute a relative path from reqPath to outsideDir mixin.
		reqDir := filepath.Join(root, "req")
		if err := os.MkdirAll(reqDir, 0o755); err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(reqDir, mixinPath)
		if err != nil {
			t.Fatal(err)
		}

		reqContent := `{"type":"object","properties":{"ev":{"$ref":"` + rel + `"}}}`
		reqPath := filepath.Join(reqDir, "request.schema.json")
		if err := os.WriteFile(reqPath, []byte(reqContent), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err = bundleSchemaRefs(root, reqPath, []byte(reqContent))
		if err == nil {
			t.Error("expected error for path traversal, got nil")
		}
	})

	t.Run("absolute_url_rejected", func(t *testing.T) {
		root := t.TempDir()

		raw := []byte(`{"type":"object","properties":{"ev":{"$ref":"https://example.com/foo.json"}}}`)
		schemaPath := filepath.Join(root, "request.schema.json")
		if err := os.WriteFile(schemaPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := bundleSchemaRefs(root, schemaPath, raw)
		if err == nil {
			t.Error("expected error for absolute URL $ref, got nil")
		}
	})

	t.Run("absolute_path_rejected", func(t *testing.T) {
		root := t.TempDir()

		raw := []byte(`{"type":"object","properties":{"ev":{"$ref":"/etc/passwd"}}}`)
		schemaPath := filepath.Join(root, "request.schema.json")
		if err := os.WriteFile(schemaPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := bundleSchemaRefs(root, schemaPath, raw)
		if err == nil {
			t.Error("expected error for absolute path $ref, got nil")
		}
	})

	t.Run("non_string_ref_rejected", func(t *testing.T) {
		root := t.TempDir()

		raw := []byte(`{"type":"object","properties":{"ev":{"$ref":42}}}`)
		schemaPath := filepath.Join(root, "request.schema.json")
		if err := os.WriteFile(schemaPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := bundleSchemaRefs(root, schemaPath, raw)
		if err == nil {
			t.Error("expected error for non-string $ref, got nil")
		}
	})

	t.Run("sibling_keys_dropped_on_ref_node", func(t *testing.T) {
		// A $ref node that carries sibling keys alongside "$ref" (e.g. a
		// "description" annotation) must be REPLACED wholesale by the resolved
		// target — the sibling keys are NOT merged into the output. This matches
		// the assertCanonicalRefOnly archtest rule: "no extra keys alongside $ref".
		root := t.TempDir()

		mixinContent := `{"type":"integer","minimum":1,"maximum":99999}`
		mixinPath := filepath.Join(root, "shared", "version.json")
		if err := os.MkdirAll(filepath.Dir(mixinPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mixinPath, []byte(mixinContent), 0o600); err != nil {
			t.Fatal(err)
		}

		// The $ref node carries a sibling "description" key — intentionally
		// non-canonical to document that sibling is dropped on replacement.
		reqContent := `{
  "type": "object",
  "properties": {
    "expectedVersion": { "$ref": "../shared/version.json", "description": "x" }
  },
  "additionalProperties": false
}`
		reqPath := filepath.Join(root, "req", "request.schema.json")
		if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reqPath, []byte(reqContent), 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := bundleSchemaRefs(root, reqPath, []byte(reqContent))
		if err != nil {
			t.Fatalf("bundleSchemaRefs: %v", err)
		}

		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("output is not valid JSON: %v", err)
		}
		props, ok := doc["properties"].(map[string]any)
		if !ok {
			t.Fatal("output has no properties object")
		}
		ev, ok := props["expectedVersion"].(map[string]any)
		if !ok {
			t.Fatal("expectedVersion property not found in output")
		}

		// The resolved node must carry the mixin's fields.
		if ev["type"] != "integer" {
			t.Errorf("expectedVersion.type = %v, want integer", ev["type"])
		}

		// The sibling "description" from the $ref node must NOT appear —
		// the node was replaced, not merged.
		if _, present := ev["description"]; present {
			t.Errorf("sibling 'description' key must be dropped when $ref node is replaced, but it is present: %v", ev["description"])
		}

		// No $ref must remain in the output.
		if strings.Contains(string(out), `"$ref"`) {
			t.Errorf("output still contains $ref: %s", out)
		}
	})

	t.Run("same_document_ref_preserved", func(t *testing.T) {
		// A same-document JSON Pointer ($ref: "#/$defs/...") is NOT a file
		// reference — santhosh-tekuri resolves it natively against the bundled
		// root. The bundler must leave it intact (no file IO, no error) and the
		// $defs block must survive so the runtime validator can resolve it.
		root := t.TempDir()

		reqContent := `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "expectedVersion": { "$ref": "#/$defs/Version" }
  },
  "required": ["expectedVersion"],
  "additionalProperties": false,
  "$defs": {
    "Version": { "type": "integer", "minimum": 1, "maximum": 99999 }
  }
}`
		reqPath := filepath.Join(root, "req", "request.schema.json")
		if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reqPath, []byte(reqContent), 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := bundleSchemaRefs(root, reqPath, []byte(reqContent))
		if err != nil {
			t.Fatalf("bundleSchemaRefs must not error on same-document $ref: %v", err)
		}

		// The "#/$defs/Version" ref must be preserved (not inlined, not read as a file).
		if !strings.Contains(string(out), `"#/$defs/Version"`) {
			t.Errorf("same-document $ref must be preserved in output, got: %s", out)
		}
		// The $defs block must survive for runtime resolution.
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("output is not valid JSON: %v", err)
		}
		if _, ok := doc["$defs"].(map[string]any); !ok {
			t.Errorf("$defs block must survive bundling, got: %s", out)
		}
		// santhosh-tekuri must compile it (same-document ref resolves at runtime).
		if _, vErr := schemavalidate.NewValidator(out); vErr != nil {
			t.Errorf("schema with preserved same-document $ref fails to compile: %v", vErr)
		}
	})

	t.Run("nested_ref_inlined", func(t *testing.T) {
		root := t.TempDir()

		// Mixin lives in shared/.
		mixinContent := `{"type":"integer","minimum":0,"maximum":10}`
		mixinPath := filepath.Join(root, "shared", "count.json")
		if err := os.MkdirAll(filepath.Dir(mixinPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mixinPath, []byte(mixinContent), 0o600); err != nil {
			t.Fatal(err)
		}

		// Request schema has a nested object with a $ref.
		reqContent := `{
  "type": "object",
  "properties": {
    "metadata": {
      "type": "object",
      "properties": {
        "count": { "$ref": "../../shared/count.json" }
      }
    }
  },
  "additionalProperties": false
}`
		reqPath := filepath.Join(root, "deep", "v1", "request.schema.json")
		if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reqPath, []byte(reqContent), 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := bundleSchemaRefs(root, reqPath, []byte(reqContent))
		if err != nil {
			t.Fatalf("bundleSchemaRefs: %v", err)
		}
		if strings.Contains(string(out), `"$ref"`) {
			t.Errorf("output still contains $ref: %s", out)
		}
		if _, vErr := schemavalidate.NewValidator(out); vErr != nil {
			t.Errorf("nested bundled schema fails to compile: %v", vErr)
		}
	})
}
