package governance

// rules_http.go consolidates the three HTTP-contract governance rules:
//
//   - CH-04 (response alignment)     — handler-emitted ≥400 status codes
//                                      must be declared in the contract
//                                      responses map.
//   - CH-05 (path-param UUID)        — handlers serving contracts with
//                                      pathParams.{name}.format=uuid must
//                                      call httputil.ParseUUIDPathParam.
//   - CH-06 (typed response envelope)— generated types_gen.go typed
//                                      response struct set must equal
//                                      contract SuccessStatus + responses[].
//
// CH-05 reuses the parsedHandlerFile cache + findHandlerFile + parseHandlerFile
// machinery introduced for CH-04. CH-06 operates on the codegen artifact
// (types_gen.go) and does not share scanning state with CH-04/05. Merged into
// one file so the shared cache is colocated with all consumers.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contractpath"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/fspath"
)

const (
	fieldHTTPPath         = "endpoints.http.path"
	fieldHTTPResponsesFmt = "endpoints.http.responses[%d]"
)

// =============================================================================
// CH-04 — HTTP response alignment
// =============================================================================

// errCorrelationMissing is returned by extractHandlerStatusCodesForContract
// when no auth.Mount call in the handler file maps the given contractID to a
// handler function. Callers that receive this error must emit a fail-closed
// finding rather than silently skipping the contract.
var errCorrelationMissing = errors.New("auth.Mount correlation missing")

// httpStatusNameToCode maps the AST selector name (e.g. "StatusBadRequest")
// to the numeric HTTP status code for ≥400 responses. Only 4xx/5xx names are
// included because CH-04 only validates error response declarations. This
// table is hand-curated because go/ast sees selector expressions as strings,
// and net/http constants cannot be enumerated without running reflect over
// compiled code.
var httpStatusNameToCode = map[string]int{
	"StatusBadRequest":                    400,
	"StatusUnauthorized":                  401,
	"StatusPaymentRequired":               402,
	"StatusForbidden":                     403,
	"StatusNotFound":                      404,
	"StatusMethodNotAllowed":              405,
	"StatusNotAcceptable":                 406,
	"StatusProxyAuthRequired":             407,
	"StatusRequestTimeout":                408,
	"StatusConflict":                      409,
	"StatusGone":                          410,
	"StatusLengthRequired":                411,
	"StatusPreconditionFailed":            412,
	"StatusRequestEntityTooLarge":         413,
	"StatusRequestURITooLong":             414,
	"StatusUnsupportedMediaType":          415,
	"StatusRequestedRangeNotSatisfiable":  416,
	"StatusExpectationFailed":             417,
	"StatusTeapot":                        418,
	"StatusMisdirectedRequest":            421,
	"StatusUnprocessableEntity":           422,
	"StatusLocked":                        423,
	"StatusFailedDependency":              424,
	"StatusTooEarly":                      425,
	"StatusUpgradeRequired":               426,
	"StatusPreconditionRequired":          428,
	"StatusTooManyRequests":               429,
	"StatusRequestHeaderFieldsTooLarge":   431,
	"StatusUnavailableForLegalReasons":    451,
	"StatusInternalServerError":           500,
	"StatusNotImplemented":                501,
	"StatusBadGateway":                    502,
	"StatusServiceUnavailable":            503,
	"StatusGatewayTimeout":                504,
	"StatusHTTPVersionNotSupported":       505,
	"StatusVariantAlsoNegotiates":         506,
	"StatusInsufficientStorage":           507,
	"StatusLoopDetected":                  508,
	"StatusNotExtended":                   510,
	"StatusNetworkAuthenticationRequired": 511,
}

// httpHelperWritesStatuses is the single source of truth for every known
// pkg/httputil helper CH-04 may encounter. The value is the set of ≥400 status
// codes the helper writes internally; a helper absent from this map is treated
// as a possibly-undeclared writer and fails closed (advHintCH04UnknownHelper).
// An empty set means "known helper, contributes no inferred ≥400 status" and
// covers two cases:
//   - the helper does not write a status at all (decorators, pure utils, or
//     callers that supply the status themselves — caught by
//     collectHTTPStatusSelectors), and
//   - the helper's status is explicit through errcode.Kind, caught separately
//     by collectErrcodeKinds / collectWritePublicKind, so re-inferring it here
//     would double-count.
//
// Helpers whose status is explicit through errcode.Kind (WritePublic) are
// dispatched to collectWritePublicKind before this table is consulted.
var httpHelperWritesStatuses = map[string][]int{
	// Writers that emit a fixed ≥400 set.
	"DecodeJSON":             {http.StatusBadRequest, http.StatusRequestEntityTooLarge},
	"DecodeJSONStrict":       {http.StatusBadRequest, http.StatusRequestEntityTooLarge},
	"ParsePageParams":        {http.StatusBadRequest},
	"ParseUUIDPathParam":     {http.StatusBadRequest},
	"ParsePageParamsOrWrite": {http.StatusBadRequest},
	// WriteError's status comes from its errcode.Kind argument, which
	// collectErrcodeKinds resolves independently — empty set avoids double-count.
	"WriteError": {},
	// Framework 5xx fallbacks invoked only by generated handlers
	// (typed-envelope nil-response guard / visit encode failure path). Per
	// ADR 202605061500-adr-typed-response-envelope.md D1, the error return
	// surface is "reserved for un-declared framework 5xx" — these helpers
	// must not require contract.yaml responses[500] declaration. Empty set keeps
	// CH-04 from inferring 500 from their inner errcode.New(KindInternal, ...).
	"WriteNilResponseInternal": {},
	"WriteEncodeFaultInternal": {},
	// Non-writers: registered with an empty set so they are recognized rather
	// than failing closed as "unknown". WriteJSON's status is caller-supplied
	// (caught by collectHTTPStatusSelectors); the rest write no HTTP status.
	"WriteJSON":                       {},
	"WithClientErrorLogSampling":      {},
	"WithClientErrorLogSamplingEvery": {},
	"AppendCorrelationAttrs":          {},
	"WithCancelReasonSlot":            {},
	"CancelReason":                    {},
	"ParseCanonicalUUID":              {},
}

