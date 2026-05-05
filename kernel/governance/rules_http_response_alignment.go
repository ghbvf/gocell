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
	"strings"

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
// errcode flow through WriteError. Static handler-only AST analysis
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

// httpHelperWritesStatuses maps pkg/httputil helper names that write HTTP error
// responses internally to the set of ≥400 status codes they may emit.
// Helpers whose status is explicit through errcode.Kind (WritePublic) are
// handled separately by collectWritePublicKind.
var httpHelperWritesStatuses = map[string][]int{
	"WriteError":             {},
	"DecodeJSON":             {http.StatusBadRequest, http.StatusRequestEntityTooLarge},
	"DecodeJSONStrict":       {http.StatusBadRequest, http.StatusRequestEntityTooLarge},
	"ParsePageParams":        {http.StatusBadRequest},
	"ParseUUIDPathParam":     {http.StatusBadRequest},
	"ParsePageParamsOrWrite": {http.StatusBadRequest},
}

var errcodeKindNameToStatus = map[string]int{
	"KindInternal":         errcode.KindInternal.Status(),
	"KindInvalid":          errcode.KindInvalid.Status(),
	"KindUnauthenticated":  errcode.KindUnauthenticated.Status(),
	"KindPermissionDenied": errcode.KindPermissionDenied.Status(),
	"KindNotFound":         errcode.KindNotFound.Status(),
	"KindConflict":         errcode.KindConflict.Status(),
	"KindGone":             errcode.KindGone.Status(),
	"KindPayloadTooLarge":  errcode.KindPayloadTooLarge.Status(),
	"KindRateLimited":      errcode.KindRateLimited.Status(),
	"KindClientClosed":     errcode.KindClientClosed.Status(),
	"KindDeadlineExceeded": errcode.KindDeadlineExceeded.Status(),
	"KindUnavailable":      errcode.KindUnavailable.Status(),
	"KindNotImplemented":   errcode.KindNotImplemented.Status(),
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

// declaredErrorStatuses returns the union of 4xx/5xx status codes declared in
// the contract's responses map and in auth.responses. The dual source allows
// middleware-injected codes (e.g. bootstrap auth 401, rate limiter 429) to be
// declared under auth.responses without requiring handler AST emission (CH-04
// double-source rule).
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
		// "http.order.create.v1" → ["http","order","create","v1"]
		segments := strings.Split(contractID, ".")
		pkgParts := append([]string{projectRoot, "generated", "contracts"}, segments...)
		handlerPath := filepath.Join(append(pkgParts, "handler_gen.go")...)
		if _, err := os.Stat(handlerPath); err == nil {
			return handlerPath
		}
		return ""
	}

	// Legacy: hand-written slice/handler.go.
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
	// allCodes is the union of all ≥400 status codes in the file. Retained
	// for potential future diagnostic use; not used for rule enforcement
	// (correlation is now required via auth.Mount).
	allCodes map[int]struct{}
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
func extractHandlerStatusCodesForContract(filename, contractID string, cache map[string]*parsedHandlerFile) (map[int]struct{}, error) {
	ph, err := parseHandlerFile(filename, cache)
	if err != nil {
		return nil, err
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
func extractFromGeneratedHandler(ph *parsedHandlerFile, contractID string) (map[int]struct{}, error) {
	// Verify that the contractSpec var in this file actually matches contractID.
	found := false
	for _, id := range ph.specVarToID {
		if id == contractID {
			found = true
			break
		}
	}
	if !found {
		return nil, errCorrelationMissing
	}

	// The generated handler's logic lives in "Handler.handle" (unexported).
	// ServeHTTP just calls h.handle(w, r).
	const generatedHandleKey = "Handler.handle"
	body, ok := ph.funcBodies[generatedHandleKey]
	if !ok {
		// Fall back to scanning the whole-file codes if the method key is missing
		// (e.g. future generator changes the method name). Fail-closed: if the
		// file has no codes at all, return errCorrelationMissing.
		if len(ph.allCodes) == 0 {
			return nil, errCorrelationMissing
		}
		out := make(map[int]struct{}, len(ph.allCodes))
		for k, v := range ph.allCodes {
			out[k] = v
		}
		return out, nil
	}
	codes := make(map[int]struct{})
	collectStatusCodesFromNode(body, codes)
	return codes, nil
}

// extractFromLegacyHandler extracts status codes for contractID from a legacy
// hand-written handler.go using auth.Mount correlation.
func extractFromLegacyHandler(ph *parsedHandlerFile, contractID string) (map[int]struct{}, error) {
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
		allCodes:        make(map[int]struct{}),
	}

	// Detect generated file via the standard gocell codegen header comment.
	// The generated file's first comment group contains the DO NOT EDIT marker.
	ph.generated = isGoCellGeneratedFile(f)

	// Pass 1: collect spec var declarations (var specFoo = wrapper.ContractSpec{ID: "..."}).
	collectSpecVarIDs(f, ph.specVarToID)

	// Pass 2: collect function declarations and whole-file status codes.
	collectFuncBodies(f, ph.funcBodies, ph.allCodes)

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
// "Handler.handle") can be looked up. allCodes receives every ≥400 status
// code found in any function body.
func collectFuncBodies(f *ast.File, funcBodies map[string]ast.Node, allCodes map[int]struct{}) {
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
		collectStatusCodesFromNode(fn.Body, allCodes)
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
		collectErrcodeKinds(call, out)
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

// collectErrcodeKinds looks for errcode.KindXxx values inside errcode.New/Wrap
// calls and maps them through errcode.Kind.Status. Code names are deliberately
// ignored: runtime status is Kind-derived, so CH-04 must not reintroduce a
// second code-name status table.
func collectErrcodeKinds(call *ast.CallExpr, out map[int]struct{}) {
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, ok := fun.X.(*ast.Ident)
	if !ok || pkg.Name != "errcode" {
		return
	}
	switch fun.Sel.Name {
	case "New", "Wrap":
	default:
		return
	}
	kindArg := errcodeKindArg(call.Args)
	status, found := errcodeKindStatus(kindArg)
	if !found {
		slog.Warn("CH-04: errcode constructor without static Kind selector, skipping alignment check",
			slog.String("constructor", fun.Sel.Name))
		return
	}
	if status >= 400 {
		out[status] = struct{}{}
	}
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
	if helperName == "WritePublic" {
		collectWritePublicKind(call, out)
		return
	}
	statuses, known := httpHelperWritesStatuses[helperName]
	if !known {
		// Not every httputil function writes a response. Only warn for names
		// that are not in a well-known "does not write status" allowlist.
		// Presence in the map (regardless of value) suppresses the warning;
		// absence means a genuinely unknown helper that may write status.
		knownNonWriters := map[string]struct{}{
			"WriteJSON":                       {}, // writes, but caller supplies the status — already caught by collectHTTPStatusSelectors
			"DecodeJSON":                      {},
			"DecodeJSONStrict":                {}, // strict variant; same semantics as DecodeJSON
			"WithClientErrorLogSampling":      {}, // logging decorator — no status write
			"WithClientErrorLogSamplingEvery": {}, // logging decorator — no status write
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

func collectWritePublicKind(call *ast.CallExpr, out map[int]struct{}) {
	if len(call.Args) < 3 {
		slog.Warn("CH-04: httputil.WritePublic without Kind argument, skipping alignment check")
		return
	}
	status, found := errcodeKindStatus(call.Args[2])
	if !found {
		slog.Warn("CH-04: httputil.WritePublic without static Kind selector, skipping alignment check")
		return
	}
	if status >= 400 {
		out[status] = struct{}{}
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
