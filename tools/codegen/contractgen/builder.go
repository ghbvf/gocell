package contractgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/http/schemavalidate"
	"github.com/ghbvf/gocell/tools/codegen/internal/pathx"
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
	case "command", "projection":
		// These kinds are in the closed set (CONTRACT-KINDS-CLOSED-SET-01) but do
		// not yet have dedicated generators. BuildContractSpec accepts them so that
		// generateOneContract can emit types_gen.go + iface_gen.go (shared scaffolding)
		// without hard-failing. No spec/handler/subscription file is emitted.
		// When a full generator is added, add the corresponding case here.
	default:
		return nil, fmt.Errorf(
			"contractgen build: contract %q has unsupported kind %q (http|event|command|projection only)",
			contractID, contract.Kind)
	}

	return spec, nil
}

func buildHTTPSpec(spec *ContractGenSpec, rootDir string, contract *metadata.ContractMeta, contractDir string) error {
	http := contract.Endpoints.HTTP
	if http == nil {
		return fmt.Errorf("contractgen build: contract %q is kind=http but has no http endpoint", contract.ID)
	}

	// Pre-compute path and query params once; both buildHTTPDTOs and
	// buildHTTPEndpointSpec need them (F-09: avoid calling buildQueryParams twice).
	pathParams := buildPathParams(http)
	queryParams := buildQueryParams(http)

	allDTOs, err := buildHTTPDTOs(rootDir, contract, contractDir, pathParams, queryParams)
	if err != nil {
		return err
	}
	spec.DTOs = allDTOs

	endpointSpec, err := buildHTTPEndpointSpec(contract, http, pathParams, queryParams)
	if err != nil {
		return err
	}
	spec.Endpoint = endpointSpec

	// Embed the request schema JSON for runtime validation by schemavalidate.Validator.
	// Only populated when the endpoint actually has a body (POST/PUT/PATCH with a
	// declared request schema). GET/DELETE may declare schemaRefs.request as
	// metadata (e.g. "no body" placeholder), but the generated handler reads no
	// body and the validator wiring would be dead code (init-time compile cost +
	// binary bloat). HANDLER-NO-SCHEMA-FOR-NOBODY-01 archtest enforces that
	// no-body handlers contain no requestSchemaJSON literal.
	// ref: oapi-codegen — request validator emitted only for operations with
	// a requestBody.
	if contract.SchemaRefs.Request != "" && endpointSpec.HasBody {
		reqPath := filepath.Join(rootDir, contractDir, contract.SchemaRefs.Request)
		schemaBytes, err := os.ReadFile(reqPath) //nolint:gosec // schema path resolved from contract.yaml metadata
		if err != nil {
			return fmt.Errorf("contractgen build: %q read request schema for embed: %w", contract.ID, err)
		}
		// Compact to a single line: eliminates newlines so the schema can be
		// safely embedded in the generated file as a Go interpreted string literal.
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, schemaBytes); err != nil {
			return fmt.Errorf("contractgen build: %q compact request schema: %w", contract.ID, err)
		}
		// Validate schema compiles before embedding — fail-fast at codegen time
		// rather than at runtime. ref: oapi-codegen pkg/codegen/templates/strict.
		if _, vErr := schemavalidate.NewValidator(compacted.Bytes()); vErr != nil {
			return fmt.Errorf("contractgen build: %q request schema fails to compile: %w", contract.ID, vErr)
		}
		spec.RequestSchemaJSON = compacted.String()
	}
	return nil
}

