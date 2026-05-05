package contractgen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/ghbvf/gocell/kernel/metadata"
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
	pkgName := pkgNameFromContractID(contractID)

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

	allDTOs, err := buildHTTPDTOs(rootDir, contract, contractDir, http)
	if err != nil {
		return err
	}
	spec.DTOs = allDTOs

	endpointSpec, err := buildHTTPEndpointSpec(contract, http)
	if err != nil {
		return err
	}
	spec.Endpoint = endpointSpec
	return nil
}

// buildHTTPDTOs loads request/response schemas, converts them to DTOSpecs, and
// merges path/query params into the Request DTO.
func buildHTTPDTOs(
	rootDir string,
	contract *metadata.ContractMeta,
	contractDir string,
	http *metadata.HTTPTransportMeta,
) ([]DTOSpec, error) {
	var allDTOs []DTOSpec

	// Request DTO — may be an empty object (GET without body).
	if contract.SchemaRefs.Request != "" {
		reqPath := filepath.Join(contractDir, contract.SchemaRefs.Request)
		reqSchema, err := Parse(rootDir, reqPath)
		if err != nil {
			return nil, fmt.Errorf("contractgen build: %q request schema: %w", contract.ID, err)
		}
		reqDTOs, err := schemaToDTOs("Request", reqSchema)
		if err != nil {
			return nil, fmt.Errorf("contractgen build: %q request DTOs: %w", contract.ID, err)
		}
		allDTOs = append(allDTOs, reqDTOs...)
	}

	// Response DTO.
	if contract.SchemaRefs.Response != "" {
		respPath := filepath.Join(contractDir, contract.SchemaRefs.Response)
		respSchema, err := Parse(rootDir, respPath)
		if err != nil {
			return nil, fmt.Errorf("contractgen build: %q response schema: %w", contract.ID, err)
		}
		respDTOs, err := schemaToDTOs("Response", respSchema)
		if err != nil {
			return nil, fmt.Errorf("contractgen build: %q response DTOs: %w", contract.ID, err)
		}
		allDTOs = append(allDTOs, respDTOs...)
	}

	// Ensure Request stub exists — handler_gen.go and iface_gen.go always reference
	// *Request, so we must generate it even when there are no body fields or params.
	if !hasDTONamed(allDTOs, "Request") {
		allDTOs = append([]DTOSpec{{
			Name: "Request",
			Doc:  contract.ID + ".request",
		}}, allDTOs...)
	}

	// Ensure Response stub exists for non-noContent endpoints — iface_gen.go always
	// references *Response. noContent endpoints (204) still have a (*Response, error)
	// return signature; the handler discards the value with _ = resp.
	if !hasDTONamed(allDTOs, "Response") {
		allDTOs = append(allDTOs, DTOSpec{
			Name: "Response",
			Doc:  contract.ID + ".response",
		})
	}

	// Merge path and query params into Request DTO.
	merged, mergeErr := mergeParamsIntoRequestWithID(allDTOs, http, contract.ID)
	if mergeErr != nil {
		return nil, fmt.Errorf("contractgen build: %q merge params: %w", contract.ID, mergeErr)
	}
	return merged, nil
}

// hasDTONamed reports whether dtos contains a DTOSpec with the given name.
func hasDTONamed(dtos []DTOSpec, name string) bool {
	for _, d := range dtos {
		if d.Name == name {
			return true
		}
	}
	return false
}

