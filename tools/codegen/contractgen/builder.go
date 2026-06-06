package contractgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/contractpath"
	"github.com/ghbvf/gocell/runtime/http/schemavalidate"
)

// buildContractSpec projects a single contract.yaml + its schemaRefs into a
// ContractGenSpec. Returns error when:
//   - contract not found in project
//   - Codegen flag is false
//   - schemaRef parsing fails
//   - kind=http but http endpoint missing
//   - kind=event but payload schemaRef missing
//   - kind=grpc but endpoints.grpc missing, service/method empty, method not an
//     exported Go identifier, proto empty or not rooted under
//     metadata.GRPCProtoPathPrefix, service/proto carrying a control character,
//     or a non-unary streamingType (streaming codegen deferred to PR 10)
func buildContractSpec(rootDir string, p *metadata.ProjectMeta, contractID string) (*ContractGenSpec, error) {
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

	kebab := contractIDToKebab(contractID)
	spec := &ContractGenSpec{
		PackageName:                             pkgName,
		PackagePath:                             pkgPath,
		ContractID:                              contractID,
		Kind:                                    contract.Kind,
		Transports:                              contract.Transports,
		ConsistencyLevel:                        contract.ConsistencyLevel,
		SourceFile:                              contract.File,
		PanicReasonPolicyNil:                    kebab + "-policy-nil",
		PanicReasonBootstrapAuthNil:             kebab + "-bootstrap-auth-nil",
		PanicReasonPublicSchemaCompileFailed:    kebab + "-public-schema-compile-failed",
		PanicReasonBootstrapSchemaCompileFailed: kebab + "-bootstrap-schema-compile-failed",
		PanicReasonClientsOnlySchemaCompileFailed:  kebab + "-clients-only-schema-compile-failed",
		PanicReasonServiceOwnedSchemaCompileFailed: kebab + "-service-owned-schema-compile-failed",
		PanicReasonStandardSchemaCompileFailed:     kebab + "-standard-schema-compile-failed",
	}

	// Fail closed on empty transports before any kind-specific template can
	// index into Transports[0] and panic. The parser sets default transports for
	// all known kinds (defaultTransportsForKind); nil/empty only occurs for an
	// unknown kind whose governance rule FMT-39 was not run (e.g. direct codegen
	// invocation bypassing gocell validate). Catching it here makes the error
	// self-explaining rather than an index-out-of-range template panic.
	if len(contract.Transports) == 0 {
		return nil, fmt.Errorf(
			"contractgen build: contract %q has empty transports (parser defaults per kind; "+
				"an unknown kind yields none — governance FMT-39 should have rejected this)",
			contractID)
	}

	contractDir := filepath.Dir(contract.File)

	if err := buildKindSpec(spec, rootDir, contract, contractDir); err != nil {
		return nil, err
	}

	return spec, nil
}

// buildKindSpec dispatches per-kind spec population onto spec. Extracted from
// buildContractSpec to keep that function's cyclomatic complexity bounded.
func buildKindSpec(spec *ContractGenSpec, rootDir string, contract *metadata.ContractMeta, contractDir string) error {
	switch contract.Kind {
	case "http":
		return buildHTTPSpec(spec, rootDir, contract, contractDir)
	case "event":
		return buildEventSpec(spec, rootDir, contract, contractDir)
	case "saga":
		return buildSagaSpec(spec, rootDir, contract, contractDir)
	case "grpc":
		return buildGRPCSpec(spec, rootDir, contract)
	case "projection":
		return validateProjectionLevel(contract.ID, contract.ConsistencyLevel)
	case "command":
		return buildCommandSpec(spec, rootDir, contract, contractDir)
	case "webhook":
		// webhook: recognized, zero artifacts by design — registration uses
		// kernel/webhook.ReceiverSpec literals via cellgen, no per-contract package.
		// generateOneContract and RenderContractArtifacts skip all artifact emission
		// for this kind; the early-return branches in those functions are the
		// enforcement point.
	default:
		return fmt.Errorf(
			"contractgen build: contract %q has unsupported kind %q (http|event|command|projection|webhook|grpc|saga)",
			contract.ID, contract.Kind)
	}

	return nil
}

