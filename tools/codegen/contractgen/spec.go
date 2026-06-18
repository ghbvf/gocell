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
	// Kind is one of the closed set: "http", "event", "command", "projection",
	// "webhook", "grpc", "saga".
	Kind string
	// Transports is the non-empty, parser-derived set of sanctioned wire
	// transports for this contract (#1389). The generated ContractSpec.Transport
	// is the primary, Transports[0]; templates render it uniformly across kinds
	// (spec.tmpl for events, handler.tmpl for http) so "generated Transport ==
	// transports[0]" has no kind special-case. For a multi-transport contract
	// (len > 1, e.g. an event over [amqp, mqtt]) spec.tmpl additionally emits an
	// exported `Transports` var so out-of-band subscribers reference the contract
	// truth source instead of hand-writing a transport string.
	Transports []string
	// ConsistencyLevel is the contract's declared consistency level ("L0".."L4").
	// Consumed by types.tmpl for two kinds, each driving a compile-time level
	// guard `const _ = uint(cellvocab.<ConsistencyLevel> - cellvocab.<floor>)`
	// that fails to compile when the level is below the floor:
	//   - kind=projection → floor L3 (PROJECTION-CONSISTENCY-01, gh #960)
	//   - kind=command    → floor L1 (COMMAND-CONTRACT-CONSISTENCY-LEVEL-01, #1668)
	// buildContractSpec validates that the value parses (cellvocab.ParseLevel)
	// before it reaches the template (validateProjectionLevel / validateCommandLevel).
	ConsistencyLevel string
	// SourceFile is the repo-relative path of the contract.yaml that drove
	// generation, e.g. "examples/todoorder/contracts/http/order/create/v1/contract.yaml".
	SourceFile string
	// DTOs holds the flattened list of Go struct definitions (nested types
	// expanded to top-level entries). Template iterates this slice directly.
	DTOs []DTOSpec
	// Endpoint is non-nil when Kind == "http". Its type is the unexported
	// httpEndpointSpec (sealed): no out-of-package code can construct a non-nil
	// Endpoint. The whole ContractGenSpec is also never handed to another
	// package as a mutable value — its sole constructor buildContractSpec and
	// every render wrapper are package-private (only Generate /
	// RenderContractArtifacts are exported, and those return rendered []byte,
	// never the spec). So neither constructing nor mutating an
	// FMT-34-unvalidated Endpoint to drive handler.tmpl is expressible from
	// another package. See httpEndpointSpec's godoc.
	Endpoint *httpEndpointSpec
	// Event is non-nil when Kind == "event".
	Event *EventEndpointSpec
	// Saga is non-nil when Kind == "saga". It drives saga.tmpl (the typed
	// Impl interface + BuildDefinition/Register); the step output DTOs it
	// references are emitted into DTOs (rendered by types.tmpl) like any kind.
	Saga *SagaSpec
	// Command is non-nil when Kind == "command". It drives command.tmpl (the
	// typed Handler interface + Register + Dispatch). Request/Response DTOs
	// are generated into DTOs (rendered by types.tmpl) like any kind.
	// iface_gen.go is NOT emitted for command (Handler lives in command_gen.go).
	Command *CommandSpec
	// (No grpc field: kind=grpc emits zero contractgen artifacts since #1688 —
	// buf's generated pb.<Svc>Server is the sole server contract.)
	// RequestSchemaJSON is the raw JSON content of the request schema file,
	// $ref-bundled and compacted to a single line (no extra whitespace).
	// Set by embedRequestSchema for two kinds:
	//   - Kind=="http" with a body (POST/PUT/PATCH declaring schemaRefs.request)
	//     — handler.tmpl embeds it to validate the request body at HTTP ingress.
	//   - Kind=="command" — ALWAYS set (D6 mandates schemaRefs.request; a command
	//     always carries a payload), so command.tmpl unconditionally embeds it to
	//     validate the untrusted outbox entry payload inside DispatchAsync (#1588).
	// The generated code embeds this as a Go string literal to compile the
	// validator at construction time — no runtime file I/O, no embed.FS.
	// Empty string means no schema validation is emitted (no-body HTTP endpoints).
	RequestSchemaJSON string

	// PanicReasonPolicyNil is the kebab-case reason literal passed to
	// panicregister.Approved for the "policy must not be nil" panic site.
	// Pre-computed from ContractID (dots replaced by dashes) + "-policy-nil".
	// Example: "http-order-create-v1-policy-nil".
	PanicReasonPolicyNil string
	// PanicReasonBootstrapAuthNil is the kebab-case reason literal passed to
	// panicregister.Approved for the "bootstrapAuth must not be nil" panic site.
	// Pre-computed from ContractID + "-bootstrap-auth-nil".
	// Example: "http-sample-bootstrap-v1-bootstrap-auth-nil".
	PanicReasonBootstrapAuthNil string
	// PanicReasonResolverNil is the kebab-case reason literal passed to
	// panicregister.Approved for the "resolver must not be nil" panic site in the
	// contract-derived NewHandler (endpoints.http.permission set, #2205).
	// Pre-computed from ContractID + "-resolver-nil".
	// Example: "http-config-get-v1-resolver-nil".
	PanicReasonResolverNil string
	// PanicReasonPublicSchemaCompileFailed is the kebab-case reason literal passed
	// to panicregister.Approved for the schema compile failed panic in NewPublicHandler.
	// Pre-computed from ContractID + "-public-schema-compile-failed".
	// Example: "http-order-create-v1-public-schema-compile-failed".
	PanicReasonPublicSchemaCompileFailed string
	// PanicReasonBootstrapSchemaCompileFailed is the kebab-case reason literal passed
	// to panicregister.Approved for the schema compile failed panic in NewBootstrapHandler.
	// Pre-computed from ContractID + "-bootstrap-schema-compile-failed".
	// Example: "http-auth-setup-admin-v1-bootstrap-schema-compile-failed".
	PanicReasonBootstrapSchemaCompileFailed string
	// PanicReasonClientsOnlySchemaCompileFailed is the kebab-case reason literal passed
	// to panicregister.Approved for the schema compile failed panic in NewClientsOnlyHandler.
	// Pre-computed from ContractID + "-clients-only-schema-compile-failed".
	// Example: "http-config-flags-evaluate-v1-clients-only-schema-compile-failed".
	PanicReasonClientsOnlySchemaCompileFailed string
	// PanicReasonServiceOwnedSchemaCompileFailed is the kebab-case reason literal passed
	// to panicregister.Approved for the schema compile failed panic in NewServiceOwnedHandler.
	// Pre-computed from ContractID + "-service-owned-schema-compile-failed".
	// Example: "http-auth-session-delete-v1-service-owned-schema-compile-failed".
	PanicReasonServiceOwnedSchemaCompileFailed string
	// PanicReasonStandardSchemaCompileFailed is the kebab-case reason literal passed
	// to panicregister.Approved for the schema compile failed panic in the standard
	// NewHandler (with policy parameter).
	// Pre-computed from ContractID + "-standard-schema-compile-failed".
	// Example: "http-order-create-v1-standard-schema-compile-failed".
	PanicReasonStandardSchemaCompileFailed string
	// PanicReasonClientTransportNil is the kebab-case reason literal for the
	// generated client's nil-transport fail-fast (client.tmpl NewClient, #2093).
	// Pre-computed from ContractID + "-client-transport-nil".
	PanicReasonClientTransportNil string
	// PanicReasonClientRingNil is the kebab-case reason literal for the generated
	// client's nil-keyring fail-fast (client.tmpl NewClient, #2093).
	// Pre-computed from ContractID + "-client-ring-nil".
	PanicReasonClientRingNil string
	// PanicReasonClientCallerCellEmpty is the kebab-case reason literal for the
	// generated client's empty-callerCell fail-fast (client.tmpl NewClient, #2093).
	// Pre-computed from ContractID + "-client-caller-cell-empty".
	PanicReasonClientCallerCellEmpty string
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
	// traversal. Callers of buildContractSpec see an empty slice — the builder
	// promotes nested types to ContractGenSpec.DTOs and clears this field.
	Nested []DTOSpec
	// EmitToMap requests a generated exported `ToMap() map[string]any` method
	// for this DTO (the resource-item type of a responseProjection endpoint).
	// Set by applyResponseProjection on the `data` resource item DTO so the
	// handler can convert the schema-typed row into the column map the masking
	// funnel (projection.NewProjection / NewProjectionList) consumes; map keys
	// mirror BareJSONTag so the projected field set equals this DTO's field set.
	// The DTO name follows the convention: ResponseDataItem for data[] array
	// endpoints (list reads), ResponseData for single-object data endpoints.
	EmitToMap bool
	// Enums holds the typed string-enum declarations for this DTO's fields that
	// carry a JSON-schema `enum` (#1935). Each EnumSpec renders to a named type +
	// const block (`type <Parent><Field> string` + `const (...)`) right after the
	// struct; the owning field's GoType references the named type. Empty for DTOs
	// with no enum fields.
	Enums []EnumSpec
}

