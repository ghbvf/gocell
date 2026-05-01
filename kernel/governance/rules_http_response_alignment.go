package governance

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
	"sort"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// errCorrelationMissing is returned by extractHandlerStatusCodesForContract
// when no auth.Mount call in the handler file maps the given contractID to a
// handler function. Callers that receive this error must emit a fail-closed
// finding rather than silently skipping the contract.
var errCorrelationMissing = errors.New("auth.Mount correlation missing")

// CodeContractHealthResponseAlignment is the rule code for CH-04 — emitted as
// SeverityError when a handler returns a 4xx/5xx status code that the contract
// does not declare in its responses map. The "extra declaration" inverse
// (contract declares N but handler never returns it) is intentionally NOT
// reported: status codes can be emitted by listener auth middleware (401/403),
// rate-limit middleware (429), framework error paths (5xx), or service-layer
// errcode flow through WriteDomainError. Static handler-only AST analysis
// cannot see those paths, and reporting them as "unused" would generate
// systematic false positives that drown out genuine missing declarations.
const CodeContractHealthResponseAlignment = "CH-04"

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

// errcodeNameToStatus maps errcode constant short names (e.g. "ErrAuthUserNotFound")
// to HTTP status codes via httputil.MapCodeToStatus. Hand-curated to cover every
// errcode constant that handlers in this repo return as a 4xx/5xx response — when
// adding a new errcode constant used in a handler, extend this table. The rule
// logs a Debug message for unknown names so gaps are visible at analysis time
// without failing the build.
var errcodeNameToStatus = func() map[string]int {
	pairs := []struct {
		name string
		code errcode.Code
	}{
		{"ErrAuthUserNotFound", errcode.ErrAuthUserNotFound},
		{"ErrAuthUserDuplicate", errcode.ErrAuthUserDuplicate},
		{"ErrAuthRoleNotFound", errcode.ErrAuthRoleNotFound},
		{"ErrAuthRoleDuplicate", errcode.ErrAuthRoleDuplicate},
		{"ErrAuthSelfDelete", errcode.ErrAuthSelfDelete},
		{"ErrAuthInvalidInput", errcode.ErrAuthInvalidInput},
		{"ErrAuthIdentityInvalidInput", errcode.ErrAuthIdentityInvalidInput},
		{"ErrAuthRBACInvalidInput", errcode.ErrAuthRBACInvalidInput},
		{"ErrAuthSessionInvalidInput", errcode.ErrAuthSessionInvalidInput},
		{"ErrAuthLogoutInvalidInput", errcode.ErrAuthLogoutInvalidInput},
		{"ErrAuthRefreshInvalidInput", errcode.ErrAuthRefreshInvalidInput},
		{"ErrAuthLoginInvalidInput", errcode.ErrAuthLoginInvalidInput},
		{"ErrAuthLoginFailed", errcode.ErrAuthLoginFailed},
		{"ErrAuthRefreshFailed", errcode.ErrAuthRefreshFailed},
		{"ErrAuthRefreshUnavailable", errcode.ErrAuthRefreshUnavailable},
		{"ErrAuthForbidden", errcode.ErrAuthForbidden},
		{"ErrAuthInvalidToken", errcode.ErrAuthInvalidToken},
		{"ErrAuthTokenInvalid", errcode.ErrAuthTokenInvalid},
		{"ErrAuthTokenExpired", errcode.ErrAuthTokenExpired},
		{"ErrAuthInvalidTokenIntent", errcode.ErrAuthInvalidTokenIntent},
		{"ErrAuthUnauthorized", errcode.ErrAuthUnauthorized},
		{"ErrAuthPasswordResetRequired", errcode.ErrAuthPasswordResetRequired},
		{"ErrAuthUserLocked", errcode.ErrAuthUserLocked},
		{"ErrAuthKeyInvalid", errcode.ErrAuthKeyInvalid},
		{"ErrAuthKeyMissing", errcode.ErrAuthKeyMissing},
		{"ErrAuthRoleFetchFailed", errcode.ErrAuthRoleFetchFailed},
		{"ErrAuthVerifierConfig", errcode.ErrAuthVerifierConfig},
		{"ErrAuthReplayDetected", errcode.ErrAuthReplayDetected},
		{"ErrSessionNotFound", errcode.ErrSessionNotFound},
		{"ErrSessionConflict", errcode.ErrSessionConflict},
		{"ErrValidationFailed", errcode.ErrValidationFailed},
		{"ErrValidationInvalidUUID", errcode.ErrValidationInvalidUUID},
		{"ErrCellNotFound", errcode.ErrCellNotFound},
		{"ErrSliceNotFound", errcode.ErrSliceNotFound},
		{"ErrContractNotFound", errcode.ErrContractNotFound},
		{"ErrAssemblyNotFound", errcode.ErrAssemblyNotFound},
		{"ErrOrderNotFound", errcode.ErrOrderNotFound},
		{"ErrDeviceNotFound", errcode.ErrDeviceNotFound},
		{"ErrCommandNotFound", errcode.ErrCommandNotFound},
		{"ErrConfigNotFound", errcode.ErrConfigNotFound},
		{"ErrConfigDuplicate", errcode.ErrConfigDuplicate},
		{"ErrConfigInvalidInput", errcode.ErrConfigInvalidInput},
		{"ErrConfigPublishInvalidInput", errcode.ErrConfigPublishInvalidInput},
		{"ErrConfigRepoNotFound", errcode.ErrConfigRepoNotFound},
		{"ErrConfigRepoDuplicate", errcode.ErrConfigRepoDuplicate},
		{"ErrConfigRepoQuery", errcode.ErrConfigRepoQuery},
		{"ErrFlagNotFound", errcode.ErrFlagNotFound},
		{"ErrFlagDuplicate", errcode.ErrFlagDuplicate},
		{"ErrFlagInvalidInput", errcode.ErrFlagInvalidInput},
		{"ErrFlagRepoQuery", errcode.ErrFlagRepoQuery},
		{"ErrAuditRepoNotFound", errcode.ErrAuditRepoNotFound},
		{"ErrAuditRepoQuery", errcode.ErrAuditRepoQuery},
		{"ErrArchiveUpload", errcode.ErrArchiveUpload},
		{"ErrArchiveMarshal", errcode.ErrArchiveMarshal},
		{"ErrInternal", errcode.ErrInternal},
		{"ErrNotImplemented", errcode.ErrNotImplemented},
		{"ErrRateLimited", errcode.ErrRateLimited},
		{"ErrCSRFOriginDenied", errcode.ErrCSRFOriginDenied},
		{"ErrBodyTooLarge", errcode.ErrBodyTooLarge},
		{"ErrCursorInvalid", errcode.ErrCursorInvalid},
		{"ErrPageSizeExceeded", errcode.ErrPageSizeExceeded},
		{"ErrInvalidTimeFormat", errcode.ErrInvalidTimeFormat},
		{"ErrSetupAlreadyInitialized", errcode.ErrSetupAlreadyInitialized},
		{"ErrDistlockTimeout", errcode.ErrDistlockTimeout},
		{"ErrClientCanceled", errcode.ErrClientCanceled},
		{"ErrServerTimeout", errcode.ErrServerTimeout},
		{"ErrReadyzVerboseDenied", errcode.ErrReadyzVerboseDenied},
		{"ErrNonceStoreFull", errcode.ErrNonceStoreFull},
		{"ErrKeyProviderKeyNotFound", errcode.ErrKeyProviderKeyNotFound},
		{"ErrKeyProviderAuthFailed", errcode.ErrKeyProviderAuthFailed},
		{"ErrKeyProviderEncryptFailed", errcode.ErrKeyProviderEncryptFailed},
		{"ErrKeyProviderDecryptFailed", errcode.ErrKeyProviderDecryptFailed},
		{"ErrKeyProviderRotateFailed", errcode.ErrKeyProviderRotateFailed},
		{"ErrKeyProviderTransient", errcode.ErrKeyProviderTransient},
		{"ErrConfigDecryptFailed", errcode.ErrConfigDecryptFailed},
		{"ErrConfigEncryptFailed", errcode.ErrConfigEncryptFailed},
		{"ErrConfigKeyMissing", errcode.ErrConfigKeyMissing},
		{"ErrVaultAuthFailed", errcode.ErrVaultAuthFailed},
	}
	m := make(map[string]int, len(pairs))
	for _, p := range pairs {
		status := errcode.MapCodeToStatus(p.code)
		if status >= 400 {
			m[p.name] = status
		}
	}
	return m
}()

