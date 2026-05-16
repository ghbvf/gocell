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
	// Auth declares route-level authentication overrides for contractgen.
	// When set, the generated handler emits the corresponding auth.Route flags
	// instead of the default Policy-only wiring. Omit for standard authenticated routes.
	Auth HTTPAuthMeta `yaml:"auth,omitempty" json:"auth,omitempty"`
	// Ownership declares object-level authorization subject/resource paths.
	// Required when auth.serviceOwned=true (governance FMT-32 enforces presence).
	Ownership *HTTPOwnershipMeta `yaml:"ownership,omitempty" json:"ownership,omitempty"`
}

// HTTPOwnershipMeta declares object-level authorization subject/resource paths.
// Required when auth.serviceOwned=true (governance FMT-32 + schema if/then enforces this).
// Pointer field tri-state: nil = block absent, non-nil with empty fields = declared but
// incomplete; both forms are rejected by FMT-32.
type HTTPOwnershipMeta struct {
	SubjectPath  string `yaml:"subjectPath"  json:"subjectPath"`
	ResourcePath string `yaml:"resourcePath" json:"resourcePath"`
}

// HTTPAuthMeta carries route-level authentication override flags for contractgen.
// These map to generated auth.Route wiring and handler constructor shape.
//
// Mutex among the 5 bool fields is enforced by metadata.AuthComboLegal (the
// single oracle shared by contract.schema.json if/then rules and governance
// validateFMT27). When adding a new bool field, see auth_combo.go for the
// checklist of files to update in lockstep.
//
// ref: kubernetes-sigs/controller-tools markers/registry.go (declarative auth metadata)
type HTTPAuthMeta struct {
	// Public marks the route as JWT-exempt. The generated NewHandler takes no
	// policy argument; auth.Route{Public: true} is emitted by RegisterRoutes.
	// Mutually exclusive with PasswordResetExempt, Bootstrap, ServiceOwned,
	// and ClientsOnly.
	Public bool `yaml:"public,omitempty" json:"public,omitempty"`
	// PasswordResetExempt allows callers whose JWT carries password_reset_required=true
	// to reach this route. The generated handler emits auth.Route{PasswordResetExempt: true}.
	// Mutually exclusive with Public, Bootstrap, and ClientsOnly. May combine
	// with ServiceOwned.
	PasswordResetExempt bool `yaml:"passwordResetExempt,omitempty" json:"passwordResetExempt,omitempty"`
	// ServiceOwned indicates that the listener must still authenticate the caller,
	// but route-level authorization is intentionally absent because the service
	// validates ownership against domain state. When true, contractgen generates a
	// single-arg NewHandler(svc Service) constructor and emits auth.Route without
	// a Policy field. May be combined with PasswordResetExempt. Mutually exclusive
	// with Public, Bootstrap, and ClientsOnly.
	ServiceOwned bool `yaml:"serviceOwned,omitempty" json:"serviceOwned,omitempty"`
	// Bootstrap marks the route as protected by HTTP Basic Auth using
	// GOCELL_BOOTSTRAP_ADMIN_USERNAME/PASSWORD env credentials. Listener-level
	// JWT middleware skips routes flagged as Bootstrap (matcher in FinalizeAuth).
	// Mutually exclusive with Public, PasswordResetExempt, ServiceOwned, and
	// ClientsOnly. FMT-28 limits this flag to contracts whose path matches
	// IsBootstrapPath.
	Bootstrap bool `yaml:"bootstrap,omitempty" json:"bootstrap,omitempty"`
	// ClientsOnly indicates that this endpoint relies solely on Contract.Clients
	// caller-cell allowlist for authorization. When true, contractgen generates a
	// single-arg NewHandler(svc Service) constructor and emits auth.Route without
	// a Policy field. auth.Mount auto-injects RequireCallerCell guard when
	// Clients is non-empty. Mutually exclusive with Public, Bootstrap,
	// PasswordResetExempt, and ServiceOwned. Requires endpoints.clients to be
	// non-empty and the path to match IsInternalHTTPPath (/internal/v1 or
	// /internal/v1/...) where caller-cell identity is verifiable via the
	// service token.
	ClientsOnly bool `yaml:"clientsOnly,omitempty" json:"clientsOnly,omitempty"`
	// Responses lists HTTP status codes injected by listener-mounted middleware
	// (e.g. bootstrap auth 401, rate limiter 429). CH-04 treats these as
	// declared without requiring handler AST emission.
	Responses []int `yaml:"responses,omitempty" json:"responses,omitempty"`
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
