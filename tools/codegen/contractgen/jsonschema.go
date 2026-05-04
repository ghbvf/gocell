package contractgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Schema represents the minimal subset of JSON Schema draft 2020-12 used by contractgen.
// PropertyOrder preserves the source order of properties keys for stable diffs.
type Schema struct {
	Type                 string             // "string" | "integer" | "number" | "boolean" | "object" | "array"
	Format               string             // "uuid" | "date-time" | "int64" | ""
	Properties           map[string]*Schema // type=object
	PropertyOrder        []string           // source order of property keys
	Required             []string           // type=object
	Items                *Schema            // type=array
	Ref                  string             // "$ref" original value (preserved for traceability)
	AdditionalProperties bool               // bool only; schema form is unsupported (true = JSON Schema default)
	Title                string
	MinLength            *int   // type=string; pointer distinguishes 0 from unset
	MaxLength            *int   // type=string
	Minimum              *int64 // numeric types
	Maximum              *int64
	SourcePath           string // file path + JSON pointer for error messages
}

// Parse loads and parses a single JSON Schema file, recursively resolving $ref.
//
//   - rootDir: absolute path to the project root
//   - refPath: path relative to rootDir
//     (e.g. "examples/todoorder/contracts/.../request.schema.json")
//
// Supported $ref forms:
//   - same-file: "#/$defs/<name>"
//   - relative sibling: "../shared/foo.json" etc.
//
// Unsupported keywords cause an immediate error containing the file path and JSON pointer.
func Parse(rootDir, refPath string) (*Schema, error) {
	absPath := filepath.Join(rootDir, refPath)
	visited := make(map[string]*Schema)
	return parseFile(absPath, visited)
}

// parseFile reads the file at absPath and parses it as a JSON Schema.
func parseFile(absPath string, visited map[string]*Schema) (*Schema, error) {
	absPath = filepath.Clean(absPath)

	if s, ok := visited[absPath]; ok {
		return s, nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("contractgen/jsonschema: cannot read %s: %w", absPath, err)
	}

	schema := &Schema{SourcePath: absPath}
	// Register before recursing to break cycles.
	visited[absPath] = schema

	if err := parseSchemaFromBytes(schema, data, absPath, visited); err != nil {
		return nil, err
	}
	return schema, nil
}

// parseSchemaFromBytes parses raw JSON bytes into s.
// absFile is the absolute path of the file being parsed.
func parseSchemaFromBytes(s *Schema, data []byte, absFile string, visited map[string]*Schema) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("contractgen/jsonschema: cannot parse %s: %w", absFile, err)
	}

	var defs map[string]any
	if defsRaw, ok := raw["$defs"].(map[string]any); ok {
		defs = defsRaw
	}

	propOrder, err := tokenPropertyOrder(data)
	if err != nil {
		return fmt.Errorf("contractgen/jsonschema: cannot extract property order at %s: %w", absFile, err)
	}

	return fillSchema(s, raw, propOrder, data, absFile, "#", defs, absFile, visited)
}

// schemaContext holds the shared parameters passed through the recursive fill calls.
type schemaContext struct {
	currentFile string
	defs        map[string]any
	rootFile    string
	visited     map[string]*Schema
}

// fillSchema populates s from the decoded rawNode.
func fillSchema(
	s *Schema,
	rawNode map[string]any,
	propOrder []string,
	rawBytes []byte,
	currentFile string,
	jsonPtr string,
	defs map[string]any,
	rootFile string,
	visited map[string]*Schema,
) error {
	ctx := schemaContext{currentFile: currentFile, defs: defs, rootFile: rootFile, visited: visited}
	return fillSchemaWithCtx(s, rawNode, propOrder, rawBytes, jsonPtr, ctx)
}