var errcodeKindNameToStatus = map[string]int{
	"KindInternal":         errcode.KindInternal.Status(),
	"KindInvalid":          errcode.KindInvalid.Status(),
	"KindUnauthenticated":  errcode.KindUnauthenticated.Status(),
	"KindPermissionDenied": errcode.KindPermissionDenied.Status(),
	"KindNotFound":         errcode.KindNotFound.Status(),
	"KindConflict":         errcode.KindConflict.Status(),
	"KindUnprocessable":    errcode.KindUnprocessable.Status(),
	"KindGone":             errcode.KindGone.Status(),
	"KindPayloadTooLarge":  errcode.KindPayloadTooLarge.Status(),
	"KindRateLimited":      errcode.KindRateLimited.Status(),
	"KindClientClosed":     errcode.KindClientClosed.Status(),
	"KindDeadlineExceeded": errcode.KindDeadlineExceeded.Status(),
	"KindUnavailable":      errcode.KindUnavailable.Status(),
	"KindNotImplemented":   errcode.KindNotImplemented.Status(),
}

// checkCH04 enforces CH-04: every 4xx/5xx status code that a handler can
// return must be declared in the corresponding contract's responses map.
//
// Contracts without a matching in-repo handler (e.g. external actor) are
// silently skipped. When multiple contracts share a handler.go file, the rule
// uses auth.Mount correlation to narrow scanning to the specific handler
// function linked to each contract.
func (v *Validator) checkCH04() []ValidationResult {
	// Parse cache is per checkCH04 call to avoid cross-test contamination.
	cache := map[string]*parsedHandlerFile{}
	var results []ValidationResult
	for _, c := range v.sortedContracts() {
		// CH-04 parses a handler file per contract; honor cancellation between
		// contracts so a signal-aware ctx (Ctrl-C) stops mid-scan rather than
		// after the whole repository has been walked (F2). run() observes the
		// canceled runCtx on the next rule boundary and surfaces the error.
		if v.runCtx.Err() != nil {
			return results
		}
		if c.Kind != "http" {
			continue
		}
		results = append(results, v.checkResponseAlignmentForContract(c, v.root, cache)...)
	}
	return results
}

func (v *Validator) checkResponseAlignmentForContract(
	c *metadata.ContractMeta, projectRoot string, cache map[string]*parsedHandlerFile,
) []ValidationResult {
	handlerFile := findHandlerFile(v.project, c.ID, projectRoot)
	if handlerFile == "" {
		slog.Debug("CH-04: no handler file found for contract, skipping",
			slog.String("contract", c.ID))
		return nil
	}
	// relHandler keeps absolute CI-worker paths out of user-facing messages (F8).
	relHandler := relToRoot(v.root, handlerFile)

	handlerCodes, unresolved, err := extractHandlerStatusCodesForContract(handlerFile, c.ID, cache)
	if err != nil {
		if errors.Is(err, errCorrelationMissing) {
			return []ValidationResult{v.newError(
				codeCH04, IssueRequired,
				c.File, fieldHTTPPath,
				fmt.Sprintf(advHintCH04CorrelationFailed, c.ID, relHandler),
				advHintCH04CorrelationFailedFix,
			)}
		}
		// Fail-closed: an unparseable handler means CH-04 cannot verify
		// response-status alignment, so emit a finding rather than silently
		// passing the contract (mirrors the errCorrelationMissing branch above).
		// The parser error is sanitized to strip the absolute root prefix (F8).
		return []ValidationResult{v.newError(
			codeCH04, IssueInvalid,
			c.File, fieldHTTPPath,
			fmt.Sprintf(advHintCH04ParseFailed, c.ID, relHandler, sanitizeRootPaths(v.root, err.Error())),
			advHintCH04ParseFailedFix,
		)}
	}

	declared := declaredErrorStatuses(c)
	results := buildAlignmentFindings(v, c, handlerCodes, declared)
	// Fail-closed on response writes whose status CH-04 cannot statically
	// resolve (dynamic errcode.Kind / unknown httputil writer): emit a finding
	// rather than silently dropping the un-analyzable status (F9).
	return append(results, v.dynamicWriteFindings(c, relHandler, unresolved)...)
}

// dynamicWriteFindings emits one CH-04 finding per distinct unresolved
// response-write reason collected while scanning the handler. Each reason is a
// write whose emitted status could not be determined statically (a non-static
// errcode.Kind argument or an unknown httputil writer); leaving them silent
// would let an undeclared status slip past CH-04 (fail-open). Deduplicated so a
// handler repeating the same pattern produces a single finding.
func (v *Validator) dynamicWriteFindings(c *metadata.ContractMeta, relHandler string, unresolved []string) []ValidationResult {
	if len(unresolved) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(unresolved))
	var results []ValidationResult
	for _, reason := range unresolved {
		if _, dup := seen[reason]; dup {
			continue
		}
		seen[reason] = struct{}{}
		results = append(results, v.newError(
			codeCH04, IssueInvalid,
			c.File, fieldHTTPPath,
			fmt.Sprintf(advHintCH04DynamicWrite, c.ID, relHandler, reason),
			advHintCH04DynamicWriteFix,
		))
	}
	return results
}

// declaredErrorStatuses returns the union of 4xx/5xx status codes a contract
// declares, from three sources: the responses map (handler-emitted typed
// responses), auth.responses (listener-middleware-injected codes the oracle does
// not compute — bootstrap auth 401, rate limiter 429), and the auth-shape-aware
// idempotency oracle HTTPTransportMeta.IdempotencyFrameworkStatuses() (compute-only
// framework 409/422, #1591). The triple source lets CH-04 treat all
// middleware-injected and computed codes as declared without requiring handler AST
// emission.
//
// Compute-only (#1591): the framework idempotency 409/422 are NOT hand-authored in
// auth.responses (CH-07 forbids that); they are folded in here from the oracle so a
// reachable mutating route's declared surface is preserved by computation rather
// than by a hand-authored copy. A non-PrincipalUser route (public/bootstrap/
// internal) gets nil from the oracle, so 409/422 are correctly absent. Folding no
// longer makes CH-07 vacuous because CH-07 no longer reads this set — it forbids
// framework statuses in auth.responses directly.
func declaredErrorStatuses(c *metadata.ContractMeta) map[int]struct{} {
	out := make(map[int]struct{})
	if c.Endpoints.HTTP == nil {
		return out
	}
	for status := range c.Endpoints.HTTP.Responses {
		if status >= 400 {
			out[status] = struct{}{}
		}
	}
	for _, status := range c.Endpoints.HTTP.Auth.Responses {
		if status >= 400 {
			out[status] = struct{}{}
		}
	}
	for _, status := range c.Endpoints.HTTP.IdempotencyFrameworkStatuses() {
		out[status] = struct{}{}
	}
	return out
}

