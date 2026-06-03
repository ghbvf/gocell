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
	// Consumed by types.tmpl only for kind=projection, where it drives the
	// PROJECTION-CONSISTENCY-01 compile-time guard (gh #960):
	// `const _ = uint(cellvocab.<ConsistencyLevel> - cellvocab.L3)`, which fails
	// to compile when the level is below L3. buildContractSpec validates that the
	// value parses (cellvocab.ParseLevel) before it reaches the template.
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
	// GRPC is non-nil when Kind == "grpc". It drives the server interface emitted
	// into iface_gen.go; the proto-generated message types + import path are
	// resolved from the .proto go_package option + rpc declaration
	// (readProtoTypeInfo).
	GRPC *GRPCEndpointSpec
	// RequestSchemaJSON is the raw JSON content of the request schema file,
	// compacted to a single line (no extra whitespace).
	// Non-empty only when Kind=="http" and the contract declares schemaRefs.request.
	// The generated handler embeds this as a Go string literal to compile the
	// validator at construction time — no runtime file I/O, no embed.FS.
	// Empty string means no schema validation is emitted.
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

// GRPCEndpointSpec holds gRPC-specific endpoint information for the
// server-interface generator. It mirrors EventEndpointSpec's role: the builder
// projects metadata.GRPCTransportMeta into this codegen-local value type, and
// iface.tmpl reads it to emit the server interface.
//
// This is the contractgen IR type — distinct from kernel/contractspec's
// GRPCEndpointSpec, which is the runtime registration descriptor. They share no
// definition because the codegen IR carries Go-identifier-shaped fields
// (InterfaceName, MethodName) the runtime descriptor has no use for.
//
// The proto-typed fields (ProtoImportPath / ProtoAlias / RequestType /
// ResponseType) are resolved by buildGRPCSpec from the .proto file's go_package
// option + rpc declaration (readProtoTypeInfo) — the proto is the single source
// of the import + message-type identity emitted into the stub. buildGRPCSpec is
// the only constructor of this struct; GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01
// locks both that single write site (C1) and that the rendered import equals
// the proto oracle (C3).
type GRPCEndpointSpec struct {
	// InterfaceName is the Go identifier for the generated server interface,
	// e.g. "Server". Held as a field (not a literal in the template) so the
	// naming convention has a single source the test can assert.
	InterfaceName string
	// MethodName is the RPC method name from contract.yaml endpoints.grpc.method,
	// e.g. "IssueCommand". Used verbatim as the Go method name on the interface.
	MethodName string
	// ServiceFQN is the proto fully-qualified service name from
	// endpoints.grpc.service, e.g. "device.command.v1.DeviceCommandService".
	// Rendered into the interface doc comment only (no code dependency).
	ServiceFQN string
	// StreamingType is the declared streaming pattern (always unary/empty —
	// buildGRPCSpec rejects non-unary; PR 10 lifts the restriction).
	StreamingType string
	// ProtoPath is the contracts-relative .proto path from endpoints.grpc.proto.
	// Rendered into the interface doc comment; also resolved + read by buildGRPCSpec.
	ProtoPath string
	// ProtoImportPath is the Go import path of the proto-generated package, read
	// from the .proto go_package option. Single source for the import literal in
	// iface.tmpl (no hand-written string).
	ProtoImportPath string
	// ProtoAlias is the import alias from go_package (after ';'), e.g. "commandv1".
	ProtoAlias string
	// RequestType is the proto request message simple name, e.g. "IssueCommandRequest".
	RequestType string
	// ResponseType is the proto response message simple name.
	ResponseType string
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