// fillSchemaWithCtx is the internal implementation that carries context separately.
func fillSchemaWithCtx(
	s *Schema,
	rawNode map[string]any,
	propOrder []string,
	rawBytes []byte,
	jsonPtr string,
	ctx schemaContext,
) error {
	loc := ctx.currentFile + jsonPtr

	if err := checkUnsupportedKeywords(rawNode, loc); err != nil {
		return err
	}

	if _, ok := rawNode["$ref"]; ok {
		return fillRef(s, rawNode, jsonPtr, ctx)
	}

	if err := fillScalars(s, rawNode, loc); err != nil {
		return err
	}

	if err := fillItems(s, rawNode, rawBytes, jsonPtr, ctx); err != nil {
		return err
	}

	return fillProperties(s, rawNode, propOrder, rawBytes, jsonPtr, ctx)
}

// checkUnsupportedKeywords returns an error if rawNode contains any unsupported JSON Schema keyword.
func checkUnsupportedKeywords(rawNode map[string]any, loc string) error {
	for _, kw := range []string{
		"oneOf", "anyOf", "allOf", "not",
		"patternProperties", "dependentSchemas", "dependentRequired",
		"enum", "const",
	} {
		if _, exists := rawNode[kw]; exists {
			return fmt.Errorf("contractgen/jsonschema: unsupported keyword %q at %s", kw, loc)
		}
	}
	return nil
}

// fillRef resolves a $ref and copies the resolved schema into s.
func fillRef(s *Schema, rawNode map[string]any, jsonPtr string, ctx schemaContext) error {
	loc := ctx.currentFile + jsonPtr
	refStr, isStr := rawNode["$ref"].(string)
	if !isStr {
		return fmt.Errorf("contractgen/jsonschema: $ref must be a string at %s", loc)
	}
	s.Ref = refStr
	resolved, err := resolveRef(refStr, ctx.currentFile, ctx.defs, ctx.rootFile, jsonPtr, ctx.visited)
	if err != nil {
		return err
	}
	sp := s.SourcePath
	ref := s.Ref
	*s = *resolved
	s.SourcePath = sp
	s.Ref = ref
	return nil
}

// fillScalars populates the scalar fields (title, type, format, additionalProperties, constraints).
func fillScalars(s *Schema, rawNode map[string]any, loc string) error {
	if v, ok := rawNode["title"].(string); ok {
		s.Title = v
	}

	if err := fillType(s, rawNode, loc); err != nil {
		return err
	}

	if v, ok := rawNode["format"].(string); ok {
		s.Format = v
	}

	if err := fillAdditionalProperties(s, rawNode, loc); err != nil {
		return err
	}

	fillRequired(s, rawNode)
	fillNumericConstraints(s, rawNode)
	return nil
}

// fillType sets s.Type from rawNode["type"], failing on unsupported forms.
func fillType(s *Schema, rawNode map[string]any, loc string) error {
	switch tv := rawNode["type"].(type) {
	case string:
		s.Type = tv
	case []any:
		return fmt.Errorf("contractgen/jsonschema: unsupported keyword \"type\" as array at %s", loc)
	case nil:
		// type may be omitted
	default:
		return fmt.Errorf("contractgen/jsonschema: unexpected \"type\" value at %s", loc)
	}
	return nil
}

// fillAdditionalProperties sets s.AdditionalProperties from rawNode.
func fillAdditionalProperties(s *Schema, rawNode map[string]any, loc string) error {
	apRaw, exists := rawNode["additionalProperties"]
	if !exists {
		s.AdditionalProperties = true // JSON Schema default
		return nil
	}
	switch ap := apRaw.(type) {
	case bool:
		s.AdditionalProperties = ap
	case map[string]any:
		return fmt.Errorf("contractgen/jsonschema: unsupported keyword \"additionalProperties\" as schema object at %s", loc)
	default:
		return fmt.Errorf("contractgen/jsonschema: unexpected \"additionalProperties\" value at %s", loc)
	}
	return nil
}

// fillRequired populates s.Required from rawNode.
func fillRequired(s *Schema, rawNode map[string]any) {
	reqRaw, ok := rawNode["required"].([]any)
	if !ok {
		return
	}
	for _, r := range reqRaw {
		if rs, ok := r.(string); ok {
			s.Required = append(s.Required, rs)
		}
	}
}