// checkCH07 enforces that the idempotency framework statuses (409 ClaimBusy / 422
// key-reused) are NEVER hand-authored in auth.responses (compute-only, #1591). They
// are computed from method + auth shape by the single-source oracle
// HTTPTransportMeta.IdempotencyFrameworkStatuses() and folded into the contract's
// declared surface by declaredErrorStatuses; auth.responses is reserved for
// middleware-injected codes the oracle does NOT compute (bootstrap auth 401, rate
// limiter 429). The only middleware that injects 409/422 is idempotency, so their
// presence in auth.responses is always a hand-authored copy of a computed value —
// the drift this rule eliminates by construction: delete the copy, keep the
// computation. This replaces the pre-#1591 "must declare 409/422" completeness rule,
// which forced non-PrincipalUser routes (public/bootstrap/service-token) to declare
// statuses the middleware structurally never emits.
//
// The forbid is method/exempt/auth-shape-agnostic: the statuses are computed, so
// they are declared nowhere. A genuine handler-emitted business 409/422 belongs in
// the responses map (a typed business response), not auth.responses.
//
// INVARIANT: CH-07 (idempotency framework status is compute-only — never
// hand-authored in auth.responses). AI-robust rating: Medium (governance type-aware
// scan over auth.responses; a YAML []int field cannot be type-sealed against
// specific values).
func (v *Validator) checkCH07() []ValidationResult {
	framework := make(map[int]struct{}, len(metadata.FrameworkIdempotencyStatuses()))
	for _, status := range metadata.FrameworkIdempotencyStatuses() {
		framework[status] = struct{}{}
	}
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" || c.Endpoints.HTTP == nil {
			continue
		}
		for _, status := range c.Endpoints.HTTP.Auth.Responses {
			if _, isFramework := framework[status]; !isFramework {
				continue
			}
			results = append(results, v.newError(
				codeCH07, IssueForbidden,
				contractFile(c), "endpoints.http.auth.responses",
				fmt.Sprintf("%s: idempotency framework status %d must not be hand-authored in "+
					"auth.responses — it is computed from the route's method and auth shape by "+
					"IdempotencyFrameworkStatuses() and folded into the contract surface automatically",
					c.ID, status),
				fmt.Sprintf("remove %d from endpoints.http.auth.responses", status),
			))
		}
	}
	return results
}

// buildAlignmentFindings compares handler-observed codes vs contract-declared
// codes and emits CH-04 (missing) findings. Extra declarations are not
// reported — see codeCH04 doc for rationale.
func buildAlignmentFindings(v *Validator, c *metadata.ContractMeta, observed, declared map[int]struct{}) []ValidationResult {
	var results []ValidationResult
	for _, status := range diffStatuses(observed, declared) {
		results = append(results, v.newError(
			codeCH04, IssueRequired,
			c.File, fmt.Sprintf(fieldHTTPResponsesFmt, status),
			fmt.Sprintf("%s: handler returns %d but contract does not declare it", c.ID, status),
			fmt.Sprintf("add responses[%d] to the contract or remove the handler return path", status),
		))
	}
	return results
}

// diffStatuses returns sorted status codes present in a but not in b.
func diffStatuses(a, b map[int]struct{}) []int {
	var out []int
	for s := range a {
		if _, ok := b[s]; !ok {
			out = append(out, s)
		}
	}
	sort.Ints(out)
	return out
}

// safeJoinUnderRoot joins root with path segments derived from a contract ID
// and returns ("", false) when any segment is unsafe or the assembled path
// would escape root. Generated-path assembly treats the contract ID as
// untrusted input: a segment that is empty, ".", "..", or contains a path
// separator could redirect the lookup into another package or outside the
// repository. Both a structural per-segment check and a final IsWithinRoot
// containment check are applied, mirroring the Go traversal-resistant path
// guidance (validate the components, then bind the result to a root). Shared by
// findHandlerFile (handler_gen.go) and typedEnvelopeTypesGenPath (types_gen.go).
func safeJoinUnderRoot(root string, segments ...string) (string, bool) {
	for _, s := range segments {
		if s == "" || s == "." || s == ".." ||
			strings.ContainsRune(s, '/') || strings.ContainsRune(s, os.PathSeparator) {
			return "", false
		}
	}
	full := filepath.Join(append([]string{root}, segments...)...)
	if !fspath.IsWithinRoot(root, full) {
		return "", false
	}
	return full, true
}

// relToRoot expresses p relative to root for user-facing finding messages.
// Governance findings must not leak absolute CI-worker paths (F8); falls back
// to the base name when p is not under root.
//
// Scoped to rules_http.go (CH-04/05 message redaction). Promote to helpers.go
// if a third rule needs it — it is deliberately separate from the security-
// oriented helpers there (IsWithinRoot now lives in pkg/fspath;
// repositoryRoot lives in helpers.go).
func relToRoot(root, p string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(p)
}

// sanitizeRootPaths strips the project-root prefix from s so absolute paths
// embedded by other tooling (e.g. a go/parser error that references the handler
// file by absolute path) become repo-relative in user-facing messages (F8).
//
// Strips all three forms the root may take so a symlinked root (e.g. macOS
// TempDir /var → /private/var) is covered regardless of which form the embedded
// path uses: the raw root, its filepath.Abs, and its EvalSymlinks resolution.
// Scoped to rules_http.go; see relToRoot's note on promotion.
func sanitizeRootPaths(root, s string) string {
	if root == "" {
		return s
	}
	prefixes := []string{root}
	if abs, err := filepath.Abs(root); err == nil {
		prefixes = append(prefixes, abs)
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			prefixes = append(prefixes, resolved)
		}
	}
	for _, p := range prefixes {
		s = strings.ReplaceAll(s, p+string(os.PathSeparator), "")
	}
	return s
}