// buildHTTPEndpointSpec constructs the HTTPEndpointSpec including pagination detection.
// HasBody is true only when the HTTP method is POST/PUT/PATCH AND the contract declares
// a schemaRefs.request — POST/PATCH endpoints that accept only path params (no request
// body schema) must not call DecodeJSONStrict (an empty body would be rejected).
func buildHTTPEndpointSpec(contract *metadata.ContractMeta, http *metadata.HTTPTransportMeta) (*HTTPEndpointSpec, error) {
	handlerMethod := goPascalCase(domainLastSegment(contract.ID))
	methodHasBody := http.Method == "POST" || http.Method == "PUT" || http.Method == "PATCH"
	hasBody := methodHasBody && contract.SchemaRefs.Request != ""

	// Clients are only wired into the generated contractSpec for /internal/v1/...
	// paths. For public /api/v1/... paths, contract.yaml endpoints.clients is
	// informational metadata (who calls this endpoint) — auth.Mount rejects
	// Clients on non-internal paths (wrapper.ContractSpec validation rule).
	var clients []string
	isInternalPath := strings.HasPrefix(http.Path, "/internal/v1/") || http.Path == "/internal/v1"
	if isInternalPath && len(contract.Endpoints.Clients) > 0 {
		clients = append(clients, contract.Endpoints.Clients...)
	}

	spec := &HTTPEndpointSpec{
		Method:                  http.Method,
		Path:                    http.Path,
		SuccessCode:             http.SuccessStatus,
		NoContent:               http.NoContent,
		HandlerMethod:           handlerMethod,
		HasBody:                 hasBody,
		Clients:                 clients,
		AuthPublic:              http.Auth.Public,
		AuthPasswordResetExempt: http.Auth.PasswordResetExempt,
	}
	spec.PathParams = buildPathParams(http)
	spec.QueryParams = buildQueryParams(http)

	// Pagination detection: cursor (string) + limit (integer) with no path params
	// and GET method signals a canonical paginated list endpoint.
	if http.Method == "GET" && len(spec.PathParams) == 0 && len(spec.QueryParams) == 2 {
		if err := detectPagination(spec); err != nil {
			return nil, fmt.Errorf("contractgen build: %q pagination: %w", contract.ID, err)
		}
	}
	return spec, nil
}

// detectPagination checks whether the query params are exactly cursor (string)
// and limit (integer), setting IsPagination=true if so. Fails fast if the params
// look like a mixed pagination+filter pattern.
func detectPagination(spec *HTTPEndpointSpec) error {
	hasCursor, hasLimit := false, false
	for _, q := range spec.QueryParams {
		switch q.Name {
		case "cursor":
			if q.GoType != "string" {
				return fmt.Errorf("cursor param must be string type")
			}
			hasCursor = true
		case "limit":
			if q.GoType != "int64" {
				return fmt.Errorf("limit param must be integer type")
			}
			hasLimit = true
		default:
			// Extra query param mixed with cursor/limit → not a pure pagination endpoint.
			return nil
		}
	}
	if hasCursor && hasLimit {
		spec.IsPagination = true
	}
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

// mergeParamsIntoRequestWithID injects path and query params as fields into the
// Request DTO. If no Request DTO exists, one is created. Field order:
// path params first, query params second, then body schema fields.
// Returns error when a path or query param name (as Go field name) conflicts
// with an existing body schema field (which would produce a duplicate struct field).
// contractID is optional context for error messages.
func mergeParamsIntoRequestWithID(dtos []DTOSpec, http *metadata.HTTPTransportMeta, contractID string) ([]DTOSpec, error) {
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)

	if len(pathParams) == 0 && len(queryParams) == 0 {
		return dtos, nil
	}

	// Find or create Request DTO.
	reqIdx := findOrCreateRequestDTO(&dtos)

	// Check for name conflicts between path/query param Go names and existing body fields.
	existing := make(map[string]bool, len(dtos[reqIdx].Fields))
	for _, f := range dtos[reqIdx].Fields {
		existing[f.Name] = true
	}

	// Build prefix fields from path and query params, checking for conflicts.
	prefixFields, err := buildParamFields(pathParams, queryParams, existing, contractID)
	if err != nil {
		return nil, err
	}

	dtos[reqIdx].Fields = append(prefixFields, dtos[reqIdx].Fields...)
	return dtos, nil
}

// findOrCreateRequestDTO locates the Request DTO in dtos, creating it if absent.
// Returns the index of the Request DTO in the (possibly modified) slice.
func findOrCreateRequestDTO(dtos *[]DTOSpec) int {
	for i, d := range *dtos {
		if d.Name == "Request" {
			return i
		}
	}
	*dtos = append([]DTOSpec{{Name: "Request", Doc: "Request holds the HTTP request parameters."}}, *dtos...)
	return 0
}

// buildParamFields converts ParamSpec slices to DTOFields, checking for name
// conflicts against existing body fields. Returns error on conflict.
func buildParamFields(pathParams, queryParams []ParamSpec, existing map[string]bool, contractID string) ([]DTOField, error) {
	var fields []DTOField
	for _, p := range pathParams {
		if existing[p.GoName] {
			return nil, fmt.Errorf("contractgen: contract %q field %q conflict between path param and request body schema",
				contractID, p.Name)
		}
		fields = append(fields, paramToField(p, "path"))
	}
	for _, q := range queryParams {
		if existing[q.GoName] {
			return nil, fmt.Errorf("contractgen: contract %q field %q conflict between query param and request body schema",
				contractID, q.Name)
		}
		fields = append(fields, paramToField(q, "query"))
	}
	return fields, nil
}