// fillNumericConstraints sets minLength, maxLength, minimum, maximum on s.
func fillNumericConstraints(s *Schema, rawNode map[string]any) {
	if v, ok := rawNode["minLength"].(float64); ok {
		iv := int(v)
		s.MinLength = &iv
	}
	if v, ok := rawNode["maxLength"].(float64); ok {
		iv := int(v)
		s.MaxLength = &iv
	}
	if v, ok := rawNode["minimum"].(float64); ok {
		iv := int64(v)
		s.Minimum = &iv
	}
	if v, ok := rawNode["maximum"].(float64); ok {
		iv := int64(v)
		s.Maximum = &iv
	}
}

// fillItems parses the "items" keyword and sets s.Items.
func fillItems(s *Schema, rawNode map[string]any, rawBytes []byte, jsonPtr string, ctx schemaContext) error {
	itemsRaw, ok := rawNode["items"].(map[string]any)
	if !ok {
		return nil
	}
	itemsPtr := jsonPtr + "/items"
	itemsLoc := ctx.currentFile + itemsPtr

	itemsBytes, err := extractRawSubValue(rawBytes, "items")
	if err != nil {
		return fmt.Errorf("contractgen/jsonschema: cannot extract raw items at %s: %w", itemsLoc, err)
	}
	itemOrder, err := tokenPropertyOrder(itemsBytes)
	if err != nil {
		return fmt.Errorf("contractgen/jsonschema: cannot extract property order for items at %s: %w", itemsLoc, err)
	}
	itemSchema := &Schema{SourcePath: itemsLoc}
	if err := fillSchemaWithCtx(itemSchema, itemsRaw, itemOrder, itemsBytes, itemsPtr, ctx); err != nil {
		return err
	}
	s.Items = itemSchema
	return nil
}

// fillProperties parses the "properties" keyword and populates s.Properties and s.PropertyOrder.
func fillProperties(s *Schema, rawNode map[string]any, propOrder []string, rawBytes []byte, jsonPtr string, ctx schemaContext) error {
	propsRaw, ok := rawNode["properties"].(map[string]any)
	if !ok {
		return nil
	}
	loc := ctx.currentFile + jsonPtr

	propsBytes, err := extractRawSubValue(rawBytes, "properties")
	if err != nil {
		return fmt.Errorf("contractgen/jsonschema: cannot extract raw properties at %s: %w", loc, err)
	}

	s.Properties = make(map[string]*Schema, len(propsRaw))
	s.PropertyOrder = propOrder

	for _, key := range propOrder {
		propMap, ok := propsRaw[key].(map[string]any)
		if !ok {
			return fmt.Errorf("contractgen/jsonschema: property %q value is not an object at %s", key, loc)
		}
		propPtr := jsonPtr + "/properties/" + key
		propLoc := ctx.currentFile + propPtr

		propBytes, err := extractRawSubValue(propsBytes, key)
		if err != nil {
			return fmt.Errorf("contractgen/jsonschema: cannot extract raw property %q at %s: %w", key, propLoc, err)
		}
		nestedOrder, err := tokenPropertyOrder(propBytes)
		if err != nil {
			return fmt.Errorf("contractgen/jsonschema: cannot extract property order for %q at %s: %w", key, propLoc, err)
		}
		propSchema := &Schema{SourcePath: propLoc}
		if err := fillSchemaWithCtx(propSchema, propMap, nestedOrder, propBytes, propPtr, ctx); err != nil {
			return err
		}
		s.Properties[key] = propSchema
	}
	return nil
}

