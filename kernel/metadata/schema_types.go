package metadata

// HTTPTransportMeta holds transport-level details for HTTP contracts.
// It elevates the wire-level contract (method, path, path/query parameters,
// status codes, bodyless semantics) to first-class metadata so static tooling
// (codegen, trace span labels, contract-health) can derive the full API shape
// from contract.yaml alone without inspecting JSON Schema files.
//
// ref: goadesign/goa v3 expr/http_endpoint.go - Params modeled as a typed
// attribute map; path params derived from the path template at parse time.
// ref: go-kratos/kratos cmd/protoc-gen-go-http - path params parsed via
// regex on the path template string.
type HTTPTransportMeta struct {
	Method        string                   `yaml:"method"        json:"method"`
	Path          string                   `yaml:"path"          json:"path"`
	PathParams    map[string]ParamSchema   `yaml:"pathParams,omitempty"  json:"pathParams,omitempty"`
	QueryParams   map[string]ParamSchema   `yaml:"queryParams,omitempty" json:"queryParams,omitempty"`
	SuccessStatus int                      `yaml:"successStatus" json:"successStatus"`
	NoContent     bool                     `yaml:"noContent"     json:"noContent"`
	Responses     map[int]HTTPResponseMeta `yaml:"responses,omitempty" json:"responses,omitempty"`
}

// ParamSchema describes a single HTTP path or query parameter.
//
// Type must be one of the well-known primitive names in ParamTypes. UUID path
// parameters use `type: "string"` with `format: "uuid"` so governance and
// runtime parsing rules share one convention.
//
// Required encodes three distinct states, chosen via pointer so YAML
// `required: false` can be distinguished from an omitted field:
//   - nil   - not declared; for path parameters this is the only legal value
//     (path placeholders are required by definition, see FMT-13); for query
//     parameters it defaults to optional.
//   - false - explicit opt-out, legal only on query parameters; FMT-13 rejects
//     `required: false` on path parameters.
//   - true  - explicit required declaration, legal on query parameters.
//
// Format is a free-form hint (e.g. "uuid", "date-time", "int64"). It does
// not influence FMT-13 enforcement today, but static tooling (codegen,
// OpenAPI export) consumes it. Governance rule FMT-25 exempts
// `format: "uuid"` from minLength/maxLength enforcement.
//
// MinLength / MaxLength / Minimum / Maximum are *int (not int) for the same
// three-state reason as Required: nil = "not declared", non-nil = "declared,
// even if zero". Governance rule FMT-25 distinguishes the two: missing
// declarations are violations; explicit zero is accepted. Minimum / Maximum
// govern both integer and number parameters; use integer-valued bounds in
// contract.yaml until ParamSchema grows decimal bound fields.
type ParamSchema struct {
	Type      string `yaml:"type"                json:"type"`
	Required  *bool  `yaml:"required,omitempty"  json:"required,omitempty"`
	Format    string `yaml:"format,omitempty"    json:"format,omitempty"`
	MinLength *int   `yaml:"minLength,omitempty" json:"minLength,omitempty"`
	MaxLength *int   `yaml:"maxLength,omitempty" json:"maxLength,omitempty"`
	Minimum   *int   `yaml:"minimum,omitempty"   json:"minimum,omitempty"`
	Maximum   *int   `yaml:"maximum,omitempty"   json:"maximum,omitempty"`
}

// ParamTypes lists the accepted `type` values for ParamSchema.
// Governance rule FMT-13 enforces membership.
var ParamTypes = map[string]bool{
	"string":  true,
	"integer": true,
	"number":  true,
	"boolean": true,
}

// HTTPResponseMeta describes a declared error response for a specific HTTP
// status code. It references a JSON Schema file, relative to the contract
// directory, that describes the error response body.
type HTTPResponseMeta struct {
	Description string `yaml:"description" json:"description" fingerprint:"-"`
	SchemaRef   string `yaml:"schemaRef"   json:"schemaRef"`
}

// SchemaRefsMeta holds JSON Schema file references relative to the contract
// directory. Known keys are request, response, payload, headers; additional
// string-valued keys are captured in Extra to stay compatible with
// contract.schema.json's additionalProperties: {"type":"string"}.
type SchemaRefsMeta struct {
	Request  string `yaml:"request,omitempty"  json:"request,omitempty"`
	Response string `yaml:"response,omitempty" json:"response,omitempty"`
	Payload  string `yaml:"payload,omitempty"  json:"payload,omitempty"`
	Headers  string `yaml:"headers,omitempty"  json:"headers,omitempty"`
	// Extra captures additional string-valued schema ref keys beyond the four
	// well-known ones, via yaml:",inline". It is excluded from JSON serialization
	// (json:"-") because Go's encoding/json does not support inline maps; callers
	// that need JSON output should implement custom MarshalJSON if needed.
	Extra map[string]string `yaml:",inline"            json:"-"`
}