// EnumSpec is one generated string-enum: a named Go type over string plus its
// const value-set, derived from a schema `enum` on a string field (#1935). The
// schema is the single source — removing a value drops the const, breaking any
// producer/consumer that referenced it (compile-time Hard funnel).
type EnumSpec struct {
	// TypeName is the named Go type, <Parent>+goPascalCase(field) (e.g. PayloadOutcome).
	TypeName string
	// FieldName is the source wire field key, used only for the type's doc comment.
	FieldName string
	// Values are the const declarations in schema source order (stable codegen).
	Values []EnumValue
}

// EnumValue is one const in an EnumSpec: ConstName = <TypeName>+goPascalCase(Value).
type EnumValue struct {
	ConstName string
	Value     string
}

// DTOField describes a single struct field.
type DTOField struct {
	// Name is the PascalCase Go field name.
	Name string
	// JSONTag is the JSON tag value, e.g. "item,omitempty". A Nullable field
	// (see below) carries NO ",omitempty" suffix — it is always present on the wire
	// (its nil pointer serializes to JSON null), e.g. "occurredAt".
	JSONTag string
	// BareJSONTag is the JSON key without the ",omitempty" suffix, e.g. "item".
	// Used by the generated ToMap() (EmitToMap DTOs) so the projection column map
	// keys match the wire JSON field names exactly. The generated ToMap emits EVERY
	// field unconditionally as `"<BareJSONTag>": i.<Name>` — the full, stable column
	// set the masking funnel relies on (PROJECTION-TOMAP-FULL-COLUMN-SET-01); there
	// is no conditional/omitempty omission (that was the #2159 regression, #1875).
	BareJSONTag string
	// Nullable is true when the source schema declared this column as
	// `type: ["<scalar>", "null"]` (JSON-Schema 2020-12 nullable form, #1875). The
	// builder renders such a field as a pointer GoType (e.g. *string) and DROPS the
	// ",omitempty" tag suffix, so its "no value" serializes as schema-valid JSON
	// `null` (not "" — which would violate a `format` constraint) while the column
	// stays present in the masked view. This mirrors the existing optional-bool→*bool
	// convention (distinguish absent from a real zero) for format-constrained columns.
	Nullable bool
	// GoType is the Go type expression, e.g. "string", "int64", "*ResponseData".
	GoType string
	// ItemDTO is the generated resource item DTO name when this field is an
	// object or array-of-object whose item is a generated struct (e.g. the
	// Response `data` field), else "". It is the STRUCTURED projection-item
	// signal captured at schema-traversal time so applyResponseProjection does
	// not re-parse the rendered GoType string (epic #1337 PR-12, F5). A non-empty
	// ItemDTO ⇒ the field is projectable through the column-masking funnel.
	ItemDTO string
	// IsList reports whether this field is an array (schema type=array). Paired
	// with ItemDTO it distinguishes []projection.ResourceProjection (list) from
	// projection.ResourceProjection (single) without inspecting GoType.
	IsList bool
	// Required indicates whether the field is in the schema's required list.
	Required bool
	// Doc is an optional comment, used for format hints (uuid, date-time).
	Doc string
	// Source identifies where this field originates: "body", "path", "query", or
	// "header". Empty means body (legacy/default). Only body fields receive schema
	// validation in the generated handler; path/query fields are validated at parse
	// time. "header" fields are populate-only (read from r.Header.Get, no gate) and
	// carry JSONTag "-" so the request body can never spoof them.
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

// httpEndpointSpec holds HTTP-specific endpoint information.
//
// The type is unexported on purpose (sealed): its sole constructor is
// buildHTTPEndpointSpec, which runs validateAuthOnInternalPath (the FMT-34
// upstream guard) unconditionally. Because ContractGenSpec.Endpoint is
// *httpEndpointSpec, no out-of-package code can build a non-nil Endpoint.
//
// Fields stay exported because text/template reads them via reflection, so a
// holder of a *httpEndpointSpec could otherwise mutate Path / AuthPublic /
// Clients after construction (after FMT-34 already ran) and drive handler.tmpl
// with the mutated, unvalidated spec. That mutation path is closed not by the
// field visibility but by the holder being unreachable cross-package: the spec
// is produced only by the package-private buildContractSpec and consumed only
// by the package-private render wrappers; the exported surface (Generate /
// RenderContractArtifacts) returns rendered []byte and never the spec. So
// neither construction nor post-construction mutation of an
// FMT-34-unvalidated Endpoint is expressible from another package.
//
// The intra-package "sole constructor / sole caller", the cross-generator
// emit-uniqueness, and the "no exported API leaks the mutable spec" invariants
// are locked by archtest CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01
// (A1b / A2 / A3 / A4 respectively).
type httpEndpointSpec struct {
	// Method is the HTTP method in upper-case, e.g. "POST".
	Method string
	// Path is the full URL path including chi-style placeholders, e.g. "/api/v1/orders/{id}".
	Path string
	// PathParams lists parameters embedded in the URL path, in declaration order.
	PathParams []ParamSpec
	// QueryParams lists URL query parameters, in declaration order.
	QueryParams []ParamSpec
	// HeaderParams lists inbound HTTP request headers, in canonical-name-sorted
	// order. The generated handler populates each into the Request DTO via
	// r.Header.Get with NO gate (populate-only — endpoint-specific fail behavior
	// is owned by the cell adapter; see HTTPTransportMeta.Headers godoc).
	HeaderParams []ParamSpec
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
	// guard when Clients is non-empty. Requires metadata.IsInternalHTTPPath(Path)
	// and len(Clients) > 0. Mutually exclusive with AuthPublic, AuthBootstrap,
	// and AuthPasswordResetExempt.
	AuthClientsOnly bool
	// AuthServiceOwned is true when contract.yaml endpoints.http.auth.serviceOwned is set.
	// The generated NewHandler takes a single svc Service argument (no policy arg) and
	// emits auth.Route without a Policy field. Listener JWT authentication still applies;
	// ownership authorization is enforced inside the service. May combine with
	// AuthPasswordResetExempt. Mutually exclusive with AuthPublic, AuthBootstrap,
	// and AuthClientsOnly.
	AuthServiceOwned bool
	// Permission is the ABAC action from contract.yaml endpoints.http.permission
	// (#2205) — non-empty only for contract-derived gated routes. When set, the
	// generated NewHandler takes a resolver authz.MethodPolicyResolver argument
	// (instead of policy auth.Policy) and constructs the gate via
	// auth.RequirePermissionForContract(contractSpec.ID, resolver); when empty, the legacy
	// hand-wired policy-arg path is generated unchanged. Mutually exclusive with the
	// no-gate auth modes (AuthPublic/AuthBootstrap/AuthClientsOnly/AuthServiceOwned);
	// may accompany a standard route or AuthPasswordResetExempt.
	Permission string
	// IdempotencyExempt is true when contract.yaml endpoints.http.idempotency.exempt
	// is set. A first-class endpoint concern (sibling of auth, #1469 review F7), not
	// an auth flag; emits auth.Route{IdempotencyExempt: true}. The idempotency
	// middleware skips claim/record/replay for this route (credential-rotation /
	// change-password whose body returns tokens).
	IdempotencyExempt bool
	// ResponseProjection is true when contract.yaml endpoints.http.responseProjection
	// is set. The builder post-pass applyResponseProjection then rewrites the
	// Response `data` field type to projection.ResourceProjection (single) or
	// []projection.ResourceProjection (array) and flags the resource item DTO
	// EmitToMap, so the handler must route wire data through the masking funnel
	// (epic #1337 PR-12, RESOURCE-PROJECTION-CALLSITE-LOCK-01).
	ResponseProjection bool
	// ClientDecodeDTO names the resource-item DTO the generated cross-cell client
	// (client_gen.go, #2093) decodes the success `data` envelope into, for
	// responseProjection contracts whose server-side Response.Data is the SEALED
	// projection.ResourceProjection carrier (no UnmarshalJSON — a client cannot
	// decode into Response). Empty for non-projection contracts: those decode
	// straight into the generated Response (directly unmarshalable). Set by the
	// deriveClientDecode post-pass from the Response `data` field's ItemDTO, after
	// applyResponseProjection. Only consumed by client.tmpl (gated by
	// shouldEmitClient); zero impact on handler/iface/types rendering.
	ClientDecodeDTO string
	// ClientDecodeList is true when the responseProjection `data` field is an array
	// ([]projection.ResourceProjection), so the generated client decodes
	// {data: []ClientDecodeDTO} and returns a slice. Paired with ClientDecodeDTO;
	// both are empty/false for non-projection contracts.
	ClientDecodeList bool
}

// IsPagination reports whether this endpoint uses the canonical cursor+limit
// pagination pattern. It is implemented as a method (not a field) so handler
// templates can keep their existing `{{- if .Endpoint.IsPagination}}` form
// while builder-side state moves to the structured *PaginationShape value.
func (e *httpEndpointSpec) IsPagination() bool {
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

// SagaSpec holds saga-specific generation data (Kind=="saga" only). It drives
// saga.tmpl, which emits the typed Impl interface + BuildDefinition/Register
// that adapt impl methods to kernel/saga's untyped StepFunc/CompensateFunc.
type SagaSpec struct {
	// DefinitionID is the contract id, used as the saga.Definition.ID const,
	// e.g. "saga.orderfulfillment.v1".
	DefinitionID string
	// TimeoutExpr is the Go duration expression for the saga-wide Timeout, e.g.
	// "30 * time.Second". Empty means the field is omitted (zero => no ceiling).
	TimeoutExpr string
	// RetryPolicy is the saga-wide default retry policy; nil means omit.
	RetryPolicy *RetryPolicySpec
	// CompensationOrder is the validated compensation walk order; always
	// "reverse" today (informational — the runtime owns the reverse walk).
	CompensationOrder string
	// NeedsTime reports whether any duration expr is present, so saga.tmpl
	// imports the time package only when used.
	NeedsTime bool
	// Steps are the forward steps in execution order.
	Steps []SagaStepSpec
}

// SagaStepSpec is one generated saga step. InputGoType is the previous step's
// OutputGoType ("" when IsFirst — the first step takes no typed input because
// the runtime feeds nil prevState to step 0).
type SagaStepSpec struct {
	// Name is the raw SafeID step name literal, e.g. "reserveInventory".
	Name string
	// GoName is goPascalCase(Name), e.g. "ReserveInventory".
	GoName string
	// OutputGoType is the typed output struct name, goPascalCase(Name)+"Output".
	OutputGoType string
	// InputGoType is the previous step's OutputGoType; empty when IsFirst.
	InputGoType string
	// IsFirst marks step 0 (its Run takes no typed input).
	IsFirst bool
	// HasCompensate is true when the step declares compensation (default true);
	// only then is a Compensate method emitted on Impl + a non-nil
	// saga.Step.Compensate wired.
	HasCompensate bool
	// TimeoutExpr is the Go duration expression for the per-step Timeout; empty
	// means omit (inherit Definition.Timeout).
	TimeoutExpr string
	// RetryPolicy overrides the saga-wide policy for this step; nil means omit.
	RetryPolicy *RetryPolicySpec
}

// RetryPolicySpec is the generation form of kernel/saga.RetryPolicy. Empty
// interval exprs / zero MaxAttempts are omitted from the emitted literal so the
// zero value (inherit) is preserved.
type RetryPolicySpec struct {
	MaxAttempts      int
	BaseIntervalExpr string
	MaxIntervalExpr  string
}

// CommandSpec holds command-specific generation data (Kind=="command" only). It
// drives command.tmpl, which emits the typed Handler interface + Register +
// Dispatch that provide a sealed typed funnel over runtime/command.Registry.
//
// The Handler interface + Register + Dispatch exist ONLY in generated code —
// a hand-written typed command handler is unexpressible, mirroring saga's
// Impl/BuildDefinition pattern. See ADR docs/architecture/ for the command-bus
// ADR (#1044).
type CommandSpec struct {
	// DispatchID is the contract id used as the command.Registry key const,
	// e.g. "command.devicecommand.enqueue.v1".
	DispatchID string
	// HandlerMethod is the method name on the generated Handler interface:
	// "Handle" + goPascalCase(domainLastSegment(id)).
	// Example: for "command.synth.do.v1" → "HandleDo".
	HandlerMethod string
	// RequestGoType is the Go type name for the command request DTO.
	// Always "Request" (the generated DTO from types_gen.go); held as a field
	// so command.tmpl has a single source rather than a hardcoded literal.
	RequestGoType string
	// ResponseGoType is the Go type name for the command response DTO.
	// Always "Response".
	ResponseGoType string
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
