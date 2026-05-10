// INVARIANT: PATCH-OPTIONAL-BOOL-POINTER-01
//
// PATCH-OPTIONAL-BOOL-POINTER-01 — archtest gate.
//
// Scans all generated/contracts/http/**/types_gen.go for Request structs that
// belong to PATCH endpoints. Any optional boolean field (not in the JSON Schema
// required list) in a PATCH Request MUST be *bool, not bool. A bare bool makes
// it impossible to distinguish {"requirePasswordReset": false} (explicit clear)
// from an absent field (no change), breaking PATCH semantics.
//
// Scope: generated/contracts/http/**/<endpoint-dir>/v*/types_gen.go where the
// endpoint directory segment matches a PATCH contract.
//
// ref: kubernetes/api core/v1/types.go optional bool fields (*bool convention)
// ref: oapi-codegen SkipOptionalPointer default false (generates *bool)
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const patchOptionalBoolRule = "PATCH-OPTIONAL-BOOL-POINTER-01"

// TestPatchOptionalBoolPointer01 scans all PATCH http contracts' generated
// types_gen.go files and asserts every optional bool field in the Request
// struct is typed *bool.
//
// Detection strategy:
//  1. Parse project metadata to find all http contracts with method=PATCH.
//  2. For each such contract, locate the generated types_gen.go.
//  3. AST-parse the file; find the Request struct.
//  4. For each field in Request: if the JSON Schema required list does NOT
//     contain this field and the Go type is `bool` (not `*bool`) → violation.
//
// The "required" determination is made from the contract's JSON Schema, not
// from the Go field tag (omitempty is insufficient — it allows false but
// omits it on marshal, not on unmarshal).
func TestPatchOptionalBoolPointer01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	project := mustParseProjectContracts(t, root)

	var violations []string

	for contractID, contract := range project.Contracts {
		if contract.Kind != "http" {
			continue
		}
		http := contract.Endpoints.HTTP
		if http == nil || http.Method != "PATCH" {
			continue
		}
		if !contract.Codegen {
			continue
		}

		// Locate generated types_gen.go.
		pkgDir := filepath.Join(root, contractIDToExpectedPkgPath(contractID))
		typesPath := filepath.Join(pkgDir, "types_gen.go")
		if _, err := os.Stat(typesPath); err != nil {
			// Missing file is handled by CODEGEN-CONTRACT-GEN-01; skip here.
			continue
		}

		// Load the JSON Schema required fields for this contract's request body.
		requiredFields := loadSchemaRequiredFields(t, root, contract)

		// AST-scan the Request struct in types_gen.go.
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, typesPath, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("%s: parse %s: %v", patchOptionalBoolRule, typesPath, err)
			continue
		}

		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name == nil || ts.Name.Name != "Request" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return false
			}

			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					continue // embedded field
				}
				fieldName := field.Names[0].Name
				jsonName := jsonTagName(field)
				if jsonName == "" {
					jsonName = fieldName
				}

				// Skip required fields — required bool is unambiguous.
				if requiredFields[jsonName] {
					continue
				}

				// Check if type is bare `bool` (not `*bool`).
				if isBareIdent(field.Type, "bool") {
					line := fset.Position(field.Names[0].Pos()).Line
					violations = append(violations, formatViolation(
						patchOptionalBoolRule,
						typesPath,
						line,
						contractID,
						fieldName,
					))
				}
			}
			return false
		})
	}

	for _, v := range violations {
		t.Error(v)
	}
}

