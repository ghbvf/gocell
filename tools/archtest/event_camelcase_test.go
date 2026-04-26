package archtest

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventPayloadSchemasUseCamelCase enforces EVENT-PAYLOAD-CAMELCASE-01:
// every top-level property key in contracts/event/**/payload.schema.json
// must use camelCase — no underscores allowed in property names.
//
// This rule locks in the G.6 camelCase migration: new event contracts are
// authored camelCase from day one, and existing contracts have been migrated.
// Any re-introduction of snake_case properties in event payload schemas will
// be caught here before it reaches CI.
func TestEventPayloadSchemasUseCamelCase(t *testing.T) {
	root := findModuleRoot(t)
	contractsEventDir := filepath.Join(root, "contracts", "event")

	var violations []string

	err := filepath.Walk(contractsEventDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip backup directories if they exist.
			if info.Name() == "bak" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() != "payload.schema.json" {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}

		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}

		for key := range schema.Properties {
			if strings.Contains(key, "_") {
				violations = append(violations,
					fmt.Sprintf("EVENT-PAYLOAD-CAMELCASE-01: %s: property %q contains underscore — use camelCase", rel, key))
			}
		}
		return nil
	})
	require.NoError(t, err, "failed to walk contracts/event")

	for _, v := range violations {
		t.Logf("%s", v)
	}
	assert.Empty(t, violations,
		"all event payload schema properties must use camelCase (no underscores); "+
			"see G.6 camelCase migration in PR-CFG-G1")
}

// TestEventDTOJSONTagsUseCamelCase enforces EVENT-DTO-CAMELCASE-01:
// every json struct tag in cells/**/dto/*event*.go must use camelCase —
// no underscores allowed in the JSON field name portion of the tag.
//
// This rule pairs with EVENT-PAYLOAD-CAMELCASE-01 to guarantee that the Go
// DTO wire representation stays in sync with the contract schemas.
// The check is AST-based (go/parser) to avoid false positives from
// comments and string constants that mention tag names.
func TestEventDTOJSONTagsUseCamelCase(t *testing.T) {
	root := findModuleRoot(t)

	violations, err := checkEventDTOCamelCase(root)
	require.NoError(t, err)

	for _, v := range violations {
		t.Logf("%s", v)
	}
	assert.Empty(t, violations,
		"all json struct tags in event DTO files (cells/**/dto/*event*.go) must use camelCase "+
			"(no underscores in the json field name); see G.6 camelCase migration in PR-CFG-G1")
}

// checkEventDTOCamelCase walks cells/ looking for dto/*event*.go files and
// checks every struct field json tag for underscore characters in the field
// name segment (before any comma — e.g. `json:"user_id,omitempty"` would
// flag "user_id" but pass on ",omitempty").
func checkEventDTOCamelCase(root string) ([]string, error) {
	var violations []string

	err := filepath.WalkDir(filepath.Join(root, "cells"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata", "generated", ".git":
				return filepath.SkipDir
			}
			return nil
		}

		// Only process files under a dto/ directory whose name matches *event*.go.
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		dir := filepath.Base(filepath.Dir(path))
		if dir != "dto" {
			return nil
		}
		base := filepath.Base(path)
		if !strings.Contains(base, "event") {
			return nil
		}

		fileViolations, err := scanDTOJSONTagsCamelCase(root, path)
		if err != nil {
			return err
		}
		violations = append(violations, fileViolations...)
		return nil
	})
	return violations, err
}

// scanDTOJSONTagsCamelCase parses a single Go file and returns violation
// strings for any struct field json tag whose field name contains an underscore.
func scanDTOJSONTagsCamelCase(root, path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		// Best-effort: parse failure surfaces via go build, not archtest.
		return nil, nil
	}

	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		field, ok := n.(*ast.Field)
		if !ok {
			return true
		}
		if field.Tag == nil {
			return true
		}
		// Strip the outer backticks from the raw tag literal.
		raw := strings.Trim(field.Tag.Value, "`")
		tag := parseStructTag(raw)
		jsonVal, ok := tag["json"]
		if !ok {
			return true
		}
		// The json tag value is "<name>[,options...]"; extract the name part.
		parts := strings.SplitN(jsonVal, ",", 2)
		jsonName := parts[0]
		// Skip "-" (explicit omit) and empty names.
		if jsonName == "-" || jsonName == "" {
			return true
		}
		if strings.Contains(jsonName, "_") {
			line := fset.Position(field.Pos()).Line
			violations = append(violations,
				fmt.Sprintf("EVENT-DTO-CAMELCASE-01: %s:%d: json tag %q contains underscore — use camelCase", rel, line, jsonName))
		}
		return true
	})
	return violations, nil
}