// httpHelperWritesStatuses maps pkg/httputil helper names that write HTTP error
// responses internally to the set of ≥400 status codes they may emit.
// Hand-curated because the helpers don't expose status as parameters — handler
// AST sees only the call site. When a new helper that writes responses is
// added, register it here or CH-04 silently skips alignment for that call site
// (slog.Warn at scan time alerts the developer).
var httpHelperWritesStatuses = map[string][]int{
	"WriteDecodeError":       {http.StatusBadRequest, http.StatusRequestEntityTooLarge},
	"ParseUUIDPathParam":     {http.StatusBadRequest},
	"ParsePageParamsOrWrite": {http.StatusBadRequest},
}

// CheckHTTPResponseAlignment enforces CH-04: every 4xx/5xx status code that a
// handler can return must be declared in the corresponding contract's responses
// map.
//
// Contracts without a matching in-repo handler (e.g. external actor) are
// silently skipped. When multiple contracts share a handler.go file, the rule
// uses auth.Mount correlation to narrow scanning to the specific handler
// function linked to each contract.
func (v *Validator) CheckHTTPResponseAlignment(contracts []*metadata.ContractMeta, projectRoot string) []ValidationResult {
	// Parse cache is per CheckHTTPResponseAlignment call to avoid cross-test contamination.
	cache := map[string]*parsedHandlerFile{}
	var results []ValidationResult
	for _, c := range contracts {
		if c.Kind != "http" {
			continue
		}
		results = append(results, v.checkResponseAlignmentForContract(c, projectRoot, cache)...)
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

	handlerCodes, err := extractHandlerStatusCodesForContract(handlerFile, c.ID, cache)
	if err != nil {
		if errors.Is(err, errCorrelationMissing) {
			return []ValidationResult{v.newResult(
				CodeContractHealthResponseAlignment, SeverityError, IssueRequired,
				c.File, "endpoints.http.path",
				fmt.Sprintf(advHintCH04CorrelationFailed, c.ID, handlerFile),
			)}
		}
		slog.Debug("CH-04: failed to parse handler AST",
			slog.String("contract", c.ID),
			slog.String("file", handlerFile),
			slog.Any("error", err))
		return nil
	}

	declared := declaredErrorStatuses(c)
	return buildAlignmentFindings(v, c, handlerCodes, declared)
}

// declaredErrorStatuses returns the set of 4xx/5xx status codes declared in
// the contract's responses map.
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
	return out
}