// buildHTTPDTOs loads request/response schemas, converts them to DTOSpecs, and
// merges path/query params into the Request DTO. pathParams and queryParams are
// pre-computed by buildHTTPSpec so they are not recomputed here (F-09).
func buildHTTPDTOs(
	rootDir string,
	contract *metadata.ContractMeta,
	contractDir string,
	pathParams, queryParams []ParamSpec,
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

	// Merge path and query params into Request DTO using the pre-computed params.
	merged, mergeErr := mergeParamsIntoRequest(allDTOs, pathParams, queryParams, contract.ID)
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
// pathParams and queryParams are pre-computed by buildHTTPSpec (F-09: avoid re-computing).
func buildHTTPEndpointSpec(
	contract *metadata.ContractMeta,
	http *metadata.HTTPTransportMeta,
	pathParams, queryParams []ParamSpec,
) (*HTTPEndpointSpec, error) {
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
		AuthBootstrap:           http.Auth.Bootstrap,
	}
	spec.PathParams = pathParams
	spec.QueryParams = queryParams

	// Pagination detection (PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE F4 absorb):
	// Any GET endpoint that declares cursor (string) + limit (integer) in its
	// query params is paginated, regardless of the presence of path params or
	// additional filter query params. The extras land in
	// Pagination.ExtraQueryParams and the handler template parses them with
	// the standard per-param branch — cursor/limit are always handled by
	// pkg/httputil.ParsePageParams so the limit error envelope is uniform
	// across the entire HTTP surface (PR#376 F-COR-001 fix; the original
	// `len(PathParams) == 0` precondition was a leftover from the strict
	// 2-element invariant and would have left e.g. /roles/{userID}?cursor=&limit=
	// emitting the divergent inline-limit envelope).
	if http.Method == "GET" && len(spec.QueryParams) >= 2 {
		if err := detectPagination(spec); err != nil {
			return nil, fmt.Errorf("contractgen build: %q pagination: %w", contract.ID, err)
		}
	}

	var liftErr error
	spec.Responses, liftErr = liftHTTPResponses(http, handlerMethod, contract.ID)
	if liftErr != nil {
		return nil, liftErr
	}
	return spec, nil
}

// detectPagination scans QueryParams; if both cursor (string) and limit
// (integer) are present, the endpoint is paginated and Pagination is set.
// Any non-cursor/non-limit query params land in ExtraQueryParams so the
// handler template can route them through per-param parsing while
// cursor/limit always go through pkg/httputil.ParsePageParams.
func detectPagination(spec *HTTPEndpointSpec) error {
	hasCursor, hasLimit := false, false
	var extras []ParamSpec
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
			extras = append(extras, q)
		}
	}
	if hasCursor && hasLimit {
		spec.Pagination = &PaginationShape{
			HasCursor:        true,
			HasLimit:         true,
			ExtraQueryParams: extras,
		}
	}
	return nil
}

// liftHTTPResponses projects the contract.yaml http.responses[] map into a
// sorted []ResponseSpec, prepending the success status (derived from the
// HTTPTransportMeta SuccessStatus / NoContent fields). Each entry's
// GoTypeName follows the {HandlerMethod}{Status}{Suffix} convention:
//
//   - success body-bearing → "{Method}{Status}JSONResponse"
//   - success 204 NoContent → "{Method}204NoContentResponse"
//   - error (>=400)         → "{Method}{Status}ErrorResponse"
//
// The slice is consumed by Batch 2 templates to generate one Go type per
// declared status implementing the XxxResponseObject interface, and by CH-04
// governance to assert the generated typed-response set matches the
// contract.yaml declaration exactly.
//
// liftHTTPResponses validates that:
//   - at least one status (SuccessStatus or responses[]) is declared (C18)
//   - SuccessStatus, if set, is in range 1xx/2xx/3xx (C5)
//   - every responses[] key (other than SuccessStatus duplicate) is in 4xx/5xx (C5)
func liftHTTPResponses(http *metadata.HTTPTransportMeta, handlerMethod string, contractID string) ([]ResponseSpec, error) {
	statuses, err := collectAndValidateStatuses(http, contractID)
	if err != nil {
		return nil, err
	}
	sort.Ints(statuses)

	out := make([]ResponseSpec, 0, len(statuses))
	for _, status := range statuses {
		isNoContent := http.NoContent && status == http.SuccessStatus
		spec := ResponseSpec{
			Status:      status,
			IsError:     status >= 400,
			IsNoContent: isNoContent,
			GoTypeName:  responseGoTypeName(handlerMethod, status, isNoContent),
		}
		if r, ok := http.Responses[status]; ok {
			spec.Description = r.Description
			spec.SchemaRef = r.SchemaRef
		}
		out = append(out, spec)
	}
	return out, nil
}

