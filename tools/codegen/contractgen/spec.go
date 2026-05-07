package contractgen

// ContractGenSpec is the top-level template input for one contract.
type ContractGenSpec struct {
	// PackageName is the Go package name derived from the last path segment
	// of PackagePath (e.g. "create", "get", "ordercreated").
	PackageName string
	// PackagePath is the module-relative path for the generated package,
	// e.g. "generated/contracts/http/order/create/v1".
	PackagePath string
	// ContractID is the full contract id, e.g. "http.order.create.v1".
	ContractID string
	// Kind is "http" or "event".
	Kind string
	// SourceFile is the repo-relative path of the contract.yaml that drove
	// generation, e.g. "examples/todoorder/contracts/http/order/create/v1/contract.yaml".
	SourceFile string
	// DTOs holds the flattened list of Go struct definitions (nested types
	// expanded to top-level entries). Template iterates this slice directly.
	DTOs []DTOSpec
	// Endpoint is non-nil when Kind == "http".
	Endpoint *HTTPEndpointSpec
	// Event is non-nil when Kind == "event".
	Event *EventEndpointSpec
	// RequestSchemaJSON is the raw JSON content of the request schema file,
	// compacted to a single line (no extra whitespace).
	// Non-empty only when Kind=="http" and the contract declares schemaRefs.request.
	// The generated handler embeds this as a Go string literal to compile the
	// validator at construction time — no runtime file I/O, no embed.FS.
	// Empty string means no schema validation is emitted.
	RequestSchemaJSON string
}

// DTOSpec is one Go struct definition.
// Nested is used only as an intermediate representation during builder
// flattening; by the time ContractGenSpec.DTOs is populated, all nested
// types have been promoted to top-level and Nested is empty.
type DTOSpec struct {
	// Name is the PascalCase struct name, e.g. Request, Response, Payload.
	Name string
	// Doc is the human-readable description (from schema.title).
	Doc string
	// Fields lists the struct fields in source-declared order.
	Fields []DTOField
	// Nested holds intermediate nested-object types discovered during schema
	// traversal. Callers of BuildContractSpec see an empty slice — the builder
	// promotes nested types to ContractGenSpec.DTOs and clears this field.
	Nested []DTOSpec
}

// DTOField describes a single struct field.
type DTOField struct {
	// Name is the PascalCase Go field name.
	Name string
	// JSONTag is the JSON tag value, e.g. "item,omitempty".
	JSONTag string
	// GoType is the Go type expression, e.g. "string", "int64", "*ResponseData".
	GoType string
	// Required indicates whether the field is in the schema's required list.
	Required bool
	// Doc is an optional comment, used for format hints (uuid, date-time).
	Doc string
	// Source identifies where this field originates: "body", "path", or "query".
	// Empty means body (legacy/default). Only body fields receive schema validation
	// in the generated handler; path/query fields are validated at parse time.
	Source string
	// MinLength constrains string body fields (minimum character length).
	MinLength *int
	// MaxLength constrains string body fields (maximum character length).
	MaxLength *int
	// Minimum constrains integer body fields (inclusive lower bound).
	Minimum *int64
	// Maximum constrains integer body fields (inclusive upper bound).
	Maximum *int64
}

// HTTPEndpointSpec holds HTTP-specific endpoint information.
type HTTPEndpointSpec struct {
	// Method is the HTTP method in upper-case, e.g. "POST".
	Method string
	// Path is the full URL path including chi-style placeholders, e.g. "/api/v1/orders/{id}".
	Path string
	// PathParams lists parameters embedded in the URL path, in declaration order.
	PathParams []ParamSpec
	// QueryParams lists URL query parameters, in declaration order.
	QueryParams []ParamSpec
	// SuccessCode is the HTTP status code on success, e.g. 201 or 200.
	SuccessCode int
	// NoContent indicates a 204 No Content response (no body written).
	NoContent bool
	// HandlerMethod is the PascalCase method name on the Service interface,
	// derived from the last domain segment, e.g. "Create", "Get", "List".
	HandlerMethod string
	// HasBody is true when Method is POST/PUT/PATCH and the contract declares a
	// schemaRefs.request. POST/PATCH endpoints that only use path params and have
	// no request body schema must not call DecodeJSONStrict (empty body → 400).
	HasBody bool
	// Pagination is non-nil when the endpoint exposes the canonical cursor+limit
	// pagination pattern (Batch 1 still requires query=cursor+limit exactly; the
	// follow-up batch relaxes detection to "contains cursor+limit, others free"
	// and routes the extras through ExtraQueryParams). When set, the generated
	// handler uses httputil.ParsePageParams for cursor/limit instead of inline
	// query param parsing.
	Pagination *PaginationShape
	// Responses lists every declared HTTP response (success + errors) sorted
	// by status ascending. It is the single declaration table that drives
	// generated typed response envelope structs (one Go type per status) and
	// CH-04 governance equality between contract.yaml http.responses[] and the
	// generated handler's typed response set.
	Responses []ResponseSpec
	// Clients lists the allowed caller-cell IDs from contract.yaml endpoints.clients.
	// When non-empty, auth.Mount enforces RequireCallerCell on this route. The
	// generated contractSpec must carry this list so the governance enforcement
	// matches the YAML declaration.
	Clients []string
	// AuthPublic is true when contract.yaml endpoints.http.auth.public is set.
	// The generated NewHandler takes no policy argument and emits
	// auth.Route{Public: true} in RegisterRoutes.
	AuthPublic bool
	// AuthPasswordResetExempt is true when contract.yaml endpoints.http.auth.passwordResetExempt is set.
	// The generated handler emits auth.Route{PasswordResetExempt: true} in RegisterRoutes.
	// Mutually exclusive with AuthPublic and AuthBootstrap.
	AuthPasswordResetExempt bool
	// AuthBootstrap is true when contract.yaml endpoints.http.auth.bootstrap is set.
	// The generated NewHandler takes bootstrapAuth as a non-nil first arg of type
	// func(http.Handler) http.Handler and emits auth.Route{BootstrapAuth: bootstrapAuth}
	// in RegisterRoutes; the listener-level JWT middleware skips this route. FMT-28
	// enforces that this flag only appears on setup/admin contracts. Mutually
	// exclusive with AuthPublic and AuthPasswordResetExempt.
	AuthBootstrap bool
	// AuthClientsOnly is true when contract.yaml endpoints.http.auth.clientsOnly is set.
	// The generated NewHandler takes a single svc Service argument (no policy arg) and
	// emits auth.Route without a Policy field. Authorization is provided solely by
	// Contract.Clients caller-cell allowlist — auth.Mount auto-injects RequireCallerCell
	// guard when Clients is non-empty. Requires isInternalPath && len(Clients) > 0.
	// Mutually exclusive with AuthPublic, AuthBootstrap, and AuthPasswordResetExempt.
	AuthClientsOnly bool
}

