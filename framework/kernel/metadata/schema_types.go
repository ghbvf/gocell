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
	// Permission is the ABAC action (e.g. "config:read") this route's PDP gate
	// requires (#2205) — the HTTP sibling of GRPCMethodMeta.Permission. It is the
	// contract-derived origin of the route's auth.RequirePermission gate: cellgen
	// derives a cell-level contractID→permission map from this overlay and the
	// generated handler resolves it through authz.MethodPolicyResolver
	// (runtime/auth.RequirePermissionForContract), replacing the slice hand-wiring of
	// auth.RequirePermission(authz.PermX()). It is a first-class sibling of Auth, NOT
	// folded into HTTPAuthMeta's 5-bool mutex matrix: Permission is a string, and
	// keeping it out preserves the 2^5 auth-combo space (same rationale as
	// Idempotency, #1469 review F7).
	//
	// Empty for routes with no RequirePermission gate: it is MUTUALLY EXCLUSIVE with
	// auth.public / auth.bootstrap / auth.clientsOnly / auth.serviceOwned (those modes
	// replace or delegate the route gate). It MAY accompany a standard route (no auth
	// flag) or a passwordResetExempt route (which still carries a Policy). When present
	// it MUST be a member of the closed authz registry — governance (the FMT
	// HTTP-permission rule, the sibling of the gRPC FMT-41 overlay check) validates
	// membership via authz.IsKnownPermissionString at `gocell validate`, so a typo
	// fails there rather than at the runtime gate.
	//
	// During the #2205 migration the overlay is OPTIONAL (sparse, exactly like
	// endpoints.grpc.methods[].permission): a standard route without it keeps the
	// legacy hand-wired gate; only contracts that opt in regenerate with the
	// resolver-based gate. PR-13 makes it mandatory for standard routes.
	Permission string `yaml:"permission,omitempty" json:"permission,omitempty"`
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

// FrameworkIdempotencyStatuses is the single literal source of the HTTP status
// codes the idempotency middleware injects: 409 (ClaimBusy, in-flight key) and 422
// (key reused with a different body, per IETF idempotency-key draft §2.7). The
// auth-shape-aware oracle IdempotencyFrameworkStatuses() and the CH-07 governance
// guard both reference this one function, so the set is declared exactly once in
// kernel/ (no second hardcode to drift). It is re-declared here as a literal — not
// imported — because kernel/ must not import runtime/ (layering); archtest
// IDEMPOTENCY-FRAMEWORK-STATUS-ORACLE-ALIGN-01 binds it to the runtime source
// runtime/http/idempotency.FrameworkStatuses() and fails if the two diverge.
func FrameworkIdempotencyStatuses() []int {
	return []int{409, 422}
}