// buildAlignmentFindings compares handler-observed codes vs contract-declared
// codes and emits CH-04 (missing) findings. Extra declarations are not
// reported — see CodeContractHealthResponseAlignment doc for rationale.
func buildAlignmentFindings(v *Validator, c *metadata.ContractMeta, observed, declared map[int]struct{}) []ValidationResult {
	var results []ValidationResult
	for _, status := range diffStatuses(observed, declared) {
		results = append(results, v.newResult(
			CodeContractHealthResponseAlignment, SeverityError, IssueRequired,
			c.File, fmt.Sprintf("endpoints.http.responses[%d]", status),
			fmt.Sprintf("%s: handler returns %d but contract does not declare it", c.ID, status),
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

// findHandlerFile resolves the handler.go path for the slice serving contractID.
// Returns "" if no slice declares a "serve" role for this contract or if the
// handler.go file does not exist on disk. The convention: each cell/slice has
// at most one handler.go that serves all its HTTP routes.
func findHandlerFile(project *metadata.ProjectMeta, contractID, projectRoot string) string {
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

// parsedHandlerFile caches the AST and derived data for a handler.go.
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
	funcBodies map[string]ast.Node
	// allCodes is the union of all ≥400 status codes in the file. Retained
	// for potential future diagnostic use; not used for rule enforcement
	// (correlation is now required via auth.Mount).
	allCodes map[int]struct{}
}

// extractHandlerStatusCodesForContract returns the ≥400 status codes that the
// handler file can return for contractID. Uses function-level narrowing via
// auth.Mount correlation. Returns errCorrelationMissing when no auth.Mount
// call in the file maps contractID to a handler function — callers must treat
// this as a fail-closed condition and emit an error finding.
func extractHandlerStatusCodesForContract(filename, contractID string, cache map[string]*parsedHandlerFile) (map[int]struct{}, error) {
	ph, err := parseHandlerFile(filename, cache)
	if err != nil {
		return nil, err
	}

	if fnName, ok := ph.contractToFuncs[contractID]; ok {
		if body, ok := ph.funcBodies[fnName]; ok {
			codes := make(map[int]struct{})
			collectStatusCodesFromNode(body, codes)
			return codes, nil
		}
	}
	return nil, errCorrelationMissing
}

// parseHandlerFile parses filename and extracts the per-contract function
// mapping and status codes. Results are stored in cache to avoid re-parsing
// the same file for multiple contracts.
func parseHandlerFile(filename string, cache map[string]*parsedHandlerFile) (*parsedHandlerFile, error) {
	if ph, ok := cache[filename]; ok {
		return ph, nil
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	ph := &parsedHandlerFile{
		specVarToID:     make(map[string]string),
		contractToFuncs: make(map[string]string),
		funcBodies:      make(map[string]ast.Node),
		allCodes:        make(map[int]struct{}),
	}

	// Pass 1: collect spec var declarations (var specFoo = wrapper.ContractSpec{ID: "..."}).
	collectSpecVarIDs(f, ph.specVarToID)

	// Pass 2: collect function declarations and whole-file status codes.
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ph.funcBodies[fn.Name.Name] = fn.Body
		collectStatusCodesFromNode(fn.Body, ph.allCodes)
	}

	// Pass 3: correlate auth.Mount calls to contract ID + handler function name.
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isAuthMountCall(call) {
			return true
		}
		cID, fnName := extractAuthMountContractAndHandler(call, ph.specVarToID)
		if cID != "" && fnName != "" {
			ph.contractToFuncs[cID] = fnName
		}
		return true
	})

	cache[filename] = ph
	return ph, nil
}

// collectSpecVarIDs scans package-level var declarations for ContractSpec
// composite literals and builds a map from var name to the ID string.
//
// Handles two common forms:
//
//	var specFoo = wrapper.ContractSpec{ID: "http.x.v1", ...}
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
// that has an "ID" key field (e.g. wrapper.ContractSpec{ID: "http.x.v1"}).
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
// a wrapper.ContractSpec literal; the governance scanner correlates the
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
// encountered in call arguments to out.
func collectStatusCodesFromNode(node ast.Node, out map[int]struct{}) {
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		collectHTTPStatusSelectors(call, out)
		collectErrcodeConstants(call, out)
		collectHelperWriteStatuses(call, out)
		return true
	})
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

// collectErrcodeConstants looks for errcode.ErrXxx constants inside known
// errcode constructor calls (New, NewDomain, NewInfra, Safe, Wrap) and maps
// them to HTTP status codes. Only ≥400 codes are added to out.
func collectErrcodeConstants(call *ast.CallExpr, out map[int]struct{}) {
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, ok := fun.X.(*ast.Ident)
	if !ok || pkg.Name != "errcode" {
		return
	}
	switch fun.Sel.Name {
	case "New", "NewDomain", "NewInfra", "Safe", "Wrap":
	default:
		return
	}
	if len(call.Args) == 0 {
		return
	}
	arg, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok {
		return
	}
	argPkg, ok := arg.X.(*ast.Ident)
	if !ok || argPkg.Name != "errcode" {
		return
	}
	name := arg.Sel.Name
	status, found := errcodeNameToStatus[name]
	if !found {
		// When handlers start using a new errcode constant, register it in the
		// errcodeNameToStatus pairs slice above, or CH-04 silently skips
		// alignment checks for that constant's status code.
		slog.Warn("CH-04: unknown errcode constant in handler, skipping alignment check",
			slog.String("constant", name))
		return
	}
	if status >= 400 {
		out[status] = struct{}{}
	}
}

// collectHelperWriteStatuses detects httputil.<HelperName>(w, ...) calls and
// adds the status codes the helper is known to write internally to out.
// Uses httpHelperWritesStatuses for the known-helper lookup.
//
// Helpers that write responses without accepting a status code parameter are
// invisible to collectHTTPStatusSelectors, so this table bridges that gap for
// CH-04. Unknown httputil calls emit a slog.Warn so new helpers are not silently
// skipped.
func collectHelperWriteStatuses(call *ast.CallExpr, out map[int]struct{}) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "httputil" {
		return
	}
	helperName := sel.Sel.Name
	statuses, known := httpHelperWritesStatuses[helperName]
	if !known {
		// Not every httputil function writes a response. Only warn for names
		// that are not in a well-known "does not write status" allowlist.
		// Presence in the map (regardless of value) suppresses the warning;
		// absence means a genuinely unknown helper that may write status.
		knownNonWriters := map[string]struct{}{
			"WriteJSON":        {}, // writes, but caller supplies the status — already caught by collectHTTPStatusSelectors
			"WriteError":       {}, // caller supplies status
			"WritePublicError": {}, // caller supplies status
			"WriteDomainError": {}, // status derived from errcode mapping — already caught by collectErrcodeConstants
			"MapCodeToStatus":  {},
			"IsClientError":    {},
			"DecodeJSON":       {},
			"DecodeJSONStrict": {}, // strict variant; same semantics as DecodeJSON
		}
		if _, suppressed := knownNonWriters[helperName]; !suppressed {
			slog.Warn("CH-04: unknown httputil helper call, skipping helper-status inference",
				slog.String("helper", helperName))
		}
		return
	}
	for _, s := range statuses {
		if s >= 400 {
			out[s] = struct{}{}
		}
	}
}

// stripQuotes removes the enclosing double-quote characters from a Go string
// literal value returned by the AST (e.g. `"http.x.v1"` → `http.x.v1`).
func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
