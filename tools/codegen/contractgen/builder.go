package contractgen

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contracts"
)

// BuildContractSpec projects a single contract.yaml + its schemaRefs into a
// ContractGenSpec. Returns error when:
//   - contract not found in project
//   - Codegen flag is false
//   - schemaRef parsing fails
//   - kind=http but http endpoint missing
//   - kind=event but payload schemaRef missing
func BuildContractSpec(rootDir string, p *metadata.ProjectMeta, contractID string) (*ContractGenSpec, error) {
	if p == nil {
		return nil, fmt.Errorf("contractgen build: project is nil")
	}
	contract, ok := p.Contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("contractgen build: contract %q not found", contractID)
	}
	if !contract.Codegen {
		return nil, fmt.Errorf("contractgen build: contract %q has codegen=false", contractID)
	}

	pkgPath := contractIDToPackagePath(contractID)
	pkgName := goPackageName(lastSegment(pkgPath))

	spec := &ContractGenSpec{
		PackageName: pkgName,
		PackagePath: pkgPath,
		ContractID:  contractID,
		Kind:        contract.Kind,
		SourceFile:  contract.File,
	}

	contractDir := filepath.Dir(contract.File)

	switch contract.Kind {
	case "http":
		if err := buildHTTPSpec(spec, rootDir, contract, contractDir); err != nil {
			return nil, err
		}
	case "event":
		if err := buildEventSpec(spec, rootDir, contract, contractDir); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("contractgen build: contract %q has unsupported kind %q (http|event only)", contractID, contract.Kind)
	}

	return spec, nil
}

func buildHTTPSpec(spec *ContractGenSpec, rootDir string, contract *metadata.ContractMeta, contractDir string) error {
	http := contract.Endpoints.HTTP
	if http == nil {
		return fmt.Errorf("contractgen build: contract %q is kind=http but has no http endpoint", contract.ID)
	}

	// Build DTOs from schema refs.
	var allDTOs []DTOSpec

	// Request DTO — may be an empty object (GET without body).
	if contract.SchemaRefs.Request != "" {
		reqPath := filepath.Join(contractDir, contract.SchemaRefs.Request)
		reqSchema, err := Parse(rootDir, reqPath)
		if err != nil {
			return fmt.Errorf("contractgen build: %q request schema: %w", contract.ID, err)
		}
		reqDTOs, err := schemaToDTOs("Request", reqSchema)
		if err != nil {
			return fmt.Errorf("contractgen build: %q request DTOs: %w", contract.ID, err)
		}
		allDTOs = append(allDTOs, reqDTOs...)
	}

	// Response DTO.
	if contract.SchemaRefs.Response != "" {
		respPath := filepath.Join(contractDir, contract.SchemaRefs.Response)
		respSchema, err := Parse(rootDir, respPath)
		if err != nil {
			return fmt.Errorf("contractgen build: %q response schema: %w", contract.ID, err)
		}
		respDTOs, err := schemaToDTOs("Response", respSchema)
		if err != nil {
			return fmt.Errorf("contractgen build: %q response DTOs: %w", contract.ID, err)
		}
		allDTOs = append(allDTOs, respDTOs...)
	}

	// Merge path and query params into Request DTO.
	allDTOs = mergeParamsIntoRequest(allDTOs, http)

	spec.DTOs = allDTOs

	// Build HTTPEndpointSpec.
	handlerMethod := goPascalCase(domainLastSegment(contract.ID))
	hasBody := http.Method == "POST" || http.Method == "PUT" || http.Method == "PATCH"

	endpointSpec := &HTTPEndpointSpec{
		Method:        http.Method,
		Path:          http.Path,
		SuccessCode:   http.SuccessStatus,
		NoContent:     http.NoContent,
		HandlerMethod: handlerMethod,
		HasBody:       hasBody,
	}

	// Path params — maintain declaration order using path extraction.
	endpointSpec.PathParams = buildPathParams(http)
	// Query params.
	endpointSpec.QueryParams = buildQueryParams(http)

	spec.Endpoint = endpointSpec
	return nil
}