// TestPatchOptionalBoolPointer01_NegativeFixture verifies the scanner catches
// a known-bad types_gen.go with a bare bool on a PATCH Request.
func TestPatchOptionalBoolPointer01_NegativeFixture(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	fixtureFile := filepath.Join(archDir, "testdata", "patch_optional_bool_fixtures", "bad_patch_bool", "types_gen.go")

	if _, err := os.Stat(fixtureFile); err != nil {
		t.Fatalf("negative fixture not found at %s — run the fixture setup", fixtureFile)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, fixtureFile, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	foundBool := false
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Request" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
		}
		for _, field := range st.Fields.List {
			if len(field.Names) > 0 && field.Names[0].Name == "Flag" {
				if isBareIdent(field.Type, "bool") {
					foundBool = true
				}
			}
		}
		return false
	})

	if !foundBool {
		t.Errorf("negative fixture %s does not contain a bare bool field 'Flag' in Request — fixture is broken", fixtureFile)
	}
}

// --- helpers ----------------------------------------------------------------

// loadSchemaRequiredFields reads the request schema for a contract and returns
// the set of field names that are in the JSON Schema "required" array.
// Returns an empty map when no request schema exists or required is empty.
func loadSchemaRequiredFields(t *testing.T, root string, contract *metadata.ContractMeta) map[string]bool {
	t.Helper()
	if contract.SchemaRefs.Request == "" {
		return nil
	}
	contractDir := filepath.Dir(contract.File)
	schemaPath := filepath.Join(root, contractDir, contract.SchemaRefs.Request)

	data, err := os.ReadFile(schemaPath) //nolint:gosec // schema path from project metadata
	if err != nil {
		return nil
	}

	// Minimal JSON parse: find "required": [...] array.
	required := extractRequiredFromSchemaJSON(string(data))
	out := make(map[string]bool, len(required))
	for _, r := range required {
		out[r] = true
	}
	return out
}

// extractRequiredFromSchemaJSON is a simple string-based extractor for the
// "required" array in a JSON Schema. We avoid importing encoding/json here to
// keep the archtest dependency footprint minimal.
func extractRequiredFromSchemaJSON(data string) []string {
	// Find "required": [...]
	const marker = `"required"`
	idx := strings.Index(data, marker)
	if idx == -1 {
		return nil
	}
	start := strings.Index(data[idx:], "[")
	if start == -1 {
		return nil
	}
	end := strings.Index(data[idx+start:], "]")
	if end == -1 {
		return nil
	}
	arr := data[idx+start+1 : idx+start+end]

	var names []string
	for _, part := range strings.Split(arr, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		if part != "" {
			names = append(names, part)
		}
	}
	return names
}

// isBareIdent reports whether expr is an *ast.Ident with name == ident.
// Returns false for *ast.StarExpr (pointer types).
func isBareIdent(expr ast.Expr, ident string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == ident
}

// jsonTagName extracts the JSON field name from a struct field's json tag.
// Returns the first comma-separated segment; returns "" when no json tag exists.
func jsonTagName(field *ast.Field) string {
	if field.Tag == nil {
		return ""
	}
	tag := strings.Trim(field.Tag.Value, "`")
	// Find json:"..." segment.
	const prefix = `json:"`
	idx := strings.Index(tag, prefix)
	if idx == -1 {
		return ""
	}
	rest := tag[idx+len(prefix):]
	end := strings.Index(rest, `"`)
	if end == -1 {
		return ""
	}
	name := rest[:end]
	// Strip ,omitempty and similar qualifiers.
	if comma := strings.Index(name, ","); comma != -1 {
		name = name[:comma]
	}
	return name
}

// formatViolation formats a single PATCH-OPTIONAL-BOOL-POINTER-01 violation message.
func formatViolation(rule, file string, line int, contractID, fieldName string) string {
	return rule + ": " + file + ":" + formatInt(line) +
		": contract " + contractID +
		": Request field " + fieldName +
		" is bare bool — PATCH optional bool must be *bool" +
		" (absent=nil, &false=clear, &true=set)"
}

// formatInt converts an int to its decimal string representation without
// importing strconv (keeping archtest dependencies lean).
func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	result := make([]byte, 0, 10)
	for n > 0 {
		result = append(result, byte('0'+n%10))
		n /= 10
	}
	// Reverse.
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}
