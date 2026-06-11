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
	Method      string                 `yaml:"method"        json:"method"`
	Path        string                 `yaml:"path"          json:"path"`
	PathParams  map[string]ParamSchema `yaml:"pathParams,omitempty"  json:"pathParams,omitempty"`
	QueryParams map[string]ParamSchema `yaml:"queryParams,omitempty" json:"queryParams,omitempty"`
	// Headers declares inbound HTTP request headers this endpoint consumes
	// (keyed by canonical header name, e.g. "X-Tenant-ID"). It is the single
	// source for request-header consumption: contractgen merges each declared
	// header into the generated Request DTO as a typed field populated from
	// r.Header.Get — see archtest HTTP-REQUEST-HEADER-READ-FUNNEL-01 (cells /
	// examples may not read inbound headers raw; the generated handler is the
	// sole reader).
	//
	// Headers are POPULATE-ONLY at codegen: the generated handler emits no
	// required / length / format gate. Per-endpoint fail behavior (e.g. the
	// X-Tenant-ID anti-enumeration matrix in ADR 1160 — login→401, setup/admin→400,
	// setup/status→200 fail-soft) is owned by the cell adapter/service via
	// tenant.ParseTenantID, not derivable from a uniform codegen gate. `required`
	// is documentation / governance / client-gen metadata only. Governance FMT-40
	// rejects unenforced minLength/maxLength/minimum/maximum on header schemas so a
	// declared constraint can never silently no-op. ref: goadesign/goa v3
	// expr/http_endpoint.go Headers; go-kratos/kratos transport/http header binding.
	Headers       map[string]ParamSchema   `yaml:"headers,omitempty"     json:"headers,omitempty"`
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
	// Idempotency declares route-level HTTP-idempotency behavior. It is a
	// first-class sibling of Auth (not folded into the auth-mode mutex matrix):
	// idempotency is a middleware concern, not an authentication mode (#1469
	// review F7). Omit for default idempotent-eligible routes.
	Idempotency HTTPIdempotencyMeta `yaml:"idempotency,omitempty" json:"idempotency,omitempty"`
	// ResponseProjection declares that this endpoint's success response `data`
	// resource is served through the sealed pkg/projection.ResourceProjection
	// carrier (epic #1337 PR-12, FR-016). When set, contractgen rewrites the
	// generated Response.Data field type to projection.ResourceProjection (single
	// object) or []projection.ResourceProjection (array), forcing the handler to
	// build the wire data through the column-masking funnel
	// (projection.NewProjection / NewProjectionList). A full, un-masked view is not
	// assignable to the field, so column masking cannot be bypassed at the handler
	// callsite (RESOURCE-PROJECTION-CALLSITE-LOCK-01). Every GET read endpoint
	// carrying a `data` resource MUST set this (RESOURCE-PROJECTION-COVERAGE-01);
	// an empty FieldMask obligation yields the identity projection so the wire
	// field set is unchanged. Default false leaves the response type untouched.
	// See contracts/http/audit/list/v1/contract.yaml for a worked example.
	ResponseProjection bool `yaml:"responseProjection,omitempty" json:"responseProjection,omitempty"`
}

// HTTPIdempotencyMeta carries route-level HTTP-idempotency behavior, orthogonal
// to authentication. It is a first-class sibling of HTTPAuthMeta rather than a
// folded-in auth flag (#1469 review F7): idempotency is a middleware concern, so
// it does not participate in the FMT-27 auth-mode mutex matrix. Keeping it out of
// HTTPAuthMeta also keeps the auth-combo space at 2^5 instead of 2^6.
type HTTPIdempotencyMeta struct {
	// Exempt, when true, instructs the HTTP idempotency middleware to never
	// claim/record/replay this route's responses (credential-rotation /
	// change-password whose body returns tokens). The generated handler emits
	// auth.Route{IdempotencyExempt: true} (see runtime/auth/route.go).
	// Mandatory for any route whose response schema carries a
	// pkg/redaction.IsSensitiveKey field — codegen rejects generation without it
	// (CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01).
	Exempt bool `yaml:"exempt,omitempty" json:"exempt,omitempty"`
}