func buildEventSpec(spec *ContractGenSpec, rootDir string, contract *metadata.ContractMeta, contractDir string) error {
	if contract.SchemaRefs.Payload == "" {
		return fmt.Errorf("contractgen build: contract %q is kind=event but has no payload schemaRef", contract.ID)
	}

	var allDTOs []DTOSpec

	// Payload DTO.
	payloadPath := filepath.Join(contractDir, contract.SchemaRefs.Payload)
	payloadSchema, err := Parse(rootDir, payloadPath)
	if err != nil {
		return fmt.Errorf("contractgen build: %q payload schema: %w", contract.ID, err)
	}
	payloadDTOs, err := schemaToDTOs("Payload", payloadSchema)
	if err != nil {
		return fmt.Errorf("contractgen build: %q payload DTOs: %w", contract.ID, err)
	}
	allDTOs = append(allDTOs, payloadDTOs...)

	// Headers DTO (optional).
	if contract.SchemaRefs.Headers != "" {
		headersPath := filepath.Join(contractDir, contract.SchemaRefs.Headers)
		headersSchema, err := Parse(rootDir, headersPath)
		if err != nil {
			return fmt.Errorf("contractgen build: %q headers schema: %w", contract.ID, err)
		}
		headersDTOs, err := schemaToDTOs("Headers", headersSchema)
		if err != nil {
			return fmt.Errorf("contractgen build: %q headers DTOs: %w", contract.ID, err)
		}
		allDTOs = append(allDTOs, headersDTOs...)
	}

	spec.DTOs = allDTOs

	// Build EventEndpointSpec.
	topic := stripVersionSuffix(contract.ID)
	domainLast := domainLastSegment(contract.ID)
	handlerMethod := "Handle" + goPascalCase(domainLast)

	replayable := false
	if contract.Replayable != nil {
		replayable = *contract.Replayable
	}

	spec.Event = &EventEndpointSpec{
		Topic:             topic,
		HandlerMethod:     handlerMethod,
		Replayable:        replayable,
		DeliverySemantics: contract.DeliverySemantics,
	}
	return nil
}

// mergeParamsIntoRequest injects path and query params as fields into the
// Request DTO. If no Request DTO exists, one is created. Field order:
// path params first, query params second, then body schema fields.
func mergeParamsIntoRequest(dtos []DTOSpec, http *contracts.HTTPTransport) []DTOSpec {
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)

	if len(pathParams) == 0 && len(queryParams) == 0 {
		return dtos
	}

	// Find or create Request DTO.
	reqIdx := -1
	for i, d := range dtos {
		if d.Name == "Request" {
			reqIdx = i
			break
		}
	}
	if reqIdx == -1 {
		dtos = append([]DTOSpec{{Name: "Request", Doc: "Request holds the HTTP request parameters."}}, dtos...)
		reqIdx = 0
	}

	// Prepend path param fields, then query param fields, before any body fields.
	var prefixFields []DTOField
	for _, p := range pathParams {
		field := DTOField{
			Name:     p.GoName,
			JSONTag:  p.Name + ",omitempty",
			GoType:   p.GoType,
			Required: p.Required,
			Doc:      p.Doc,
		}
		if p.Required {
			field.JSONTag = p.Name
		}
		prefixFields = append(prefixFields, field)
	}
	for _, q := range queryParams {
		field := DTOField{
			Name:     q.GoName,
			JSONTag:  q.Name + ",omitempty",
			GoType:   q.GoType,
			Required: q.Required,
			Doc:      q.Doc,
		}
		if q.Required {
			field.JSONTag = q.Name
		}
		prefixFields = append(prefixFields, field)
	}

	dtos[reqIdx].Fields = append(prefixFields, dtos[reqIdx].Fields...)
	return dtos
}

// buildPathParams extracts path parameters from HTTPTransport in path-template order.
func buildPathParams(http *contracts.HTTPTransport) []ParamSpec {
	if len(http.PathParams) == 0 {
		return nil
	}
	// Extract param names in path order by scanning the path template.
	names := pathParamNamesFromPath(http.Path)
	var out []ParamSpec
	for _, name := range names {
		schema, ok := http.PathParams[name]
		if !ok {
			continue
		}
		out = append(out, ParamSpec{
			Name:      name,
			GoName:    goPascalCase(name),
			GoType:    paramGoType(schema.Type),
			Required:  true, // path params are always required
			Doc:       paramDoc(schema),
			MinLength: paramMinLength(schema),
			MaxLength: paramMaxLength(schema),
			Minimum:   paramMinimum(schema),
			Maximum:   paramMaximum(schema),
		})
	}
	return out
}