// findHandlerFile resolves the handler file path for the contract serving contractID.
//
// K#06 PR-2 codegen path: when contract.Codegen is true and contract.Kind is
// "http", the handler lives in generated/contracts/<segments.../handler_gen.go
// (mirrors contractgen.contractIDToPackagePath). The generated path is tried
// first; if the file does not exist on disk, "" is returned (no legacy fallback
// for codegen contracts — Codegen=true is the single source of truth).
//
// Legacy path: for non-codegen contracts, scan slices for a "serve" role and
// return the slice-adjacent handler.go file if it exists on disk.
func findHandlerFile(project *metadata.ProjectMeta, contractID, projectRoot string) string {
	contract, ok := project.Contracts[contractID]
	if ok && contract.Codegen && contract.Kind == "http" {
		return findCodegenHandlerFile(projectRoot, contractID)
	}
	return findLegacyHandlerFile(project, contractID, projectRoot)
}

// findCodegenHandlerFile resolves the generated handler_gen.go for a codegen
// HTTP contract. Returns "" when the contract ID yields an unsafe path segment
// (empty / "." / ".." / a path separator) — contract IDs are dotted and never
// contain "/", so refusing rather than joining keeps the lookup from probing
// another package or escaping the repo (F1) — or when the file is absent
// (Codegen=true is the single source of truth; no legacy fallback).
func findCodegenHandlerFile(projectRoot, contractID string) string {
	segments := contractpath.Segments(contractID)
	dir, safe := safeJoinUnderRoot(projectRoot, append([]string{"generated", "contracts"}, segments...)...)
	if !safe {
		return ""
	}
	handlerPath := filepath.Join(dir, "handler_gen.go")
	if _, err := os.Stat(handlerPath); err == nil {
		return handlerPath
	}
	return ""
}

// findLegacyHandlerFile scans slices for a "serve" role on contractID and
// returns the slice-adjacent handler.go if it exists on disk.
func findLegacyHandlerFile(project *metadata.ProjectMeta, contractID, projectRoot string) string {
	for _, slice := range project.Slices {
		for _, usage := range slice.ContractUsages {
			if usage.Contract != contractID || usage.Role != "serve" {
				continue
			}
			// slice.File is "cells/<cell>/slices/<slice>/slice.yaml"
			handlerPath := filepath.Join(filepath.Dir(slice.File), "handler.go")
			full := filepath.Join(projectRoot, handlerPath)
			if _, err := os.Stat(full); err == nil {
				return full
			}
		}
	}
	return ""
}

// parsedHandlerFile caches the AST and derived data for a handler.go or handler_gen.go.
type parsedHandlerFile struct {
	// specVarToID maps package-level var names (e.g. "specUserGet") to their
	// ContractSpec.ID string values, resolved from var declarations in the file.
	// Used to correlate auth.Mount calls that reference spec vars by name.
	specVarToID map[string]string
	// contractToFuncs maps contract ID strings found in auth.Mount calls to the
	// handler function names in the same file. Enables per-contract function
	// body scanning instead of whole-file scanning.
	contractToFuncs map[string]string
	// funcBodies maps top-level function/method name to its ast.BlockStmt body.
	// For generated handlers, the key is "<ReceiverType>.<MethodName>" for methods
	// (e.g. "Handler.handle") in addition to bare function names.
	funcBodies map[string]ast.Node
	// generated is true when the first comment in the file is the standard
	// "Code generated by gocell generate contract. DO NOT EDIT." header.
	// When true, extractHandlerStatusCodesForContract uses the generated path.
	generated bool
}

// extractHandlerStatusCodesForContract returns the ≥400 status codes that the
// handler file can return for contractID.
//
// For generated handlers (handler_gen.go with the standard DO NOT EDIT header):
// the function scans the unexported "handle" method body of Handler, which
// is the delegate called by ServeHTTP. Correlation is via the contractSpec var
// whose ID matches contractID — if not found, errCorrelationMissing is returned.
//
// For legacy hand-written handlers: uses auth.Mount correlation (the previous
// behavior). Returns errCorrelationMissing when no auth.Mount call maps
// contractID to a handler function.
func extractHandlerStatusCodesForContract(
	filename, contractID string, cache map[string]*parsedHandlerFile,
) (map[int]struct{}, []string, error) {
	ph, err := parseHandlerFile(filename, cache)
	if err != nil {
		return nil, nil, err
	}

	if ph.generated {
		return extractFromGeneratedHandler(ph, contractID)
	}
	return extractFromLegacyHandler(ph, contractID)
}

// extractFromGeneratedHandler scans the "handle" method body of the generated
// Handler for contractID. Generated handlers delegate ServeHTTP → handle,
// so scanning handle captures all status codes emitted by the contract.
//
// Correlation: the generated file must contain a package-level var with
// ContractSpec.ID == contractID (set by collectSpecVarIDs in Pass 1). If the
// spec var is absent, errCorrelationMissing is returned (fail-closed).
func extractFromGeneratedHandler(ph *parsedHandlerFile, contractID string) (map[int]struct{}, []string, error) {
	// Verify that the contractSpec var in this file actually matches contractID.
	found := false
	for _, id := range ph.specVarToID {
		if id == contractID {
			found = true
			break
		}
	}
	if !found {
		return nil, nil, errCorrelationMissing
	}

	// The generated handler's logic lives in "Handler.handle" (unexported).
	// ServeHTTP just calls h.handle(w, r).
	const generatedHandleKey = "Handler.handle"
	body, ok := ph.funcBodies[generatedHandleKey]
	if !ok {
		// The spec var matched but the canonical delegate method is absent (e.g.
		// a future generator renamed Handler.handle). We cannot reliably scope
		// the scan to this contract, so fail closed via correlation-missing
		// rather than fall back to a whole-file scan — that fallback would drop
		// the per-call unresolved dynamic-write reasons and silently re-open the
		// F9 fail-open it was meant to close.
		return nil, nil, errCorrelationMissing
	}
	codes := make(map[int]struct{})
	unresolved := collectStatusCodesFromNode(body, codes)
	return codes, unresolved, nil
}