// resolveRef resolves a $ref string.
// defs is the $defs map of the root file for same-file #/$defs references.
func resolveRef(
	ref string,
	currentFile string,
	defs map[string]any,
	rootFile string,
	jsonPtr string,
	visited map[string]*Schema,
) (*Schema, error) {
	loc := currentFile + jsonPtr

	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return nil, fmt.Errorf("contractgen/jsonschema: $ref absolute URL not supported %q at %s", ref, loc)
	}

	if strings.HasPrefix(ref, "#/$defs/") {
		return resolveDefsRef(ref, currentFile, defs, rootFile, jsonPtr, visited)
	}

	if strings.HasPrefix(ref, "#") {
		return nil, fmt.Errorf("contractgen/jsonschema: $ref fragment %q not supported (only #/$defs/<name>) at %s", ref, loc)
	}

	targetAbs := filepath.Clean(filepath.Join(filepath.Dir(currentFile), ref))
	resolved, err := parseFile(targetAbs, visited)
	if err != nil {
		return nil, fmt.Errorf("contractgen/jsonschema: cannot resolve $ref %q at %s: %w", ref, loc, err)
	}
	return resolved, nil
}

// resolveDefsRef resolves a "#/$defs/<name>" reference.
func resolveDefsRef(
	ref string,
	currentFile string,
	defs map[string]any,
	rootFile string,
	jsonPtr string,
	visited map[string]*Schema,
) (*Schema, error) {
	loc := currentFile + jsonPtr
	name := strings.TrimPrefix(ref, "#/$defs/")
	defRaw, ok := defs[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("contractgen/jsonschema: $ref %q: definition not found in $defs at %s", ref, loc)
	}
	// json.Marshal on map[string]any from Unmarshal always succeeds.
	defBytes, _ := json.Marshal(defRaw)
	// tokenPropertyOrder on Marshal output always succeeds.
	defOrder, _ := tokenPropertyOrder(defBytes)
	defSchema := &Schema{SourcePath: fmt.Sprintf("%s#/$defs/%s", rootFile, name)}
	ctx := schemaContext{currentFile: currentFile, defs: defs, rootFile: rootFile, visited: visited}
	if err := fillSchemaWithCtx(defSchema, defRaw, defOrder, defBytes, "/$defs/"+name, ctx); err != nil {
		return nil, err
	}
	return defSchema, nil
}

// tokenPropertyOrder scans raw JSON bytes and returns the keys of the "properties"
// object in the order they appear in the source. It uses json.Decoder for token
// streaming, which preserves JSON key order as it appears in the input.
//
// Precondition: data must be a valid JSON object (callers pre-validate with json.Unmarshal).
func tokenPropertyOrder(data []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected '{' at root, got %v", tok)
	}

	for dec.More() {
		keyTok, _ := dec.Token()
		key, _ := keyTok.(string)
		if key == "properties" {
			return streamObjectKeys(dec)
		}
		skipValue(dec)
	}
	return nil, nil
}

// streamObjectKeys reads a JSON object from dec and returns its keys in order.
// The decoder must be positioned just before the opening '{'.
func streamObjectKeys(dec *json.Decoder) ([]string, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected '{' for properties, got %v", tok)
	}

	var keys []string
	for dec.More() {
		keyTok, _ := dec.Token()
		key, _ := keyTok.(string)
		keys = append(keys, key)
		skipValue(dec)
	}
	_, _ = dec.Token() // consume closing '}'
	return keys, nil
}

// skipValue skips a single JSON value from dec (scalar, object, or array).
//
// Precondition: data backing dec is valid JSON.
func skipValue(dec *json.Decoder) {
	tok, err := dec.Token()
	if err != nil {
		return
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return
	}

	var closing json.Delim
	switch d {
	case '{':
		closing = '}'
	case '[':
		closing = ']'
	default:
		return
	}
	for dec.More() {
		skipValue(dec)
	}
	_, _ = dec.Token() // consume closing delimiter
	_ = closing
}

// extractRawSubValue extracts the raw JSON bytes of the value for the given key
// from a JSON object. The object is scanned via token streaming to preserve order.
//
// Precondition: data must be a valid JSON object (callers pre-validate with json.Unmarshal).
func extractRawSubValue(data []byte, key string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected '{', got %v", tok)
	}

	for dec.More() {
		keyTok, _ := dec.Token()
		k, _ := keyTok.(string)
		if k != key {
			skipValue(dec)
			continue
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return nil, fmt.Errorf("key %q not found", key)
}