// collectAndValidateStatuses returns the unsorted status set declared by the
// contract (SuccessStatus + responses[] keys), enforcing the C5 / C18 ranges
// described on liftHTTPResponses. Extracted out so the parent function stays
// under the gocognit complexity budget.
func collectAndValidateStatuses(http *metadata.HTTPTransportMeta, contractID string) ([]int, error) {
	if http.SuccessStatus == 0 && len(http.Responses) == 0 {
		return nil, fmt.Errorf(
			"contractgen: contract %q declares no SuccessStatus and no responses[]; HTTP endpoint must declare at least one response",
			contractID)
	}

	statuses := make([]int, 0, len(http.Responses)+1)

	if http.SuccessStatus > 0 {
		if http.SuccessStatus < 100 || http.SuccessStatus > 399 {
			return nil, fmt.Errorf(
				"contractgen: contract %q success status %d invalid: must be 1xx/2xx/3xx",
				contractID, http.SuccessStatus)
		}
		statuses = append(statuses, http.SuccessStatus)
	}

	hasError := false
	for s := range http.Responses {
		if s == http.SuccessStatus {
			continue
		}
		if s < 400 || s > 599 {
			return nil, fmt.Errorf(
				"contractgen: contract %q response status %d invalid: must be 4xx/5xx (success status %d declared via SuccessStatus)",
				contractID, s, http.SuccessStatus)
		}
		statuses = append(statuses, s)
		hasError = true
	}
	if !hasError && (http.SuccessStatus > 0 || len(http.Responses) > 0) {
		return nil, fmt.Errorf(
			"contractgen: contract %q HTTP endpoint must declare at least one 4xx/5xx response;"+
				" typed error envelope requires an explicit error response declaration",
			contractID)
	}
	return statuses, nil
}

// responseGoTypeName derives the typed response struct identifier from the
// endpoint's HandlerMethod, an HTTP status code, and whether the success
// status is 204 NoContent (controls the JSONResponse vs NoContentResponse
// suffix). The error suffix is unconditional for status >= 400.
func responseGoTypeName(handlerMethod string, status int, isNoContent bool) string {
	switch {
	case status >= 400:
		return fmt.Sprintf("%s%dErrorResponse", handlerMethod, status)
	case isNoContent:
		return fmt.Sprintf("%s%dNoContentResponse", handlerMethod, status)
	default:
		return fmt.Sprintf("%s%dJSONResponse", handlerMethod, status)
	}
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
	topic := contract.ID
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

// mergeParamsIntoRequest injects pre-computed path and query params as fields
// into the Request DTO. If no Request DTO exists, one is created. Field order:
// path params first, query params second, then body schema fields.
// Returns error when a param name (as Go field name) conflicts with an existing
// body schema field (which would produce a duplicate struct field).
// contractID is used in error messages.
func mergeParamsIntoRequest(dtos []DTOSpec, pathParams, queryParams []ParamSpec, contractID string) ([]DTOSpec, error) {
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
//
// Cognitive complexity comes from the schema-shape switch (object / array /
// inline / ref / scalar) crossed with the optional-pointer rules (*int64
// for minimum/maximum, *bool for optional booleans). Splitting would only
// push the same shape × pointer-policy matrix into helpers.
//
//nolint:gocognit // structural schema-shape × pointer-policy matrix; see godoc above.
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

		// Optional boolean fields must be *bool so callers can distinguish absent
		// (nil) from explicit false (&false = clear). This is critical for PATCH
		// where false and absent are otherwise indistinguishable at decode time.
		// ref: kubernetes/api core/v1 optional bool fields (*bool convention)
		// ref: oapi-codegen SkipOptionalPointer default false
		if !required && goType == "bool" {
			goType = "*bool"
		}

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
// Delegates to pathx.ContractIDToPackagePath — single source of truth shared
// with cellgen and archtest.
func contractIDToPackagePath(id string) string {
	return pathx.ContractIDToPackagePath(id)
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