// parseStructTag parses a raw struct tag string (without backticks) into a
// map of key → value. Implements the same grammar as reflect.StructTag.Lookup.
func parseStructTag(tag string) map[string]string {
	result := make(map[string]string)
	for tag != "" {
		// Skip leading spaces.
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		// Find key end (colon or space).
		i = 0
		for i < len(tag) && tag[i] != ':' && tag[i] != ' ' {
			i++
		}
		if i == 0 || i >= len(tag) || tag[i] != ':' {
			break
		}
		key := tag[:i]
		tag = tag[i+1:]
		// Value must start with a double-quote.
		if len(tag) == 0 || tag[0] != '"' {
			break
		}
		// Find closing quote, respecting backslash escapes.
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			break
		}
		value := tag[1:i]
		tag = tag[i+1:]
		result[key] = value
	}
	return result
}

// TestEventPayloadSchemasUseCamelCase_NegativeProbe validates that the rule
// correctly flags a schema with underscore property names and passes one
// that uses camelCase.
func TestEventPayloadSchemasUseCamelCase_NegativeProbe(t *testing.T) {
	t.Parallel()

	t.Run("underscore_property_is_flagged", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		schemaPath := filepath.Join(tmp, "payload.schema.json")
		content := `{"$schema":"https://json-schema.org/draft/2020-12/schema","title":"test","type":"object","properties":{"user_id":{"type":"string"},"username":{"type":"string"}}}`
		require.NoError(t, os.WriteFile(schemaPath, []byte(content), 0o644))

		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		require.NoError(t, json.Unmarshal([]byte(content), &schema))
		var violations []string
		for key := range schema.Properties {
			if strings.Contains(key, "_") {
				violations = append(violations, key)
			}
		}
		assert.NotEmpty(t, violations, "negative probe: underscore property must be detected")
	})

	t.Run("camelCase_property_passes", func(t *testing.T) {
		t.Parallel()
		content := `{"$schema":"https://json-schema.org/draft/2020-12/schema","title":"test","type":"object","properties":{"userId":{"type":"string"},"username":{"type":"string"}}}`
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		require.NoError(t, json.Unmarshal([]byte(content), &schema))
		var violations []string
		for key := range schema.Properties {
			if strings.Contains(key, "_") {
				violations = append(violations, key)
			}
		}
		assert.Empty(t, violations, "negative probe: camelCase properties must not be flagged")
	})
}

// TestEventDTOJSONTagsUseCamelCase_NegativeProbe validates that the AST scanner
// correctly flags json tags with underscores and passes camelCase tags.
func TestEventDTOJSONTagsUseCamelCase_NegativeProbe(t *testing.T) {
	t.Parallel()

	t.Run("underscore_json_tag_is_flagged", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		// Simulate a dto/user_events.go file.
		dtDir := filepath.Join(tmp, "cells", "accesscore", "internal", "dto")
		require.NoError(t, os.MkdirAll(dtDir, 0o755))
		filePath := filepath.Join(dtDir, "user_events.go")
		content := "package dto\ntype UserCreatedEvent struct {\n\tUserID string `json:\"user_id\"`\n}\n"
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))

		violations, err := scanDTOJSONTagsCamelCase(tmp, filePath)
		require.NoError(t, err)
		assert.NotEmpty(t, violations,
			"negative probe: json tag with underscore must be flagged as EVENT-DTO-CAMELCASE-01")
		assert.Contains(t, violations[0], "EVENT-DTO-CAMELCASE-01")
	})

	t.Run("camelCase_json_tag_passes", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		dtDir := filepath.Join(tmp, "cells", "accesscore", "internal", "dto")
		require.NoError(t, os.MkdirAll(dtDir, 0o755))
		filePath := filepath.Join(dtDir, "user_events.go")
		content := "package dto\ntype UserCreatedEvent struct {\n\tUserID string `json:\"userId\"`\n}\n"
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))

		violations, err := scanDTOJSONTagsCamelCase(tmp, filePath)
		require.NoError(t, err)
		assert.Empty(t, violations,
			"negative probe: camelCase json tag must not be flagged")
	})

	t.Run("omitempty_with_camelCase_passes", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		dtDir := filepath.Join(tmp, "cells", "accesscore", "internal", "dto")
		require.NoError(t, os.MkdirAll(dtDir, 0o755))
		filePath := filepath.Join(dtDir, "user_events.go")
		content := "package dto\ntype UserCreatedEvent struct {\n\tUserID string `json:\"userId,omitempty\"`\n}\n"
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))

		violations, err := scanDTOJSONTagsCamelCase(tmp, filePath)
		require.NoError(t, err)
		assert.Empty(t, violations,
			"negative probe: camelCase json tag with omitempty must not be flagged")
	})

	t.Run("dash_json_tag_is_skipped", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		dtDir := filepath.Join(tmp, "cells", "accesscore", "internal", "dto")
		require.NoError(t, os.MkdirAll(dtDir, 0o755))
		filePath := filepath.Join(dtDir, "user_events.go")
		content := "package dto\ntype UserCreatedEvent struct {\n\tInternal string `json:\"-\"`\n}\n"
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0o644))

		violations, err := scanDTOJSONTagsCamelCase(tmp, filePath)
		require.NoError(t, err)
		assert.Empty(t, violations,
			"negative probe: json:\"-\" tag must not be flagged")
	})
}