// extractFromLegacyHandler extracts status codes for contractID from a legacy
// hand-written handler.go using auth.Mount correlation.
func extractFromLegacyHandler(ph *parsedHandlerFile, contractID string) (map[int]struct{}, []string, error) {
	if fnName, ok := ph.contractToFuncs[contractID]; ok {
		if body, ok := ph.funcBodies[fnName]; ok {
			codes := make(map[int]struct{})
			unresolved := collectStatusCodesFromNode(body, codes)
			return codes, unresolved, nil
		}
	}
	return nil, nil, errCorrelationMissing
}

// parseHandlerFile parses filename and extracts the per-contract function
// mapping and status codes. Results are stored in cache to avoid re-parsing
// the same file for multiple metadata.
//
// For generated handler_gen.go files, the file header "// Code generated by
// gocell generate contract. DO NOT EDIT." is detected and ph.generated is set.
// Method bodies are also indexed under "ReceiverType.MethodName" keys so that
// generated handler dispatch (e.g. "Handler.handle") can be resolved.
func parseHandlerFile(filename string, cache map[string]*parsedHandlerFile) (*parsedHandlerFile, error) {
	if ph, ok := cache[filename]; ok {
		return ph, nil
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	ph := &parsedHandlerFile{
		specVarToID:     make(map[string]string),
		contractToFuncs: make(map[string]string),
		funcBodies:      make(map[string]ast.Node),
	}

	// Detect generated file via the standard gocell codegen header comment.
	// The generated file's first comment group contains the DO NOT EDIT marker.
	ph.generated = isGoCellGeneratedFile(f)

	// Pass 1: collect spec var declarations (var specFoo = contractspec.ContractSpec{ID: "..."}).
	collectSpecVarIDs(f, ph.specVarToID)

	// Pass 2: collect function/method declaration bodies (per-contract scanning
	// keys off these; status codes are collected later from the correlated body).
	collectFuncBodies(f, ph.funcBodies)

	// Pass 3: correlate auth.Mount calls to contract ID + handler function name.
	collectAuthMountCorrelations(f, ph.specVarToID, ph.contractToFuncs)

	cache[filename] = ph
	return ph, nil
}

// isGoCellGeneratedFile returns true when the AST file starts with the
// standard gocell codegen header:
//
//	// Code generated by gocell generate contract. DO NOT EDIT.
//
// Only the first comment group (the file header) is inspected.
func isGoCellGeneratedFile(f *ast.File) bool {
	const generatedMarker = "Code generated by gocell generate contract. DO NOT EDIT."
	if len(f.Comments) == 0 {
		return false
	}
	for _, c := range f.Comments[0].List {
		if strings.Contains(c.Text, generatedMarker) {
			return true
		}
	}
	return false
}

// collectFuncBodies populates funcBodies with every top-level function/method
// declaration found in f. Methods are indexed under both "MethodName" and
// "ReceiverType.MethodName" so generated handler dispatch (e.g.
// "Handler.handle") can be looked up. Status codes are not collected here:
// CH-04 scans only the correlated handler body (per contract), so a whole-file
// scan would mis-attribute codes across sibling handlers.
func collectFuncBodies(f *ast.File, funcBodies map[string]ast.Node) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		funcBodies[fn.Name.Name] = fn.Body
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			recvType := exprTypeName(fn.Recv.List[0].Type)
			if recvType != "" {
				funcBodies[recvType+"."+fn.Name.Name] = fn.Body
			}
		}
	}
}

// collectAuthMountCorrelations walks the AST of f and populates contractToFuncs
// with contractID → handlerFuncName mappings extracted from auth.Mount calls.
func collectAuthMountCorrelations(f *ast.File, specVarToID, contractToFuncs map[string]string) {
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isAuthMountCall(call) {
			return true
		}
		cID, fnName := extractAuthMountContractAndHandler(call, specVarToID)
		if cID != "" && fnName != "" {
			contractToFuncs[cID] = fnName
		}
		return true
	})
}

// collectSpecVarIDs scans package-level var declarations for ContractSpec
// composite literals and builds a map from var name to the ID string.
//
// Handles two common forms:
//
//	var specFoo = contractspec.ContractSpec{ID: "http.x.v1", ...}
//	var specFoo = SomeType{ID: "http.x.v1", ...}  // any struct with an ID field
func collectSpecVarIDs(f *ast.File, out map[string]string) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		collectSpecVarIDsFromGenDecl(gd, out)
	}
}

// collectSpecVarIDsFromGenDecl collects ContractSpec ID values from a single
// top-level var declaration block (e.g. `var ( specFoo = ...; specBar = ... )`).
func collectSpecVarIDsFromGenDecl(gd *ast.GenDecl, out map[string]string) {
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			if i >= len(vs.Values) {
				break
			}
			if id := extractContractIDFromLit(vs.Values[i]); id != "" {
				out[name.Name] = id
			}
		}
	}
}

// extractContractIDFromLit extracts the ID string from a composite literal
// that has an "ID" key field (e.g. contractspec.ContractSpec{ID: "http.x.v1"}).
// Returns "" if expr is not a composite literal or has no ID field.
func extractContractIDFromLit(expr ast.Expr) string {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "ID" {
			continue
		}
		if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			return stripQuotes(bl.Value)
		}
	}
	return ""
}

// isAuthMountCall returns true when the call is auth.Mount(…) or
// auth.MustMount(…). Both signatures share the (mux, Route) shape and bind
// a contractspec.ContractSpec literal; the governance scanner correlates the
// route declaration regardless of which variant the cell uses.
func isAuthMountCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "auth" && (sel.Sel.Name == "Mount" || sel.Sel.Name == "MustMount")
}