// buildQueryParams extracts query parameters from HTTPTransport in map key-sorted order.
// Note: contract.yaml queryParams is a map, so we use a deterministic iteration order
// by scanning all keys alphabetically.
func buildQueryParams(http *contracts.HTTPTransport) []ParamSpec {
	if len(http.QueryParams) == 0 {
		return nil
	}
	// Collect and sort for stability.
	names := make([]string, 0, len(http.QueryParams))
	for name := range http.QueryParams {
		names = append(names, name)
	}
	sortStrings(names)

	var out []ParamSpec
	for _, name := range names {
		schema := http.QueryParams[name]
		required := false
		if schema.Required != nil {
			required = *schema.Required
		}
		out = append(out, ParamSpec{
			Name:      name,
			GoName:    goPascalCase(name),
			GoType:    paramGoType(schema.Type),
			Required:  required,
			Doc:       paramDoc(schema),
			MinLength: paramMinLength(schema),
			MaxLength: paramMaxLength(schema),
			Minimum:   paramMinimum(schema),
			Maximum:   paramMaximum(schema),
		})
	}
	return out
}

// schemaToDTOs flattens a Schema (type=object root) into a list of DTOSpecs.
// The root schema becomes rootName; nested object properties are named
// <Parent><Child> and appended after the parent.
// Returns error if root schema is not type=object.
func schemaToDTOs(rootName string, s *Schema) ([]DTOSpec, error) {
	if s.Type != "object" {
		return nil, fmt.Errorf("contractgen: schema for %q must be type=object, got %q", rootName, s.Type)
	}
	var out []DTOSpec
	collectDTOs(rootName, s, &out)
	return out, nil
}

// collectDTOs recursively collects DTOSpecs from an object schema.
// All types are appended to out in DFS pre-order (parent before children).
func collectDTOs(name string, s *Schema, out *[]DTOSpec) {
	dto := DTOSpec{Name: name, Doc: s.Title}

	// Track which nested names need recursive collection.
	type nestedEntry struct {
		nestedName string
		schema     *Schema
	}
	var nested []nestedEntry

	for _, key := range s.PropertyOrder {
		prop := s.Properties[key]
		required := isRequired(key, s.Required)
		fieldName := goPascalCase(key)
		jsonTag := key
		if !required {
			jsonTag = key + ",omitempty"
		}

		goType, nestedName := schemaGoType(key, name, prop)
		doc := ""
		if prop.Format == "uuid" || prop.Format == "date-time" {
			doc = "format: " + prop.Format
		}

		dto.Fields = append(dto.Fields, DTOField{
			Name:     fieldName,
			JSONTag:  jsonTag,
			GoType:   goType,
			Required: required,
			Doc:      doc,
		})

		// Track nested objects for recursive collection after the parent is appended.
		if nestedName != "" {
			if prop.Type == "object" {
				nested = append(nested, nestedEntry{nestedName: nestedName, schema: prop})
			} else if prop.Type == "array" && prop.Items != nil && prop.Items.Type == "object" {
				// Array items object uses a composite name <parent><field>Item.
				nested = append(nested, nestedEntry{nestedName: nestedName, schema: prop.Items})
			}
		}
	}

	// Append parent first (pre-order).
	*out = append(*out, dto)

	// Then recurse into nested types.
	for _, n := range nested {
		collectDTOs(n.nestedName, n.schema, out)
	}
}

// schemaGoType returns the Go type expression for a schema property.
// nestedName is non-empty when the type refers to a generated nested struct.
func schemaGoType(fieldKey, parentName string, s *Schema) (goType string, nestedName string) {
	switch s.Type {
	case "string":
		return "string", ""
	case "integer":
		return "int64", ""
	case "number":
		return "float64", ""
	case "boolean":
		return "bool", ""
	case "object":
		nested := parentName + goPascalCase(fieldKey)
		return "*" + nested, nested
	case "array":
		if s.Items == nil {
			return "[]any", ""
		}
		itemType, nestedItem := schemaGoType(fieldKey, parentName, s.Items)
		if nestedItem != "" {
			// Items is an object — use slice of pointer.
			nested := parentName + goPascalCase(fieldKey) + "Item"
			return "[]*" + nested, nested
		}
		return "[]" + itemType, ""
	default:
		return "any", ""
	}
}