// IsPagination reports whether this endpoint uses the canonical cursor+limit
// pagination pattern. It is implemented as a method (not a field) so handler
// templates can keep their existing `{{- if .Endpoint.IsPagination}}` form
// while builder-side state moves to the structured *PaginationShape value.
func (e *HTTPEndpointSpec) IsPagination() bool {
	return e != nil && e.Pagination != nil
}

// PaginationShape captures the structural facts the handler template needs
// to emit the right query-parsing code for a paginated endpoint:
//
//   - HasCursor / HasLimit indicate which canonical params are present
//     (always both true after Batch 1; Batch 2 keeps the same invariant
//     while relaxing endpoint-shape detection).
//   - ExtraQueryParams carries any additional query parameters declared on
//     the same endpoint. Today the builder rejects mixed pagination+filter
//     endpoints in detectPagination; Batch 2 lifts that restriction and
//     these params are routed through per-param parsing while cursor+limit
//     keep going through pkg/httputil.ParsePageParams (single error envelope).
type PaginationShape struct {
	HasCursor        bool
	HasLimit         bool
	ExtraQueryParams []ParamSpec
}

// ResponseSpec describes a single declared HTTP response from contract.yaml.
// One ResponseSpec is emitted per declared status (success status from the
// HTTP transport metadata + every entry in http.responses[]), sorted by
// Status ascending. The slice is the single source of truth for typed
// response envelope generation and CH-04 governance.
type ResponseSpec struct {
	// Status is the HTTP status code, e.g. 200, 204, 401, 503.
	Status int
	// Description mirrors the contract.yaml response description (free form).
	// Empty for the success entry (the success body schema documents itself).
	Description string
	// SchemaRef is the contract-relative path to the JSON schema that
	// describes this response body. Empty for the success entry (success
	// body lives in schemaRefs.response, not in responses[]) and for the
	// 204 NoContent entry.
	SchemaRef string
	// IsError is true for status >= 400; the success entry is false.
	IsError bool
	// IsNoContent is true for the 204 NoContent success entry — the
	// generated typed struct is an empty marker (`struct{}`) and the visit
	// method writes only the status header. Templates branch on this flag
	// rather than reverse-derive the suffix from GoTypeName, keeping the
	// IR the single source for the JSON-vs-NoContent distinction.
	IsNoContent bool
	// GoTypeName is the Go identifier for the typed response struct that
	// implements the per-endpoint XxxResponseObject interface. The naming
	// convention is {HandlerMethod}{Status}{Suffix}: 200 → 200JSONResponse,
	// 204 → 204NoContentResponse, 4xx/5xx → {Status}ErrorResponse.
	GoTypeName string
}

// EventEndpointSpec holds event-specific endpoint information.
type EventEndpointSpec struct {
	// Topic is the broker topic name (contract id with version suffix stripped),
	// e.g. "event.order-created".
	Topic string
	// HandlerMethod is the PascalCase handler method name,
	// e.g. "HandleOrderCreated".
	HandlerMethod string
	// Replayable indicates whether this event supports replay.
	Replayable bool
	// DeliverySemantics is the declared delivery guarantee, e.g. "at-least-once".
	DeliverySemantics string
}

// ParamSpec describes a single HTTP path or query parameter.
type ParamSpec struct {
	// Name is the parameter name as declared in contract.yaml, e.g. "id", "cursor".
	Name string
	// GoName is the PascalCase Go field name used in the Request struct.
	GoName string
	// GoType is the Go scalar type: "string", "int64", "float64", or "bool".
	GoType string
	// Required is true for path params (always) or explicitly-required query params.
	Required bool
	// Doc is an optional hint comment.
	Doc string
	// Format is the json-schema format for this param, e.g. "uuid", "date-time".
	// Used by handler.tmpl to emit httputil.ParseUUIDPathParam for uuid-format path params.
	Format string
	// MinLength applies to string params.
	MinLength *int
	// MaxLength applies to string params.
	MaxLength *int
	// Minimum applies to numeric params.
	Minimum *int64
	// Maximum applies to numeric params.
	Maximum *int64
}