// extractAuthMountContractAndHandler parses an auth.Mount call and returns
// (contractID, handlerFuncName). contractID comes from the Route.Contract field
// (either an inline struct literal or a resolved spec var); handlerFuncName
// comes from the http.HandlerFunc(h.handleXxx) argument in Route.Handler.
func extractAuthMountContractAndHandler(call *ast.CallExpr, specVarToID map[string]string) (contractID, fnName string) {
	// auth.Mount takes (mux, route). Route is the second argument.
	if len(call.Args) < 2 {
		return "", ""
	}
	routeLit, ok := call.Args[1].(*ast.CompositeLit)
	if !ok {
		return "", ""
	}

	for _, elt := range routeLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Contract":
			contractID = resolveContractID(kv.Value, specVarToID)
		case "Handler":
			fnName = extractHandlerFuncName(kv.Value)
		}
	}
	return contractID, fnName
}

// resolveContractID resolves a contract ID from an expression that is either
// an inline ContractSpec literal or a reference to a spec var.
func resolveContractID(expr ast.Expr, specVarToID map[string]string) string {
	// Case 1: inline composite literal with ID field.
	if id := extractContractIDFromLit(expr); id != "" {
		return id
	}
	// Case 2: variable reference — look up in the pre-built spec var map.
	if ident, ok := expr.(*ast.Ident); ok {
		return specVarToID[ident.Name]
	}
	return ""
}

// extractHandlerFuncName extracts the handler function name from an expression
// like http.HandlerFunc(h.handleXxx) or http.HandlerFunc(handleXxx).
func extractHandlerFuncName(expr ast.Expr) string {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return ""
	}
	if len(call.Args) == 0 {
		return ""
	}
	arg := call.Args[0]
	switch a := arg.(type) {
	case *ast.SelectorExpr:
		// h.handleXxx — return "handleXxx"
		return a.Sel.Name
	case *ast.Ident:
		// handleXxx — return directly
		return a.Name
	}
	return ""
}

// collectStatusCodesFromNode walks node and adds every ≥400 HTTP status code
// encountered in call arguments to out. It returns the reasons for any response
// writes whose emitted status could not be resolved statically (non-static
// errcode.Kind / unknown httputil writer); the caller turns these into
// fail-closed CH-04 findings (F9).
func collectStatusCodesFromNode(node ast.Node, out map[int]struct{}) []string {
	var unresolved []string
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		collectHTTPStatusSelectors(call, out)
		if reason := collectErrcodeKinds(call, out); reason != "" {
			unresolved = append(unresolved, reason)
		}
		if reason := collectHelperWriteStatuses(call, out); reason != "" {
			unresolved = append(unresolved, reason)
		}
		return true
	})
	return unresolved
}

// collectHTTPStatusSelectors looks for http.StatusXxx used as arguments inside
// the given call expression. Only ≥400 codes are added to out.
func collectHTTPStatusSelectors(call *ast.CallExpr, out map[int]struct{}) {
	for _, arg := range call.Args {
		sel, ok := arg.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "http" {
			continue
		}
		if code, found := httpStatusNameToCode[sel.Sel.Name]; found && code >= 400 {
			out[code] = struct{}{}
		}
	}
}

// collectErrcodeKinds looks for errcode.KindXxx values inside errcode.New/Wrap
// calls and maps them through errcode.Kind.Status. Code names are deliberately
// ignored: runtime status is Kind-derived, so CH-04 must not reintroduce a
// second code-name status table. Returns a non-empty reason when the Kind
// argument is not a static errcode.KindXxx selector (so CH-04 cannot resolve
// the status); the caller fails closed on it (F9).
func collectErrcodeKinds(call *ast.CallExpr, out map[int]struct{}) string {
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := fun.X.(*ast.Ident)
	if !ok || pkg.Name != "errcode" {
		return ""
	}
	switch fun.Sel.Name {
	case "New", "Wrap":
	default:
		return ""
	}
	kindArg := errcodeKindArg(call.Args)
	status, found := errcodeKindStatus(kindArg)
	if !found {
		return fmt.Sprintf(advHintCH04DynamicKind, fun.Sel.Name)
	}
	if status >= 400 {
		out[status] = struct{}{}
	}
	return ""
}

func errcodeKindArg(args []ast.Expr) ast.Expr {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

func errcodeKindStatus(expr ast.Expr) (int, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "errcode" {
		return 0, false
	}
	status, ok := errcodeKindNameToStatus[sel.Sel.Name]
	return status, ok
}

// collectHelperWriteStatuses detects httputil.<HelperName>(w, ...) calls and
// adds the status codes the helper is known to write internally to out.
// Uses httpHelperWritesStatuses for the known-helper lookup.
//
// Helpers that write responses without accepting a status code parameter are
// invisible to collectHTTPStatusSelectors, so this table bridges that gap for
// CH-04. A genuinely unknown httputil writer returns a non-empty reason so the
// caller fails closed rather than silently skipping a possibly-undeclared
// status (F9).
func collectHelperWriteStatuses(call *ast.CallExpr, out map[int]struct{}) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "httputil" {
		return ""
	}
	helperName := sel.Sel.Name
	if helperName == "WritePublic" {
		return collectWritePublicKind(call, out)
	}
	statuses, known := httpHelperWritesStatuses[helperName]
	if !known {
		// A helper absent from the single httpHelperWritesStatuses table is a
		// genuinely unknown writer that may emit an undeclared status — fail
		// closed. Known non-writers carry an empty set in that table (see its doc).
		return fmt.Sprintf(advHintCH04UnknownHelper, helperName)
	}
	for _, s := range statuses {
		if s >= 400 {
			out[s] = struct{}{}
		}
	}
	return ""
}

func collectWritePublicKind(call *ast.CallExpr, out map[int]struct{}) string {
	if len(call.Args) < 3 {
		return advHintCH04WritePublicNoKind
	}
	status, found := errcodeKindStatus(call.Args[2])
	if !found {
		return advHintCH04WritePublicDynamicKind
	}
	if status >= 400 {
		out[status] = struct{}{}
	}
	return ""
}