// isRequired reports whether name appears in the required slice.
func isRequired(name string, required []string) bool {
	for _, r := range required {
		if r == name {
			return true
		}
	}
	return false
}

// contractIDToPackagePath converts a contract id to a module-relative generated path.
// "http.order.create.v1" → "generated/contracts/http/order/create/v1".
// "event.order-created.v1" → "generated/contracts/event/order-created/v1".
func contractIDToPackagePath(id string) string {
	parts := strings.Split(id, ".")
	// The last segment is the version (v1, v2, ...).
	// Middle segments form the domain path.
	segments := make([]string, len(parts))
	copy(segments, parts)
	return "generated/contracts/" + strings.Join(segments, "/")
}

// lastSegment returns the last "/" separated segment of a path.
func lastSegment(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

// domainLastSegment returns the second-to-last dot-separated segment,
// which is the domain action (create, get, order-created, etc.).
// For "http.order.create.v1" returns "create".
// For "event.order-created.v1" returns "order-created".
func domainLastSegment(contractID string) string {
	parts := strings.Split(contractID, ".")
	if len(parts) < 2 {
		return contractID
	}
	// Last part is version, second-to-last is the action.
	return parts[len(parts)-2]
}

// stripVersionSuffix removes the trailing version segment from a contract id.
// "event.order-created.v1" → "event.order-created".
func stripVersionSuffix(id string) string {
	parts := strings.Split(id, ".")
	if len(parts) < 2 {
		return id
	}
	last := parts[len(parts)-1]
	if isVersionSegment(last) {
		return strings.Join(parts[:len(parts)-1], ".")
	}
	return id
}

func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// goPascalCase converts a kebab-case or camelCase or snake_case identifier
// to PascalCase. Handles delimiters "-" and "_".
// "order-created" → "OrderCreated", "userId" → "UserId", "user_id" → "UserId".
func goPascalCase(s string) string {
	if s == "" {
		return s
	}
	parts := splitOnDelimiters(s)
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(capitalizeFirst(p))
	}
	return sb.String()
}

// goPackageName converts a path segment to a valid Go package name.
// Strips dashes, lowercases the result.
// "order-created" → "ordercreated"
// "create" → "create"
// Returns error on Go reserved words.
func goPackageName(s string) string {
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return strings.ToLower(s)
}

// splitOnDelimiters splits on "-" and "_" and also handles camelCase boundaries.
func splitOnDelimiters(s string) []string {
	// First split on explicit delimiters.
	s = strings.ReplaceAll(s, "-", "_")
	return strings.Split(s, "_")
}

// capitalizeFirst uppercases the first rune of s.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// pathParamNamesFromPath extracts parameter names from a chi-style path template,
// preserving the order in which they appear in the path string.
// "/api/v1/orders/{id}/items/{itemId}" → ["id", "itemId"].
func pathParamNamesFromPath(path string) []string {
	var names []string
	rest := path
	for {
		start := strings.Index(rest, "{")
		if start == -1 {
			break
		}
		end := strings.Index(rest[start:], "}")
		if end == -1 {
			break
		}
		name := rest[start+1 : start+end]
		names = append(names, name)
		rest = rest[start+end+1:]
	}
	return names
}

// paramGoType converts a ParamSchema type string to a Go type.
func paramGoType(t string) string {
	switch t {
	case "integer":
		return "int64"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	default:
		return "string"
	}
}

// paramDoc builds a doc hint for a param schema.
func paramDoc(s contracts.ParamSchema) string {
	if s.Format != "" {
		return "format: " + s.Format
	}
	return ""
}

func paramMinLength(s contracts.ParamSchema) *int {
	return s.MinLength
}

func paramMaxLength(s contracts.ParamSchema) *int {
	return s.MaxLength
}

func paramMinimum(s contracts.ParamSchema) *int64 {
	if s.Minimum == nil {
		return nil
	}
	v := int64(*s.Minimum)
	return &v
}

func paramMaximum(s contracts.ParamSchema) *int64 {
	if s.Maximum == nil {
		return nil
	}
	v := int64(*s.Maximum)
	return &v
}

// sortStrings sorts a string slice in-place.
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}