// paramToField converts a ParamSpec to a DTOField with the given source tag.
// Path and query fields carry Source="path"/"query" so the handler template
// does not re-validate them in the body validation block.
func paramToField(p ParamSpec, source string) DTOField {
	tag := p.Name + ",omitempty"
	if p.Required {
		tag = p.Name
	}
	return DTOField{
		Name:     p.GoName,
		JSONTag:  tag,
		GoType:   p.GoType,
		Required: p.Required,
		Doc:      p.Doc,
		Source:   source,
		// MinLength/MaxLength/Minimum/Maximum are intentionally left nil for
		// path/query fields — they are validated at query parse time in the
		// generated handler, not in the body validation block.
	}
}

// buildPathParams extracts path parameters from HTTPTransport in path-template order.
func buildPathParams(http *metadata.HTTPTransportMeta) []ParamSpec {
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
			Format:    schema.Format,
			MinLength: paramMinLength(schema),
			MaxLength: paramMaxLength(schema),
			Minimum:   paramMinimum(schema),
			Maximum:   paramMaximum(schema),
		})
	}
	return out
}

// buildQueryParams extracts query parameters from HTTPTransport in map key-sorted order.
// Note: contract.yaml queryParams comes from a YAML map (unordered); we sort alphabetically
// to guarantee deterministic output. This is intentional, not a bug.
func buildQueryParams(http *metadata.HTTPTransportMeta) []ParamSpec {
	if len(http.QueryParams) == 0 {
		return nil
	}
	// Collect and sort for stability.
	names := make([]string, 0, len(http.QueryParams))
	for name := range http.QueryParams {
		names = append(names, name)
	}
	sort.Strings(names)

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

		dto.Fields = append(dto.Fields, bodyFieldFromSchema(fieldName, jsonTag, goType, required, doc, prop))

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
// "http.internal.foo.v1" → "generated/contracts/http/internalapi/foo/v1".
//
// The segment "internal" is rewritten to "internalapi" so that generated
// packages under http/internal/... are importable from cells/ and examples/
// (Go's internal package rule would otherwise block cross-directory imports).
// Contract IDs (http.internal.X.v1) and URL prefixes (/internal/v1/...) are
// unchanged — only the generated filesystem path segment is renamed.
// ref: golang/go internal package rule (https://go.dev/ref/spec#Internal_packages)
func contractIDToPackagePath(id string) string {
	parts := strings.Split(id, ".")
	// Rewrite any "internal" segment to "internalapi" to avoid Go's
	// internal package restriction on generated package importers.
	segments := make([]string, len(parts))
	for i, p := range parts {
		if p == "internal" {
			segments[i] = "internalapi"
		} else {
			segments[i] = p
		}
	}
	return "generated/contracts/" + strings.Join(segments, "/")
}

// pkgNameFromContractID derives the Go package name from a contract id.
// The package name is the segment immediately before the version segment,
// lowercased and with "-" and "_" stripped. When the candidate collides with
// a Go keyword, builtin, or stdlib package name, the preceding domain segment
// is prepended to disambiguate.
// Examples:
//
//	"http.order.create.v1"        -> "create"
//	"event.order-created.v1"      -> "ordercreated"
//	"http.audit.list.v1"          -> "list"
//	"http.config.delete.v1"       -> "configdelete"  (delete is a builtin)
//	"http.user.range.v1"          -> "userrange"     (range is a keyword)
func pkgNameFromContractID(id string) string {
	parts := strings.Split(id, ".")
	// contract id format: <kind>.<domain-path>....<vN>
	// Take penultimate segment as primary candidate.
	if len(parts) < 3 {
		// Pathological — return raw package name (caller already validates format).
		return goPackageName(parts[len(parts)-1])
	}
	last := goPackageName(parts[len(parts)-2])
	if !goReservedNames[last] {
		return last
	}
	// Collision: join with previous domain segment.
	if len(parts) >= 4 {
		prev := goPackageName(parts[len(parts)-3])
		return prev + last // e.g. "config" + "delete" = "configdelete"
	}
	// Fallback when no previous segment is available.
	return last + "pkg"
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

// commonInitialisms is the set of well-known initialisms from golang.org/x/lint/golint
// that should be uppercased entirely rather than just capitalised first-letter.
// Examples: "id" → "ID", "url" → "URL", "api" → "API".
var commonInitialisms = map[string]bool{
	"API":   true,
	"ASCII": true,
	"CPU":   true,
	"CSS":   true,
	"DNS":   true,
	"EOF":   true,
	"GUID":  true,
	"HTML":  true,
	"HTTP":  true,
	"HTTPS": true,
	"ID":    true,
	"IP":    true,
	"JSON":  true,
	"LHS":   true,
	"QPS":   true,
	"RAM":   true,
	"RHS":   true,
	"RPC":   true,
	"SLA":   true,
	"SMTP":  true,
	"SQL":   true,
	"SSH":   true,
	"TCP":   true,
	"TLS":   true,
	"TTL":   true,
	"UDP":   true,
	"UI":    true,
	"UID":   true,
	"UUID":  true,
	"URI":   true,
	"URL":   true,
	"UTF8":  true,
	"VM":    true,
	"XML":   true,
	"XMPP":  true,
	"XSRF":  true,
	"XSS":   true,
}

// goPascalCase converts a kebab-case or camelCase or snake_case identifier
// to PascalCase. Handles delimiters "-" and "_".
// Applies commonInitialisms: "id" → "ID", "user_id" → "UserID", "api_key" → "APIKey".
// "order-created" → "OrderCreated", "user_id" → "UserID".
func goPascalCase(s string) string {
	if s == "" {
		return s
	}
	parts := splitOnDelimiters(s)
	var sb strings.Builder
	for _, p := range parts {
		upper := strings.ToUpper(p)
		if commonInitialisms[upper] {
			sb.WriteString(upper)
		} else {
			sb.WriteString(capitalizeFirst(p))
		}
	}
	return sb.String()
}

// goReservedNames lists Go keywords + builtin identifiers that must not
// appear as a package name (or would shadow stdlib at use sites).
var goReservedNames = map[string]bool{
	// keywords (Go spec)
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
	// predeclared / builtin identifiers (subset that occurs as contract action verbs)
	"append": true, "cap": true, "clear": true, "copy": true, "delete": true,
	"len": true, "make": true, "max": true, "min": true, "new": true,
	"panic": true, "print": true, "println": true, "recover": true,
	// stdlib package names that would collide as a generated package
	"context": true, "errors": true, "fmt": true, "http": true, "io": true,
	"json": true, "log": true, "net": true, "os": true, "path": true,
	"sort": true, "strconv": true, "strings": true, "sync": true, "time": true,
}

// goPackageName converts a path segment to a valid Go package name.
// Strips dashes and underscores, lowercases the result.
// "order-created" → "ordercreated"
// "create" → "create"
// Note: callers must check goReservedNames separately; this function does not
// sanitize keyword conflicts on its own.
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
func paramDoc(s metadata.ParamSchema) string {
	if s.Format != "" {
		return "format: " + s.Format
	}
	return ""
}

func paramMinLength(s metadata.ParamSchema) *int {
	return s.MinLength
}

func paramMaxLength(s metadata.ParamSchema) *int {
	return s.MaxLength
}

func paramMinimum(s metadata.ParamSchema) *int64 {
	if s.Minimum == nil {
		return nil
	}
	v := int64(*s.Minimum)
	return &v
}

func paramMaximum(s metadata.ParamSchema) *int64 {
	if s.Maximum == nil {
		return nil
	}
	v := int64(*s.Maximum)
	return &v
}

// bodyFieldFromSchema constructs a DTOField for a body (schema-derived) property,
// extracting schema constraints (minLength/maxLength/minimum/maximum) for
// runtime validation in the generated handler.
func bodyFieldFromSchema(name, jsonTag, goType string, required bool, doc string, prop *Schema) DTOField {
	f := DTOField{
		Name:     name,
		JSONTag:  jsonTag,
		GoType:   goType,
		Required: required,
		Doc:      doc,
		Source:   "body",
	}
	if prop.MinLength != nil {
		v := *prop.MinLength
		f.MinLength = &v
	}
	if prop.MaxLength != nil {
		v := *prop.MaxLength
		f.MaxLength = &v
	}
	if prop.Minimum != nil {
		v := *prop.Minimum
		f.Minimum = &v
	}
	if prop.Maximum != nil {
		v := *prop.Maximum
		f.Maximum = &v
	}
	return f
}