// stripQuotes removes the enclosing double-quote characters from a Go string
// literal value returned by the AST (e.g. `"http.x.v1"` → `http.x.v1`).
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// =============================================================================
// CH-05 — HTTP path-param UUID parsing
// =============================================================================

// CheckHTTPPathParamUUID enforces CH-05: for every contract with
// pathParams.{name}.format == "uuid", the corresponding handler must call
// httputil.ParseUUIDPathParam(w, r, "{name}") for that parameter.
//
// Contracts without a matching in-repo handler are silently skipped.
//
// checkCH05 enforces CH-05: handlers serving contracts with
// pathParams.{name}.format=uuid must call httputil.ParseUUIDPathParam.
//
// CH-05 reuses the same parsedHandlerFile cache and contractToFuncs mapping
// from CH-04 to narrow the walk to the specific handler function linked to
// each contract via auth.Mount. When no auth.Mount correlation is found the
// rule emits a SeverityError finding (fail-closed) rather than falling back
// to whole-file scanning.
func (v *Validator) checkCH05() []ValidationResult {
	// Share the same parse cache across all contracts in one call; avoids
	// re-parsing the same handler.go for every contract it serves.
	cache := map[string]*parsedHandlerFile{}
	var results []ValidationResult
	for _, c := range v.sortedContracts() {
		// Honor cancellation between contracts (F2) — CH-05 also parses a
		// handler file per contract.
		if v.runCtx.Err() != nil {
			return results
		}
		if c.Kind != "http" {
			continue
		}
		results = append(results, v.checkPathParamUUIDForContract(c, v.root, cache)...)
	}
	return results
}

func (v *Validator) checkPathParamUUIDForContract(
	c *metadata.ContractMeta, projectRoot string, cache map[string]*parsedHandlerFile,
) []ValidationResult {
	uuidParams := collectUUIDPathParams(c)
	if len(uuidParams) == 0 {
		return nil
	}

	handlerFile := findHandlerFile(v.project, c.ID, projectRoot)
	if handlerFile == "" {
		return nil
	}

	ph, err := parseHandlerFile(handlerFile, cache)
	if err != nil {
		// Fail-closed, symmetric with CH-04: an unparseable handler means the
		// UUID-param check cannot run, so emit a finding rather than skip. The
		// path is relativized and the parser error sanitized so absolute
		// CI-worker paths do not leak into the message (F8, symmetric with CH-04).
		return []ValidationResult{v.newError(
			codeCH05, IssueInvalid,
			c.File, fieldHTTPPath,
			fmt.Sprintf(advHintCH05ParseFailed, c.ID, relToRoot(v.root, handlerFile), sanitizeRootPaths(v.root, err.Error())),
			advHintCH05ParseFailedFix,
		)}
	}

	fnName, ok := ph.contractToFuncs[c.ID]
	if !ok {
		return []ValidationResult{v.newError(
			codeCH05, IssueRequired,
			c.File, fieldHTTPPath,
			fmt.Sprintf(advHintCH05CorrelationFailed, c.ID),
			advHintCH05CorrelationFailedFix,
		)}
	}

	body, ok := ph.funcBodies[fnName]
	if !ok {
		return []ValidationResult{v.newError(
			codeCH05, IssueRequired,
			c.File, fieldHTTPPath,
			fmt.Sprintf(advHintCH05CorrelationFailed, c.ID),
			advHintCH05CorrelationFailedFix,
		)}
	}

	parsed := collectParsedUUIDParamNamesFromAST(body)
	return buildPathParamFindings(v, c, uuidParams, parsed)
}

// buildPathParamFindings compares required UUID params vs parsed call sites.
func buildPathParamFindings(
	v *Validator, c *metadata.ContractMeta, uuidParams []string, parsed map[string]struct{},
) []ValidationResult {
	var results []ValidationResult
	for _, paramName := range uuidParams {
		if _, ok := parsed[paramName]; !ok {
			results = append(results, v.newError(
				codeCH05, IssueRequired,
				c.File, fmt.Sprintf("endpoints.http.pathParams.%s", paramName),
				fmt.Sprintf(advHintCH05MissingParseCall, c.ID, paramName, paramName),
				advHintCH05MissingParseCallFix,
			))
		}
	}
	return results
}

// collectUUIDPathParams returns a sorted slice of path-param names that have
// format == "uuid" in the contract.
func collectUUIDPathParams(c *metadata.ContractMeta) []string {
	if c.Endpoints.HTTP == nil {
		return nil
	}
	var names []string
	for name, schema := range c.Endpoints.HTTP.PathParams {
		if schema.Format == "uuid" {
			names = append(names, name)
		}
	}
	// Sort for deterministic finding order across Go map iteration.
	sort.Strings(names)
	return names
}

// collectParsedUUIDParamNamesFromAST walks a single AST node (typically a
// function body) and returns the set of parameter names passed to
// httputil.ParseUUIDPathParam(w, r, "<name>") calls within it.
func collectParsedUUIDParamNamesFromAST(node ast.Node) map[string]struct{} {
	found := make(map[string]struct{})
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		collectParseUUIDCallName(call, found)
		return true
	})
	return found
}

// collectParseUUIDCallName inspects a single call expression and, if it is
// httputil.ParseUUIDPathParam(w, r, "<name>"), adds "<name>" to found.
func collectParseUUIDCallName(call *ast.CallExpr, found map[string]struct{}) {
	if !isParseUUIDPathParamCall(call) {
		return
	}
	if len(call.Args) < 3 {
		return
	}
	lit, ok := call.Args[2].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return
	}
	name := lit.Value
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		name = name[1 : len(name)-1]
	}
	found[name] = struct{}{}
}

// isParseUUIDPathParamCall returns true when call is
// httputil.ParseUUIDPathParam(...).
func isParseUUIDPathParamCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "httputil" && sel.Sel.Name == "ParseUUIDPathParam"
}

// =============================================================================
// CH-06 — typed response envelope alignment
// =============================================================================