// IdempotencyFrameworkStatuses returns the HTTP status codes the idempotency
// middleware can emit for this route as a framework-injected set, analogous to
// HTTPAuthMeta.Responses (401/429). When idempotency is default-on, a mutating
// route (POST/PUT/PATCH/DELETE) that is not Idempotency.Exempt can return 409 on
// an in-flight key (ClaimBusy) or 422 on a reused key with a mismatched body
// fingerprint (ErrIdempotencyKeyReused, per IETF idempotency-key draft §2.7).
// Returns nil for GET/HEAD and exempt routes.
//
// This is the single-source oracle for the CH-07 governance rule (#1537 review
// F4): CH-07 requires every contract this returns a non-empty set for to declare
// those statuses in auth.responses, so the declaration surface cannot drift from
// the middleware and a future mutating route is forced to declare 409/422 or set
// idempotency.exempt. These are NOT folded into declaredErrorStatuses (that would
// make CH-07 vacuous and has no effect on CH-04, which checks handler-emitted
// statuses — the middleware injects 409/422, not the handler).
//
// The {409, 422} set is re-declared here as an integer literal because kernel/
// must not import runtime/ (layering); it is bound to the single runtime source
// runtime/http/idempotency.FrameworkStatuses() by archtest
// IDEMPOTENCY-FRAMEWORK-STATUS-ORACLE-ALIGN-01, which fails if the two diverge.
//
// Known reachability gap: this oracle keys only on method + Idempotency.Exempt, so
// it also requires 409/422 on mutating routes the middleware never claims for —
// public / bootstrap / service-token routes, whose non-PrincipalUser principals are
// bypassed by middleware.extractIdentity. Those routes therefore declare statuses
// they cannot emit (a pre-existing property of the 409 leg, not introduced by 422).
// Making the oracle auth-shape-aware is tracked at gh #1591.
func (h *HTTPTransportMeta) IdempotencyFrameworkStatuses() []int {
	if h == nil || h.Idempotency.Exempt {
		return nil
	}
	switch h.Method {
	case "POST", "PUT", "PATCH", "DELETE":
		return []int{409, 422}
	default:
		return nil
	}
}

// GRPCTransportMeta declares a gRPC contract's service-level ownership. It lives
// under EndpointsMeta.GRPC, parallel to EndpointsMeta.HTTP; the provider/consumer
// cells reuse endpoints.server / endpoints.clients (gRPC mirrors http: roles
// serve/call). A single contract owns a whole proto service — the .proto file is
// the single source of truth for the RPC method set. Per-method field
// declarations have been removed (#1655): the contractgen ProtoRegistry
// enumerates all RPCs from the .proto via ReadProtoServiceInfo, so contract.yaml
// never lists individual method names.
//
// No per-RPC auth overlay is declared here: a service-level public flag could
// not express per-method auth once a service owns multiple RPCs, and nothing
// consumed it (the runtime auth predicate is wired separately). The per-method
// auth model is deferred to #1675.
//
// ref: grpc/grpc-go ServiceDesc; go-kratos/kratos protoc-gen-go-grpc service
// descriptor — the proto service name is the wire identity.
type GRPCTransportMeta struct {
	// Service is the proto fully-qualified service name,
	// e.g. "device.command.v1.DeviceCommandService".
	Service string `yaml:"service" json:"service"`
	// Proto is the contracts-relative path to the .proto file, e.g.
	// "contracts/grpc/device/command/v1/device_command.proto".
	Proto string `yaml:"proto" json:"proto"`
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
// validateFMT27). HTTP-idempotency behavior is NOT an auth mode — it lives on the
// sibling HTTPIdempotencyMeta (endpoints.http.idempotency), so HTTPAuthMeta holds
// exactly the 5 auth-mode flags and the auth-combo matrix stays 2^5. When adding a
// new bool field, see auth_combo.go for the checklist of files to update in lockstep.
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
	// Responses lists HTTP status codes injected by listener-mounted middleware,
	// NOT emitted by the handler/adapter — so they are declared here (no typed
	// response struct) rather than in the responses map. Despite the "auth" name,
	// this list already spans non-auth middleware: bootstrap auth 401, rate limiter
	// 429, and HTTP-idempotency 409 (ClaimBusy) / 422 (key-reused) — both required
	// on non-exempt mutating routes by governance rule CH-07. CH-04 treats these as
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
	// Ref is a relative path to a shared JSON Schema mixin that single-sources
	// the value shape (type/minimum/maximum/minLength/maxLength) for this param.
	// It is mutually exclusive with inline type, minimum, maximum, minLength,
	// maxLength, and format declarations; resolved at parse time so governance
	// (FMT-25) and contractgen see a fully-resolved ParamSchema transparently.
	// The Ref field is preserved after resolution for traceability.
	Ref       string `yaml:"$ref,omitempty"      json:"$ref,omitempty"`
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