// IdempotencyFrameworkStatuses returns the framework-injected idempotency status
// codes the middleware can emit for THIS route — the SOLE computed source of those
// statuses, which are never hand-authored per contract (compute-only, #1591).
//
// The middleware (runtime/http/idempotency.extractIdentity) only claims for a
// PrincipalUser with a non-empty Subject; non-PrincipalUser principals are bypassed
// (no claim, so no 409/422). A route's auth shape determines its principal kind:
//
//   - Auth.Public         → anonymous principal           → bypass
//   - Auth.Bootstrap      → HTTP Basic (bootstrap)         → bypass
//   - internal HTTP path  → service token (PrincipalService) → bypass
//   - otherwise (JWT, incl. ServiceOwned / PasswordResetExempt) → PrincipalUser → reachable
//
// ClientsOnly is subsumed by the internal-path check (FMT-28 confines clientsOnly to
// internal paths). So a mutating (POST/PUT/PATCH/DELETE), non-exempt,
// PrincipalUser-reachable route returns FrameworkIdempotencyStatuses(); GET/HEAD,
// idempotency.exempt, and non-PrincipalUser auth shapes return nil.
//
// Compute-only (#1591): this is THE source — the statuses are NOT declared per
// contract. CH-07 forbids them from appearing in auth.responses, and
// declaredErrorStatuses folds this (auth-shape-aware) set in so CH-04 sees the same
// declared surface for a reachable route without a hand-authored copy. The drift the
// pre-#1591 design risked (a route declaring statuses it cannot emit) is eliminated
// by construction: there is no second copy to drift.
//
// INVARIANT: the auth-shape→principal-kind mapping above mirrors
// middleware.extractIdentity. AI-robust rating: Medium — the cross-layer binding to
// the runtime middleware is an archtest + INVARIANT-godoc contract, not a
// type-system Hard (kernel/ must not import runtime/; the value binding shares the
// permanent ceiling family of IDEMPOTENCY-FRAMEWORK-STATUS-ORACLE-ALIGN-01). Blind
// spot: PrincipalDevice-resolved mutating HTTP routes (none today) are not modeled
// here — out of #1591 scope, which covers public / bootstrap / service-token.
func (h *HTTPTransportMeta) IdempotencyFrameworkStatuses() []int {
	if h == nil || h.Idempotency.Exempt {
		return nil
	}
	// Non-PrincipalUser auth shapes are bypassed by middleware.extractIdentity, so
	// it never injects 409/422 on them.
	if h.Auth.Public || h.Auth.Bootstrap || IsInternalHTTPPath(h.Path) {
		return nil
	}
	switch h.Method {
	case "POST", "PUT", "PATCH", "DELETE":
		return FrameworkIdempotencyStatuses()
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
// Methods is a SPARSE per-RPC auth overlay (#1675): only RPCs needing a
// non-default auth flag (public:true) appear; absent methods inherit the
// fail-closed default (authed). The .proto remains the single source for the
// method SET — this overlay only annotates existing methods, it never declares
// them. Referential integrity (each Name ∈ the proto's method set) is enforced
// by the contractgen pre-pass (kernel⊥tools, so governance cannot read the
// .proto); governance FMT-41 enforces the metadata-pure guards (non-empty name,
// no duplicates, methods⇒codegen:true, and — in #1675 where public is the only
// flag — each entry must assert public:true).
//
// #1675 carried only the public flag; #2008 extended the overlay with the ABAC
// Permission field (GRPCMethodMeta.Permission) — the action a non-public RPC
// requires, consumed by the runtime interceptor's PDP gate. Both Public and
// Permission are now live exported fields (mutually exclusive per RPC). The PDP
// resource for coarse permissions is the full method name (applied at the gate).
// For owner-scoped permissions, #2207 added GRPCMethodMeta.Resource — a field
// name on the proto request message that the interceptor extracts per-message
// and forwards as the PDP resource, enabling the ownership rule
// (subject.sub == resource.id) to fire for owner-scoped streaming methods
// (e.g. WatchCommands declares resource: device_id so a device can watch its
// own queue). See GRPCMethodMeta.Resource.
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
	// Methods is the sparse per-RPC auth overlay; nil/empty → every RPC authed.
	// See ADR docs/architecture/202605260000-adr-grpc-transport-adapter.md
	// §"Amendment 2026-06-13 — #1675" for the threat-matrix re-eval and the #2008
	// extension plan.
	Methods []GRPCMethodMeta `yaml:"methods,omitempty" json:"methods,omitempty"`
}

// GRPCMethodMeta is one entry of the per-RPC auth overlay (#1675, ABAC extended
// in #2008). It annotates a single proto RPC with its non-default auth posture —
// either JWT-exempt (Public) or its required ABAC permission (Permission). Under
// the #2008 strict-fail-closed model the overlay is COMPLETE for non-public
// methods: every authed RPC carries a Permission, and a method absent from the
// overlay is DENIED at the interceptor gate (not merely authed). The cellgen
// completeness pre-pass (EnrichGrpcServicesWithProtoInfo, gocell generate cell)
// enforces that every non-public proto method has a Permission entry, so
// "absent ⇒ dead 403" cannot ship silently.
//
// ref: grpc-ecosystem/go-grpc-middleware interceptors/auth — per-method
// AuthFuncOverride (declarative public-method exemption)
// ref: grpc/grpc-go health/server.go — Check/Watch as the canonical public RPCs
type GRPCMethodMeta struct {
	// Name is the proto RPC method's simple name (e.g. "Check"). It MUST be a
	// member of the proto service's method set (the .proto is the single source);
	// FMT-41 + the contractgen pre-pass enforce referential integrity.
	Name string `yaml:"name" json:"name"`
	// Public marks this RPC as JWT-exempt. Codegen derives
	// GRPCServiceSpec.PublicMethods from the public:true entries, which the runtime
	// registrar aggregates into the auth interceptor's bypass set.
	//
	// omitempty is intentional: a public:false entry is meaningful only when it
	// carries a Permission (an authed RPC declaring its ABAC action). Public and
	// Permission are MUTUALLY EXCLUSIVE — a JWT-exempt RPC has no authenticated
	// subject to authorize, so requiring a permission on it is contradictory; FMT-41
	// + the schema reject the combination. An entry with neither (vacuous) is also
	// rejected.
	Public bool `yaml:"public,omitempty" json:"public,omitempty"`
	// Permission is the ABAC action string (e.g. "device:command") the non-public
	// RPC requires (#2008). It is the value carried as `action` into the PDP
	// (auth.Authorizer.Authorize); codegen derives GRPCServiceSpec.MethodPermissions
	// from these entries and the runtime registrar resolves the string back to a
	// sealed authz.Permission (fail-fast on an unknown action). It MUST be a member
	// of the closed authz registry — FMT-41 validates membership statically via
	// authz.IsKnownPermissionString, so a typo fails at `gocell validate` rather than
	// silently denying at runtime. Mutually exclusive with Public (see above).
	Permission string `yaml:"permission,omitempty" json:"permission,omitempty"`
	// Resource is the REQUEST MESSAGE field name (proto field, snake_case, e.g.
	// "device_id") whose string value becomes the PDP resource for owner-scoped
	// per-message authz (#2207). The interceptor extracts it via protoreflect on
	// the first received message, canonicalizes it (same ParseCanonicalUUID path as
	// HTTP RequirePermissionForResource), and forwards it as the PDP resource so
	// the ownership rule (subject.sub == resource.id) can fire.
	//
	// Constraints (enforced by cellgen cross-check and metadata parser):
	//   - Mutually exclusive with Public (a JWT-exempt RPC has no subject to compare).
	//   - Only valid when Permission is also set (resource extraction without an ABAC
	//     decision is meaningless; the gate always runs).
	//   - Must be set when Permission refers to an owner-scoped authz.Permission
	//     (authz.Permission.IsOwnerScoped()==true); omitting it silently locks out
	//     the owner because fullMethod never equals device-id.
	//
	// Mirrors HTTP auth.RequirePermissionForResource's path-param role for gRPC.
	Resource string `yaml:"resource,omitempty" json:"resource,omitempty"`
	// PasswordResetExempt marks this RPC as exempt from the password-reset gate
	// (#1382). Codegen derives GRPCServiceSpec.PasswordResetExemptMethods from the
	// passwordResetExempt:true entries, which the runtime registrar aggregates into
	// the auth interceptor's exempt set.
	//
	// omitempty is intentional: a passwordResetExempt:false entry is meaningless
	// (absent = blocked on reset).
	//
	// Constraints (schema-enforced):
	//   - Mutually exclusive with Public: a JWT-exempt RPC has no authenticated
	//     subject, so the reset gate (which runs after authn) is contradictory.
	//   - Must coexist with Permission: the exempt method is still non-public and
	//     still requires ABAC authorization — permission is orthogonal to exempt
	//     (the ABAC gate always runs on the non-public path). Omitting permission
	//     would make it a dead 403.
	//
	// Naming is consistent with HTTPAuthMeta.PasswordResetExempt.
	PasswordResetExempt bool `yaml:"passwordResetExempt,omitempty" json:"passwordResetExempt,omitempty"`
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
	// Reason documents WHY this route opts out of the ABAC default (#2020). It is
	// REQUIRED (non-empty) whenever any opt-out flag (public / serviceOwned /
	// bootstrap / clientsOnly) is set, and forbidden otherwise — ABAC (the default,
	// expressed via endpoints.http.permission) is self-justifying and needs no
	// reason. Enforced by the schema if/then rules (Hard), the cellgen completeness
	// gate (Hard), and governance FMT-42 (Medium defense-in-depth). It is NOT a
	// mutex-governed mode flag, so it stays outside the 2^5 AuthComboLegal matrix
	// (like Responses). passwordResetExempt is a modifier, not an opt-out mode, and
	// does not require a reason on its own.
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty"`
	// Responses lists HTTP status codes injected by listener-mounted middleware,
	// NOT emitted by the handler/adapter — so they are declared here (no typed
	// response struct) rather than in the responses map. Despite the "auth" name,
	// this list spans non-auth middleware the oracle does NOT compute: bootstrap
	// auth 401, rate limiter 429. CH-04 treats these as declared without requiring
	// handler AST emission.
	//
	// The HTTP-idempotency framework statuses 409 (ClaimBusy) / 422 (key-reused)
	// MUST NOT appear here (compute-only, #1591): they are computed from method +
	// auth shape by IdempotencyFrameworkStatuses() and folded into the declared
	// surface by governance.declaredErrorStatuses. CH-07 forbids hand-authoring them
	// in this list.
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
// even if zero". Governance rule FMT-25 treats the facets asymmetrically:
//   - maxLength and numeric minimum / maximum are REQUIRED for non-uuid string
//     and integer/number params; a missing declaration is a violation. An
//     explicit minimum: 0 is accepted (it rejects negatives — real semantics).
//   - minLength is OPTIONAL (the lower string bound carries no DoS weight), but
//     when declared it must be >= 1: an explicit no-op minLength: 0 is rejected
//     (string length is always >= 0, so a zero lower bound constrains nothing).
//
// Minimum / Maximum govern both integer and number parameters; use integer-valued
// bounds in contract.yaml until ParamSchema grows decimal bound fields.
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