// typedResponseStructPattern matches the generated typed response struct names
// produced by tools/codegen/contractgen/templates/types.tmpl. The status code
// is captured as the second-to-last 3-digit run before the suffix.
//
// Examples that match:
//
//   - Get200JSONResponse        → status 200
//   - Delete204NoContentResponse → status 204
//   - Get404ErrorResponse        → status 404
//   - HandleEnqueue201JSONResponse → status 201
//
// The leading run is the {HandlerMethod} (PascalCase, may contain digits if
// the contract is named that way), so the regex anchors the status as the
// 3-digit run immediately before the {Suffix} group.
//
// Correctness depends on archtest CODEGEN-CONTRACT-USER-OVERLAP-01
// (tools/archtest/codegen_contract_gen_test.go) which prevents hand-written
// .go files from landing under generated/contracts/. Without that guard a
// user-written DTO accidentally named e.g. `Foo200JSONResponse` would be
// counted as an "implemented" typed struct and could mask CH-06 orphan
// reports — the two rules together provide the closed-set guarantee.
var typedResponseStructPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*?(\d{3})(JSONResponse|NoContentResponse|ErrorResponse)$`)

// checkCH06 enforces CH-06: every HTTP contract that opts into codegen must
// have a typed response struct in its generated types_gen.go for every
// declared SuccessStatus + responses[] key, and no orphan structs may exist
// beyond the declared set.
//
// Skipped silently for:
//   - non-HTTP contracts (event/command/projection)
//   - codegen=false contracts (legacy hand-written handlers do not emit typed structs)
//   - missing types_gen.go (treated as codegen drift, surfaced by the verify pipeline)
func (v *Validator) checkCH06() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.sortedContracts() {
		// Honor cancellation between contracts (F2) — CH-06 parses each
		// contract's generated types_gen.go.
		if v.runCtx.Err() != nil {
			return results
		}
		if c.Kind != "http" || !c.Codegen {
			continue
		}
		results = append(results, v.checkTypedEnvelopeForContract(c, v.root)...)
	}
	return results
}

func (v *Validator) checkTypedEnvelopeForContract(
	c *metadata.ContractMeta, projectRoot string,
) []ValidationResult {
	typesPath := typedEnvelopeTypesGenPath(projectRoot, c.ID)

	implemented, ok := scanTypedResponseStructs(typesPath)
	if !ok {
		// types_gen.go absent or unparseable — codegen drift is a separate
		// concern surfaced by `gocell generate --verify`. Stay silent here
		// rather than double-reporting; CH-06 only owns the alignment check
		// when both sides exist.
		return nil
	}

	declared := typedEnvelopeDeclaredStatuses(c)

	var results []ValidationResult
	for _, status := range diffStatuses(declared, implemented) {
		results = append(results, v.newError(
			codeCH06, IssueRequired,
			c.File, fmt.Sprintf(fieldHTTPResponsesFmt, status),
			fmt.Sprintf("%s: contract declares status %d but generated types_gen.go has no matching typed response struct", c.ID, status),
			"run `gocell generate contract --all` to regenerate typed response structs",
		))
	}
	for _, status := range diffStatuses(implemented, declared) {
		results = append(results, v.newError(
			codeCH06, IssueRequired,
			c.File, fmt.Sprintf(fieldHTTPResponsesFmt, status),
			fmt.Sprintf("%s: generated types_gen.go has typed response struct for status %d "+
				"but contract.yaml does not declare it (orphan struct)", c.ID, status),
			fmt.Sprintf("add responses[%d] to contract.yaml or rerun `gocell generate contract --all`", status),
		))
	}
	return results
}

// typedEnvelopeDeclaredStatuses returns the union of SuccessStatus and
// responses[] keys declared on the HTTP endpoint. Auth.Responses (middleware-
// injected codes) are intentionally excluded — they are pre-service codes
// emitted by listener-mounted middleware and do not produce typed structs in
// types_gen.go (the generator only renders structs for entries in the IR
// Responses slice, which is built from SuccessStatus + responses[]).
func typedEnvelopeDeclaredStatuses(c *metadata.ContractMeta) map[int]struct{} {
	out := make(map[int]struct{})
	if c.Endpoints.HTTP == nil {
		return out
	}
	if c.Endpoints.HTTP.SuccessStatus > 0 {
		out[c.Endpoints.HTTP.SuccessStatus] = struct{}{}
	}
	for status := range c.Endpoints.HTTP.Responses {
		out[status] = struct{}{}
	}
	return out
}

// typedEnvelopeTypesGenPath resolves the absolute path to the generated
// types_gen.go file for the given contract. Path segments come from
// pkg/contractpath.Segments so CH-04/05 and CH-06 share the same internal→
// internalapi rewrite source-of-truth as contractgen / cellgen.
//
// Returns "" when the contract ID yields an unsafe path segment or the
// assembled path would escape projectRoot (F1); scanTypedResponseStructs treats
// "" as a missing file and CH-06 skips the contract.
func typedEnvelopeTypesGenPath(projectRoot, contractID string) string {
	segments := contractpath.Segments(contractID)
	dir, safe := safeJoinUnderRoot(projectRoot, append([]string{"generated", "contracts"}, segments...)...)
	if !safe {
		return ""
	}
	return filepath.Join(dir, "types_gen.go")
}

// scanTypedResponseStructs parses types_gen.go and returns the set of HTTP
// status codes encoded in the typed response struct names declared at the
// package level. The second return is false when the file is absent or fails
// to parse, signaling that the alignment check should fall through silently
// (codegen drift is a separate concern owned by `gocell generate --verify`).
func scanTypedResponseStructs(typesPath string) (map[int]struct{}, bool) {
	if _, err := os.Stat(typesPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false
		}
		return nil, false
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, typesPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	out := map[int]struct{}{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		collectTypedResponseStatuses(gen.Specs, out)
	}
	return out, true
}

// collectTypedResponseStatuses appends every typed-response status code found
// in specs into out. Extracted from scanTypedResponseStructs to keep cognitive
// complexity below the package's 15-branch ceiling.
func collectTypedResponseStatuses(specs []ast.Spec, out map[int]struct{}) {
	for _, spec := range specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		m := typedResponseStructPattern.FindStringSubmatch(ts.Name.Name)
		if m == nil {
			continue
		}
		status, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			continue
		}
		out[status] = struct{}{}
	}
}