// validateProjectionLevel enforces the contractgen half of PROJECTION-CONSISTENCY-01
// (gh #960): the generated types_gen.go carries a compile-time guard
// `const _ = uint(cellvocab.<level> - cellvocab.L3)`. The level must parse to a
// known cellvocab.Level so the template emits a valid cellvocab identifier; an
// empty/garbage level would render uncompilable Go for the wrong reason, so it is
// rejected here with a clean build error. The floor check (>= L3) is deliberately
// NOT done here — that is the compile-time guard's job (Hard downstream); doing it
// here would degrade it to a builder-time guard.
func validateProjectionLevel(contractID, level string) error {
	if _, err := cellvocab.ParseLevel(level); err != nil {
		return fmt.Errorf(
			"contractgen build: projection contract %q has invalid consistencyLevel %q (must be L0..L4): %w",
			contractID, level, err)
	}
	return nil
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
	// binary bloat). The `endpointSpec.HasBody` gate here combined with the
	// `if .RequestSchemaJSON` template gate is the single funnel — generated
	// no-body handlers cannot contain a requestSchemaJSON literal, byte-pinned
	// by the http_order_get_v1 / http_order_list_v1 / synth_http_full handler
	// goldens in render_test.go.
	// ref: oapi-codegen — request validator emitted only for operations with
	// a requestBody.
	if contract.SchemaRefs.Request != "" && endpointSpec.HasBody {
		reqPath := filepath.Join(rootDir, contractDir, contract.SchemaRefs.Request)
		schemaBytes, err := os.ReadFile(reqPath) //nolint:gosec // schema path resolved from contract.yaml metadata
		if err != nil {
			return fmt.Errorf("contractgen build: %q read request schema for embed: %w", contract.ID, err)
		}
		// Resolve external $ref entries so the embedded schema is self-contained.
		// schemavalidate.NewValidator (santhosh-tekuri, base URI "mem:///") has no
		// external loader; an unresolved $ref would fail to compile at codegen time.
		bundled, err := bundleSchemaRefs(rootDir, reqPath, schemaBytes)
		if err != nil {
			return fmt.Errorf("contractgen build: %q bundle request schema $ref: %w", contract.ID, err)
		}
		// Compact to a single line: eliminates newlines so the schema can be
		// safely embedded in the generated file as a Go interpreted string literal.
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, bundled); err != nil {
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
		respDTOs, err := buildResponseDTOs(rootDir, contract, contractDir)
		if err != nil {
			return nil, err
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

// buildResponseDTOs loads and validates the response schema for an HTTP contract,
// applies both wire-out guards (audit-domain and credential-idempotency), and
// converts the schema to DTOSpecs. Extracted from buildHTTPDTOs to keep that
// function's cognitive complexity within the project limit of 15.
func buildResponseDTOs(rootDir string, contract *metadata.ContractMeta, contractDir string) ([]DTOSpec, error) {
	respPath := filepath.Join(contractDir, contract.SchemaRefs.Response)
	respSchema, err := Parse(rootDir, respPath)
	if err != nil {
		return nil, fmt.Errorf("contractgen build: %q response schema: %w", contract.ID, err)
	}
	// Wire-out funnel A: audit-domain responses must not project Principal
	// credentials (AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01). Request path is
	// intentionally exempt (inbound password/token fields are legitimate).
	if err := rejectSensitiveAuditWireFields(contract.ID, "response", respSchema); err != nil {
		return nil, fmt.Errorf("contractgen build: %w", err)
	}
	// Wire-out funnel B: any HTTP response carrying a sensitive key (per
	// pkg/redaction.IsSensitiveKey) must declare idempotency.exempt: true so the
	// idempotency store does not record+replay credentials into Redis.
	// Exempt when contract.Endpoints.HTTP is nil (occurs only in unit tests that
	// exercise schema parsing without a full contract; production path always has
	// HTTP set because buildHTTPSpec gates on http != nil).
	idempotencyExempt := contract.Endpoints.HTTP != nil && contract.Endpoints.HTTP.Idempotency.Exempt
	if err := rejectUnexemptCredentialResponse(contract.ID, idempotencyExempt, respSchema); err != nil {
		return nil, fmt.Errorf("contractgen build: %w", err)
	}
	respDTOs, err := schemaToDTOs("Response", respSchema)
	if err != nil {
		return nil, fmt.Errorf("contractgen build: %q response DTOs: %w", contract.ID, err)
	}
	return respDTOs, nil
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

// buildHTTPEndpointSpec is the SOLE constructor of the sealed httpEndpointSpec
// and the FMT-34 funnel entry (it calls validateAuthOnInternalPath). It is
// called only from buildHTTPSpec, which assigns the result to
// ContractGenSpec.Endpoint. The "sole constructor / sole caller" invariant is
// locked by archtest CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (A1b); the
// cross-package bypass is sealed by httpEndpointSpec being unexported.
//
// It constructs the httpEndpointSpec including pagination detection.
// HasBody is true only when the HTTP method is POST/PUT/PATCH AND the contract declares
// a schemaRefs.request — POST/PATCH endpoints that accept only path params (no request
// body schema) must not call DecodeJSONStrict (an empty body would be rejected).
// pathParams and queryParams are pre-computed by buildHTTPSpec (F-09: avoid re-computing).
func buildHTTPEndpointSpec(
	contract *metadata.ContractMeta,
	http *metadata.HTTPTransportMeta,
	pathParams, queryParams []ParamSpec,
) (*httpEndpointSpec, error) {
	handlerMethod := goPascalCase(domainLastSegment(contract.ID))
	methodHasBody := http.Method == "POST" || http.Method == "PUT" || http.Method == "PATCH"
	hasBody := methodHasBody && contract.SchemaRefs.Request != ""

	// Clients are only wired into the generated contractSpec for /internal/v1/...
	// paths. For public /api/v1/... paths, contract.yaml endpoints.clients is
	// informational metadata (who calls this endpoint) — auth.Mount rejects
	// Clients on non-internal paths (contractspec.ContractSpec validation rule).
	var clients []string
	isInternalPath := metadata.IsInternalHTTPPath(http.Path)
	if isInternalPath && len(contract.Endpoints.Clients) > 0 {
		clients = append(clients, contract.Endpoints.Clients...)
	}

	if err := validateAuthServiceOwned(contract.ID, http.Auth); err != nil {
		return nil, err
	}

	if err := validateAuthClientsOnly(contract.ID, http.Path, http.Auth, contract.Endpoints.Clients, isInternalPath); err != nil {
		return nil, err
	}

	if err := validateAuthOnInternalPath(contract.ID, http.Path, http.Auth); err != nil {
		return nil, err
	}

	spec := &httpEndpointSpec{
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
		AuthClientsOnly:         http.Auth.ClientsOnly,
		AuthServiceOwned:        http.Auth.ServiceOwned,
		IdempotencyExempt:       http.Idempotency.Exempt,
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

func validateAuthServiceOwned(contractID string, auth metadata.HTTPAuthMeta) error {
	if !auth.ServiceOwned {
		return nil
	}
	if !auth.Public && !auth.Bootstrap && !auth.ClientsOnly {
		return nil
	}
	return fmt.Errorf(
		"contractgen build: contract %q declares auth.serviceOwned:true with auth.public/auth.bootstrap/auth.clientsOnly; "+
			"serviceOwned keeps listener JWT auth and delegates ownership authorization to the service, "+
			"so it cannot be combined with auth modes that replace or bypass that route shape",
		contractID)
}

// validateAuthOnInternalPath is the codegen-side upstream Hard funnel for
// FMT-34 (kernel/governance/rules_fmt.go::validateFMT34 is the downstream
// Medium half). validateAuthOnInternalPath is called unconditionally inside
// buildHTTPEndpointSpec — the sole production HTTP codegen entry — so any
// HTTP contract generation path triggers this funnel automatically. That
// "sole entry" premise is no longer grep-only: it is enforced by the sealed
// (unexported) httpEndpointSpec type plus archtest
// CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (sole constructor/caller +
// http.Handler emit-uniqueness across tools/codegen/**).
//
// Uses metadata.IsInternalHTTPPath as the single oracle for the /internal/v1
// predicate (shared with governance + runtime), preventing prefix-string
// divergence across layers.
func validateAuthOnInternalPath(contractID, path string, auth metadata.HTTPAuthMeta) error {
	if !metadata.IsInternalHTTPPath(path) {
		return nil
	}
	var errs []error
	if auth.Public {
		errs = append(errs, fmt.Errorf(
			"contractgen build: contract %q [FMT-34] declares auth.public:true on "+
				"internal path %q [field: endpoints.http.auth.public]; "+
				"internal endpoints must not bypass JWT "+
				"(use auth.serviceOwned or auth.clientsOnly instead)"+
				"; fix: remove auth.public or move the endpoint off /internal/v1/",
			contractID, path))
	}
	if auth.PasswordResetExempt {
		errs = append(errs, fmt.Errorf(
			"contractgen build: contract %q [FMT-34] declares auth.passwordResetExempt:true "+
				"on internal path %q [field: endpoints.http.auth.passwordResetExempt]; "+
				"internal endpoints are cell-to-cell only and must "+
				"not accept the password-reset bypass token"+
				"; fix: remove auth.passwordResetExempt or move the endpoint off /internal/v1/",
			contractID, path))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func validateAuthClientsOnly(
	contractID string,
	path string,
	auth metadata.HTTPAuthMeta,
	declaredClients []string,
	isInternalPath bool,
) error {
	if !auth.ClientsOnly {
		return nil
	}
	if auth.Public || auth.Bootstrap || auth.PasswordResetExempt {
		return fmt.Errorf(
			"contractgen build: contract %q declares auth.clientsOnly:true with auth.public/auth.bootstrap/auth.passwordResetExempt; "+
				"clientsOnly relies on caller-cell identity only and cannot be combined with listener-bypass or password-reset auth modes",
			contractID)
	}
	if !isInternalPath {
		return fmt.Errorf(
			"contractgen build: contract %q declares auth.clientsOnly:true but path %q is "+
				"not an internal path (must match /internal/v1 or /internal/v1/...); "+
				"clientsOnly is only meaningful for internal endpoints where caller-cell identity is verifiable",
			contractID, path)
	}
	if len(declaredClients) == 0 {
		return fmt.Errorf(
			"contractgen build: contract %q declares auth.clientsOnly:true but endpoints.clients is empty; "+
				"clientsOnly requires at least one declared client cell so RequireCallerCell has an allowlist to enforce",
			contractID)
	}
	return nil
}

// detectPagination scans QueryParams; if both cursor (string) and limit
// (integer) are present, the endpoint is paginated and Pagination is set.
// Any non-cursor/non-limit query params land in ExtraQueryParams so the
// handler template can route them through per-param parsing while
// cursor/limit always go through pkg/httputil.ParsePageParams.
func detectPagination(spec *httpEndpointSpec) error {
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
	// Wire-out funnel: audit-domain event payloads must not project Principal
	// credentials (AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01).
	if err := rejectSensitiveAuditWireFields(contract.ID, "payload", payloadSchema); err != nil {
		return fmt.Errorf("contractgen build: %w", err)
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
		// Wire-out funnel: event headers are an outbound wire surface too, so
		// audit-domain headers must not project Principal credentials either
		// (AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01). event.audit.appended.v1 ships
		// a real headers schema — leaving it unguarded would let a sensitive key
		// added there bypass the payload-only check.
		if err := rejectSensitiveAuditWireFields(contract.ID, "headers", headersSchema); err != nil {
			return fmt.Errorf("contractgen build: %w", err)
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

// buildCommandSpec projects a kind=command contract into spec.Command and
// appends Request + Response DTOs to spec.DTOs. It is the codegen Hard half of
// COMMAND-CONTRACT-SCHEMA-REF-01 (the governance Medium 兜底 is rules_command.go):
// both schemaRefs.request and schemaRefs.response must be non-empty.
//
// Fail-closed: missing either ref (or both) returns an error — there is no
// "stub" / graceful-skip path. A codegen command always emits a full funnel
// (spec.Command is non-nil whenever buildCommandSpec returns nil), so
// generateOneContract emits command_gen.go unconditionally for kind=command.
func buildCommandSpec(spec *ContractGenSpec, rootDir string, contract *metadata.ContractMeta, contractDir string) error {
	reqRef := strings.TrimSpace(contract.SchemaRefs.Request)
	respRef := strings.TrimSpace(contract.SchemaRefs.Response)

	// Fail-closed (the codegen Hard half of COMMAND-CONTRACT-SCHEMA-REF-01): a
	// codegen command MUST declare BOTH request and response schemas — they are the
	// typed Handler signature (*Request) (*Response, error). Missing either (or
	// both) is a misconfiguration, NOT a "stub" to silently skip: the governance
	// rule flags it at validate-time and contractgen rejects it here so a refs-less
	// command can never half-generate (no static no-op / silent degradation).
	if reqRef == "" || respRef == "" {
		return fmt.Errorf("contractgen build: command contract %q must declare both schemaRefs.request and "+
			"schemaRefs.response (COMMAND-CONTRACT-SCHEMA-REF-01); got request=%q response=%q",
			contract.ID, contract.SchemaRefs.Request, contract.SchemaRefs.Response)
	}

	dtos, err := buildCommandDTOs(rootDir, contract, contractDir, reqRef, respRef)
	if err != nil {
		return err
	}
	spec.DTOs = dtos

	domainLast := domainLastSegment(contract.ID)
	spec.Command = &CommandSpec{
		DispatchID:     contract.ID,
		HandlerMethod:  "Handle" + goPascalCase(domainLast),
		RequestGoType:  "Request",
		ResponseGoType: "Response",
	}
	return nil
}

// buildCommandDTOs loads request and response schemas for a kind=command contract
// and returns the flattened DTOSpec slice (Request + Response + any nested types).
// Extracted from buildCommandSpec to keep cognitive complexity ≤ 15.
func buildCommandDTOs(rootDir string, contract *metadata.ContractMeta, contractDir, reqRef, respRef string) ([]DTOSpec, error) {
	var allDTOs []DTOSpec

	// Request DTO.
	reqPath := filepath.Join(contractDir, reqRef)
	reqSchema, err := Parse(rootDir, reqPath)
	if err != nil {
		return nil, fmt.Errorf("contractgen build: %q command request schema: %w", contract.ID, err)
	}
	reqDTOs, err := schemaToDTOs("Request", reqSchema)
	if err != nil {
		return nil, fmt.Errorf("contractgen build: %q command request DTOs: %w", contract.ID, err)
	}
	allDTOs = append(allDTOs, reqDTOs...)

	// Response DTO.
	respPath := filepath.Join(contractDir, respRef)
	respSchema, err := Parse(rootDir, respPath)
	if err != nil {
		return nil, fmt.Errorf("contractgen build: %q command response schema: %w", contract.ID, err)
	}
	respDTOs, err := schemaToDTOs("Response", respSchema)
	if err != nil {
		return nil, fmt.Errorf("contractgen build: %q command response DTOs: %w", contract.ID, err)
	}
	allDTOs = append(allDTOs, respDTOs...)

	// Ensure stubs exist — command_gen.go always references *Request and *Response.
	if !hasDTONamed(allDTOs, "Request") {
		allDTOs = append([]DTOSpec{{
			Name: "Request",
			Doc:  contract.ID + ".request",
		}}, allDTOs...)
	}
	if !hasDTONamed(allDTOs, "Response") {
		allDTOs = append(allDTOs, DTOSpec{
			Name: "Response",
			Doc:  contract.ID + ".response",
		})
	}
	return allDTOs, nil
}

// buildSagaSpec projects a kind=saga contract's saga block into spec.Saga and
// appends each step's output-schema DTOs to spec.DTOs. The typed input of a
// step is the previous step's output (the first step takes no typed input —
// the runtime feeds nil prevState to step 0; see kernel/saga StepFunc +
// runtime/saga foldEvents). Reuses Parse + schemaToDTOs (no bundleSchemaRefs:
// saga steps validate at the domain layer, not in generated code).
func buildSagaSpec(spec *ContractGenSpec, rootDir string, contract *metadata.ContractMeta, contractDir string) error {
	sm := contract.Saga
	if sm == nil {
		return fmt.Errorf("contractgen build: contract %q is kind=saga but has no saga block", contract.ID)
	}
	if len(sm.Steps) == 0 {
		return fmt.Errorf("contractgen build: contract %q saga has no steps", contract.ID)
	}
	if sm.CompensationOrder != "" && sm.CompensationOrder != "reverse" {
		return fmt.Errorf("contractgen build: contract %q saga compensationOrder %q unsupported (only \"reverse\")",
			contract.ID, sm.CompensationOrder)
	}

	timeoutExpr, err := durationExpr(sm.Timeout)
	if err != nil {
		return fmt.Errorf("contractgen build: contract %q saga timeout: %w", contract.ID, err)
	}
	retry, err := sagaRetrySpec(sm.Retries)
	if err != nil {
		return fmt.Errorf("contractgen build: contract %q saga retries: %w", contract.ID, err)
	}

	out := &SagaSpec{
		DefinitionID:      contract.ID,
		TimeoutExpr:       timeoutExpr,
		RetryPolicy:       retry,
		CompensationOrder: "reverse",
	}

	// seenIdent tracks identifier -> first step index for collision detection.
	// Keys are both step.GoName and each DTO name emitted by buildSagaStep.
	seenIdent := make(map[string]int)

	var prevOutput string
	var allDTOs []DTOSpec
	for i := range sm.Steps {
		step, dtos, err := buildSagaStep(rootDir, contract, contractDir, i, prevOutput)
		if err != nil {
			return err
		}
		if err := checkSagaStepIdentCollision(seenIdent, contract, i, step, dtos); err != nil {
			return err
		}
		allDTOs = append(allDTOs, dtos...)
		out.Steps = append(out.Steps, step)
		prevOutput = step.OutputGoType
	}

	out.NeedsTime = sagaNeedsTime(out)
	spec.DTOs = allDTOs
	spec.Saga = out
	return nil
}

// checkSagaStepIdentCollision registers the Go identifiers step i emits (its
// GoName and each output DTO name) into seen, failing fast if any collides with
// an identifier an earlier step already produced. Two distinct step names can
// collapse to the same identifier after goPascalCase ("reserve" and "Reserve"
// both → "Reserve"), which would emit duplicate Run<Name>/<Name>Output
// declarations into an uncompilable generated package — this codegen-funnel
// fail-fast makes that output unexpressible. Extracted from buildSagaSpec to
// keep it within the cognitive-complexity budget.
func checkSagaStepIdentCollision(seen map[string]int, contract *metadata.ContractMeta, i int, step SagaStepSpec, dtos []DTOSpec) error {
	steps := contract.Saga.Steps
	idents := make([]string, 0, len(dtos)+1)
	idents = append(idents, step.GoName)
	for _, dto := range dtos {
		idents = append(idents, dto.Name)
	}
	for _, id := range idents {
		if prev, ok := seen[id]; ok {
			return fmt.Errorf(
				"contractgen build: contract %q saga steps[%d] %q and steps[%d] %q produce the same Go identifier %q "+
					"(names collapse after PascalCase, e.g. \"reserve\"/\"Reserve\"); rename one step",
				contract.ID, prev, steps[prev].Name, i, steps[i].Name, id)
		}
		seen[id] = i
	}
	return nil
}

// buildSagaStep projects one saga step into a SagaStepSpec and its output DTOs.
// prevOutput is the previous step's OutputGoType ("" for the first step).
func buildSagaStep(
	rootDir string,
	contract *metadata.ContractMeta,
	contractDir string,
	i int,
	prevOutput string,
) (SagaStepSpec, []DTOSpec, error) {
	st := contract.Saga.Steps[i]
	if strings.TrimSpace(st.Output) == "" {
		return SagaStepSpec{}, nil, fmt.Errorf(
			"contractgen build: contract %q saga step %d (%q) missing output schema $ref",
			contract.ID, i, st.Name)
	}
	// The output ref must be a contract-relative path; reject absolute paths and
	// ".." traversal so a contract.yaml cannot drive the schema loader outside
	// its own directory (defense-in-depth — contract.yaml is repo-trusted, but
	// the guard keeps generation hermetic). filepath.IsLocal (Go 1.20+) rejects
	// both forms.
	if !filepath.IsLocal(st.Output) {
		return SagaStepSpec{}, nil, fmt.Errorf(
			"contractgen build: contract %q saga step %d (%q) output %q must be a contract-relative path (no '..' or absolute)",
			contract.ID, i, st.Name, st.Output)
	}
	goName := goPascalCase(st.Name)
	outType := goName + "Output"

	schema, err := Parse(rootDir, filepath.Join(contractDir, st.Output))
	if err != nil {
		return SagaStepSpec{}, nil, fmt.Errorf(
			"contractgen build: contract %q saga step %d (%q) output schema: %w", contract.ID, i, st.Name, err)
	}
	dtos, err := schemaToDTOs(outType, schema)
	if err != nil {
		return SagaStepSpec{}, nil, fmt.Errorf(
			"contractgen build: contract %q saga step %d (%q) output DTOs: %w", contract.ID, i, st.Name, err)
	}

	timeoutExpr, err := durationExpr(st.Timeout)
	if err != nil {
		return SagaStepSpec{}, nil, fmt.Errorf(
			"contractgen build: contract %q saga step %d (%q) timeout: %w", contract.ID, i, st.Name, err)
	}
	retry, err := sagaRetrySpec(st.Retries)
	if err != nil {
		return SagaStepSpec{}, nil, fmt.Errorf(
			"contractgen build: contract %q saga step %d (%q) retries: %w", contract.ID, i, st.Name, err)
	}

	compensate := true
	if st.Compensate != nil {
		compensate = *st.Compensate
	}

	return SagaStepSpec{
		Name:          st.Name,
		GoName:        goName,
		OutputGoType:  outType,
		InputGoType:   prevOutput,
		IsFirst:       i == 0,
		HasCompensate: compensate,
		TimeoutExpr:   timeoutExpr,
		RetryPolicy:   retry,
	}, dtos, nil
}

// retryPolicySpec converts a non-nil SagaRetryMeta into a RetryPolicySpec.
// Callers guard the nil case (a nil meta means "no retry override" → omit the
// field). The kernel zero value means "inherit", so an all-zero meta yields a
// RetryPolicySpec with empty exprs (the template renders an empty literal,
// equivalent to inheriting).
//
// F6B: delegates to saga.RetryPolicy.Validate so that kernel-level invariants
// (e.g. MaxInterval >= BaseInterval) are enforced at codegen time — making an
// invalid policy UNEXPRESSIBLE in generated output (Hard codegen funnel).
func retryPolicySpec(m *metadata.SagaRetryMeta) (*RetryPolicySpec, error) {
	// Parse durations first so we can construct a saga.RetryPolicy for Validate.
	var bi, maxI time.Duration
	if m.BaseInterval != "" {
		d, err := time.ParseDuration(m.BaseInterval)
		if err != nil {
			return nil, fmt.Errorf("baseInterval: invalid duration %q: %w", m.BaseInterval, err)
		}
		bi = d
	}
	if m.MaxInterval != "" {
		d, err := time.ParseDuration(m.MaxInterval)
		if err != nil {
			return nil, fmt.Errorf("maxInterval: invalid duration %q: %w", m.MaxInterval, err)
		}
		maxI = d
	}

	// Delegate to kernel validation — catches MaxAttempts < 0, negative intervals,
	// and MaxInterval < BaseInterval (the partial-fork replaced by this funnel).
	rp := saga.RetryPolicy{
		MaxAttempts:  m.MaxAttempts,
		BaseInterval: bi,
		MaxInterval:  maxI,
	}
	if err := rp.Validate(); err != nil {
		return nil, fmt.Errorf("saga retry policy: %w", err)
	}

	baseExpr, err := durationExpr(m.BaseInterval)
	if err != nil {
		return nil, fmt.Errorf("baseInterval: %w", err)
	}
	maxIExpr, err := durationExpr(m.MaxInterval)
	if err != nil {
		return nil, fmt.Errorf("maxInterval: %w", err)
	}
	return &RetryPolicySpec{MaxAttempts: m.MaxAttempts, BaseIntervalExpr: baseExpr, MaxIntervalExpr: maxIExpr}, nil
}

// sagaRetrySpec resolves an optional SagaRetryMeta to a *RetryPolicySpec,
// returning nil (omit the field) when the meta is absent.
func sagaRetrySpec(m *metadata.SagaRetryMeta) (*RetryPolicySpec, error) {
	if m == nil {
		return nil, nil //nolint:nilnil // nil meta = "no retry override"; absence, not error
	}
	return retryPolicySpec(m)
}

// durationExpr parses a Go duration string and returns a readable Go expression
// (e.g. "30 * time.Second"). Empty input and "0" return "" (field omitted).
// Negative durations are rejected.
func durationExpr(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return "", fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if d < 0 {
		return "", fmt.Errorf("duration %q must be >= 0", s)
	}
	return goDurationExpr(d), nil
}

// goDurationExpr renders a non-negative time.Duration as a readable Go
// expression using the largest exact unit. Zero returns "" (caller omits).
func goDurationExpr(d time.Duration) string {
	switch {
	case d == 0:
		return ""
	case d%time.Hour == 0:
		return fmt.Sprintf("%d * time.Hour", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%d * time.Minute", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%d * time.Second", d/time.Second)
	case d%time.Millisecond == 0:
		return fmt.Sprintf("%d * time.Millisecond", d/time.Millisecond)
	case d%time.Microsecond == 0:
		return fmt.Sprintf("%d * time.Microsecond", d/time.Microsecond)
	default:
		return fmt.Sprintf("%d * time.Nanosecond", int64(d))
	}
}

// sagaNeedsTime reports whether any duration expression is present, so saga.tmpl
// imports the time package only when it is actually referenced.
func sagaNeedsTime(s *SagaSpec) bool {
	if s.TimeoutExpr != "" || retryNeedsTime(s.RetryPolicy) {
		return true
	}
	for _, st := range s.Steps {
		if st.TimeoutExpr != "" || retryNeedsTime(st.RetryPolicy) {
			return true
		}
	}
	return false
}

// retryNeedsTime reports whether a RetryPolicySpec carries a duration expr.
func retryNeedsTime(r *RetryPolicySpec) bool {
	return r != nil && (r.BaseIntervalExpr != "" || r.MaxIntervalExpr != "")
}

// buildGRPCSpec projects metadata.GRPCTransportMeta into spec.GRPC for the
// server-interface generator. The proto file is read (ReadProtoServiceInfo) to
// resolve the request/response proto-generated message types + the go_package
// import path emitted into the stub; the proto is the single source of that
// identity (GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01).
//
// All guards are fail-closed at codegen time (the golden test path does not run
// governance FMT-37, so this is the funnel's own defense against malformed
// endpoints.grpc):
//   - nil endpoints.grpc / empty service (mirrors buildHTTPSpec).
//   - Proto must be present and rooted under metadata.GRPCProtoPathPrefix
//     (contracts/grpc/). This mirrors governance FMT-37 (validateFMT37Proto):
//     codegen never runs FMT-37, so the funnel rejects the same proto paths the
//     governance rule would, keeping the generated doc comment's proto reference
//     a real contracts-relative path rather than an empty or stray string.
//   - Service is rendered into the interface doc comment; a control rune
//     (notably a newline) would break out of the // comment and inject arbitrary
//     text into the generated source that goimports/gofumpt accept silently.
//     Reject control runes so the comment stays a comment.
//   - Each rpc method name must be an exported Go identifier and the rpc must be
//     unary — ReadProtoServiceInfo enforces both fail-closed at proto-read time.
func buildGRPCSpec(spec *ContractGenSpec, rootDir string, contract *metadata.ContractMeta) error {
	g := contract.Endpoints.GRPC
	if g == nil {
		return fmt.Errorf("contractgen build: contract %q is kind=grpc but has no endpoints.grpc block", contract.ID)
	}
	if g.Service == "" {
		return fmt.Errorf("contractgen build: contract %q grpc block requires service", contract.ID)
	}
	if _, err := metadata.GRPCServiceGoName(g.Service); err != nil {
		return fmt.Errorf("contractgen build: contract %q grpc service: %w", contract.ID, err)
	}
	if err := validateGRPCProtoPath(contract.ID, g.Proto); err != nil {
		return err
	}

	svcInfo, err := ReadProtoServiceInfo(filepath.Join(rootDir, filepath.FromSlash(g.Proto)), g.Service)
	if err != nil {
		return fmt.Errorf("contractgen build: contract %q grpc proto: %w", contract.ID, err)
	}

	methods := make([]GRPCMethodSpec, len(svcInfo.Methods))
	for i, m := range svcInfo.Methods {
		methods[i] = GRPCMethodSpec{
			MethodName:   m.Name,
			RequestType:  m.RequestType,
			ResponseType: m.ResponseType,
		}
	}

	spec.GRPC = &GRPCEndpointSpec{
		InterfaceName:   "Server",
		ServiceFQN:      g.Service,
		ProtoPath:       g.Proto,
		ProtoImportPath: svcInfo.ImportPath,
		ProtoAlias:      svcInfo.Alias,
		Methods:         methods,
	}
	return nil
}

// validateGRPCProtoPath fail-closes on a grpc contract's endpoints.grpc.proto
// path before it is filepath.Join-ed onto rootDir and read. Delegates to
// metadata.ValidateGRPCProtoPath (the single-source 4-guard validator shared
// with governance FMT-37) and wraps the error with contract-identity context.
func validateGRPCProtoPath(contractID, proto string) error {
	if err := metadata.ValidateGRPCProtoPath(proto); err != nil {
		return fmt.Errorf("contractgen build: contract %q %w", contractID, err)
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
// Delegates to pkg/contractpath.ContractIDToPackagePath — single source of truth
// shared with cellgen, kernel/governance, and archtest.
func contractIDToPackagePath(id string) string {
	return contractpath.ContractIDToPackagePath(id)
}

// contractIDToKebab converts a contract id to a kebab-case string by replacing
// all dots with dashes. Used to pre-compute panicregister.Approved reason literals
// at codegen time so the emitted Go source contains only const string literals.
// Example: "http.order.create.v1" → "http-order-create-v1".
func contractIDToKebab(id string) string {
	return strings.ReplaceAll(id, ".", "-")
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

// splitOnDelimiters splits on "-", "_", and camelCase boundaries
// (lowercase→uppercase transitions). Handles both snake_case and camelCase inputs.
// Examples: "user_id" → ["user","id"], "eventId" → ["event","Id"],
// "httpStatus" → ["http","Status"], "UserID" → ["User","ID"].
func splitOnDelimiters(s string) []string {
	// Normalise explicit delimiters first.
	s = strings.ReplaceAll(s, "-", "_")

	// Split on underscores first; then further split each segment on camelCase
	// boundaries (transition from a lowercase/digit rune to an uppercase rune).
	rawParts := strings.Split(s, "_")
	var out []string
	for _, part := range rawParts {
		if part == "" {
			continue
		}
		runes := []rune(part)
		start := 0
		for i := 1; i < len(runes); i++ {
			if unicode.IsUpper(runes[i]) && unicode.IsLower(runes[i-1]) {
				out = append(out, string(runes[start:i]))
				start = i
			}
		}
		out = append(out, string(runes[start:]))
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
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
