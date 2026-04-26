package governance

// Rule CONTRACT-CONSISTENCY-EMIT-01 validates bidirectional alignment between
// HTTP contract triggers and outbox.Emit calls in the owning cell's service files.
//
// Three constraints:
//  1. L2+ HTTP contract without triggers → Error (triggers required)
//  2. Triggers present but consistencyLevel ∈ {L0, L1} → Error (level mismatch)
//  3. Bidirectional AST check: every trigger must appear in a resolvable emit
//     call somewhere in the cell's slice service files, and every resolvable
//     emit call topic must be covered by a trigger in some HTTP contract of the
//     same cell.
//
// ref: tools/archtest/outbox_service_test.go — same-package const propagation
// ref: golang.org/x/tools/go/analysis/passes/nilness rule discipline
// Stdlib go/ast only — kernel/ cannot import golang.org/x/tools/go/packages.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

const codeContractConsistencyEmit01 = "CONTRACT-CONSISTENCY-EMIT-01"

// pkgConstMap maps constName → stringValue for a single package.
type pkgConstMap = map[string]string

// cellPkgConsts maps pkgIdent ("dto" or "domain") → pkgConstMap.
type cellPkgConsts = map[string]pkgConstMap

// validateCONTRACTCONSISTENCYEMIT01 checks three consistency constraints
// between HTTP contract triggers and outbox emit calls in service files.
// Contracts under examples/ are excluded — they are illustrative and may
// not conform to the full governance model.
//
// Structured as two phases:
//
//  1. Per-cell phase: scan emit topics and run reverse-check unconditionally for
//     every cell that has HTTP contracts. Also discovers cells that have no HTTP
//     contracts but do emit — their topics must not be undeclared.
//
//  2. Per-contract phase: run constraint-1/2 checks and forward-check (every
//     declared trigger must appear in the cell's emit set).
//
// The per-cell phase runs before the per-contract loop so that reverse-check is
// never gated on a contract having non-empty triggers.
func (v *Validator) validateCONTRACTCONSISTENCYEMIT01() []ValidationResult {
	cellTriggerSets := buildCellTriggerSets(v.project.Contracts)
	cellEmitSets, perCellResults := v.runPerCellPhase(cellTriggerSets)
	perContractResults := v.runPerContractPhase(cellEmitSets)
	return append(perCellResults, perContractResults...)
}

// runPerCellPhase scans emit topics and runs reverse-checks for all cells with
// HTTP contracts that have declared triggers. Returns the emit-set cache and findings.
//
// Reverse-check only runs for cells that have at least one HTTP contract with
// non-empty triggers (L2+ contract). A cell whose only HTTP contracts are L0/L1
// with no triggers is typically event-driven: its emits come from event-subscription
// handlers, not from HTTP endpoints, and those emits do not need HTTP trigger
// declarations. Skipping reverse-check for such cells avoids false positives
// without weakening the forward-check (which still runs per-contract in phase 2).
func (v *Validator) runPerCellPhase(cellTriggerSets map[string]map[string]struct{}) (
	map[string]map[string]struct{}, []ValidationResult,
) {
	var results []ValidationResult
	cellEmitSets := map[string]map[string]struct{}{}

	for ownerCell, triggerSet := range cellTriggerSets {
		emits, scanResults := scanCellEmitTopics(v.root, ownerCell, "")
		results = append(results, scanResults...)
		cellEmitSets[ownerCell] = emits
		if len(triggerSet) == 0 {
			continue // event-driven cell — no HTTP trigger declarations needed
		}
		results = append(results, v.checkReverseEmits(ownerCell, emits, triggerSet)...)
	}

	// Scan cells that have NO HTTP contracts at all but do emit — every emit is unaccounted.
	for _, ownerCell := range discoverEmittingCellsWithoutHTTPContracts(v.root, cellTriggerSets) {
		emits, scanResults := scanCellEmitTopics(v.root, ownerCell, "")
		results = append(results, scanResults...)
		results = append(results, v.checkReverseEmits(ownerCell, emits, nil)...)
	}
	return cellEmitSets, results
}

// runPerContractPhase runs constraint-1/2 and forward-check for each HTTP contract.
func (v *Validator) runPerContractPhase(cellEmitSets map[string]map[string]struct{}) []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if cell.ContractKind(c.Kind) != cell.ContractHTTP {
			continue
		}
		if isExamplePath(c.File) || isExamplePath(c.Dir) {
			continue
		}
		r, skip := v.checkConsistencyConstraints12(c)
		results = append(results, r...)
		if skip || len(c.Triggers) == 0 || !isL2OrHigher(c.ConsistencyLevel) {
			continue
		}
		results = append(results, v.checkForwardTriggers(c, cellEmitSets[c.OwnerCell])...)
	}
	return results
}

// isExamplePath returns true if the path is under an examples/ subtree.
func isExamplePath(p string) bool {
	return strings.HasPrefix(p, "examples/")
}

// buildCellTriggerSets pre-computes per-cell declared triggers for all HTTP contracts.
// Returns a map of ownerCell → set of declared trigger topic strings.
func buildCellTriggerSets(contracts map[string]*metadata.ContractMeta) map[string]map[string]struct{} {
	sets := map[string]map[string]struct{}{}
	for _, c := range contracts {
		if cell.ContractKind(c.Kind) != cell.ContractHTTP {
			continue
		}
		if isExamplePath(c.File) || isExamplePath(c.Dir) {
			continue
		}
		if sets[c.OwnerCell] == nil {
			sets[c.OwnerCell] = map[string]struct{}{}
		}
		for _, t := range c.Triggers {
			sets[c.OwnerCell][t] = struct{}{}
		}
	}
	return sets
}

// discoverEmittingCellsWithoutHTTPContracts walks cells/ to find cells that have
// no HTTP contracts (not in knownCells) but contain outbox.Emit or receiver .Emit
// calls. Only non-example cells are considered.
func discoverEmittingCellsWithoutHTTPContracts(root string, knownCells map[string]map[string]struct{}) []string {
	cellsDir := filepath.Join(root, "cells")
	entries, err := os.ReadDir(cellsDir)
	if err != nil {
		return nil
	}
	var extras []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, known := knownCells[name]; known {
			continue
		}
		// Skip cells under examples/ paths (cell dir is directly under cells/).
		slicesDir := filepath.Join(cellsDir, name, "slices")
		if _, statErr := os.Stat(slicesDir); statErr != nil {
			continue
		}
		if cellHasAnyEmit(slicesDir) {
			extras = append(extras, name)
		}
	}
	return extras
}

// cellHasAnyEmit does a quick scan of all non-test Go files under slicesDir to
// check whether any file contains an outbox.Emit or receiver .Emit call.
func cellHasAnyEmit(slicesDir string) bool {
	found := false
	fset := token.NewFileSet()
	_ = filepath.WalkDir(slicesDir, func(path string, d fs.DirEntry, err error) error {
		if found || err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}
		if fileHasEmitCall(f) {
			found = true
		}
		return nil
	})
	return found
}

// fileHasEmitCall returns true if the file's AST contains any outbox.Emit or
// receiver .Emit call site.
func fileHasEmitCall(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if (isOutboxEmitCall(call) && len(call.Args) >= 3) ||
			(isReceiverEmitCall(call) && len(call.Args) >= 2) {
			found = true
			return false
		}
		return true
	})
	return found
}

// checkConsistencyConstraints12 validates constraints 1 and 2 for a contract.
// Returns results and whether the caller should skip further checks for this contract.
func (v *Validator) checkConsistencyConstraints12(c *metadata.ContractMeta) ([]ValidationResult, bool) {
	// Constraint 1: L2+ HTTP contract must have non-empty triggers.
	if isL2OrHigher(c.ConsistencyLevel) && len(c.Triggers) == 0 {
		return []ValidationResult{v.newResult(
			codeContractConsistencyEmit01, SeverityError, IssueRequired,
			contractFile(c), "triggers",
			fmt.Sprintf("contract %q: L2+ HTTP contract must declare non-empty triggers (matches outbox.Emit topics)", c.ID),
		)}, true
	}
	// Constraint 2: triggers present but level ∈ {L0, L1}.
	if len(c.Triggers) > 0 && !isL2OrHigher(c.ConsistencyLevel) {
		return []ValidationResult{v.newResult(
			codeContractConsistencyEmit01, SeverityError, IssueMismatch,
			contractFile(c), "triggers",
			fmt.Sprintf("contract %q declares triggers but consistencyLevel=%s; triggers imply L2+", c.ID, c.ConsistencyLevel),
		)}, true
	}
	return nil, false
}

// checkForwardTriggers validates that each contract trigger appears in emitTopics.
func (v *Validator) checkForwardTriggers(c *metadata.ContractMeta, emitTopics map[string]struct{}) []ValidationResult {
	var results []ValidationResult
	for _, t := range c.Triggers {
		if _, found := emitTopics[t]; !found {
			results = append(results, v.newResult(
				codeContractConsistencyEmit01, SeverityError, IssueRefNotFound,
				contractFile(c), "triggers",
				fmt.Sprintf("contract %q declares trigger %q but no non-test Go file under cells/%s/slices/ emits it via outbox.Emit or *.Emitter.Emit; topic must be string literal or named constant (dynamic fmt.Sprintf rejected)", c.ID, t, c.OwnerCell),
			))
		}
	}
	return results
}

// checkReverseEmits validates that each emitted topic appears in declared triggers.
// declared may be nil when a cell has no HTTP contracts; in that case every emit fails.
func (v *Validator) checkReverseEmits(
	ownerCell string,
	emitTopics map[string]struct{},
	declared map[string]struct{},
) []ValidationResult {
	var results []ValidationResult
	for t := range emitTopics {
		if _, found := declared[t]; !found {
			results = append(results, v.newScopedResult(
				codeContractConsistencyEmit01, SeverityError, IssueRefNotFound,
				"project", "triggers",
				fmt.Sprintf("service emits topic %q but no HTTP contract in cell %s declares it in triggers; fix: add %q to triggers in one of cells/%s's HTTP contract.yaml files (or change the emit if dead code)", t, ownerCell, t, ownerCell),
			))
		}
	}
	return results
}

// scanCellEmitTopics scans all non-test Go files under cells/<ownerCell>/slices/
// and returns the set of resolvable topic strings emitted by the cell.
//
// Resolution strategy (in order of precision):
//  1. outbox.Emit(ctx, emitter, TOPIC, ...) — third arg resolved if literal or const;
//     call expressions → dynamic-topic error.
//  2. EventType: EXPR in outbox.Entry composite literals — resolved if literal or const.
//  3. Pre-assigned entry variable passed to receiver Emit — walk back assignments.
//  4. Same-package helper: if a function calls helper(ctx, dto.TopicX) and helper's body
//     emits via outbox.Emit(ctx, e, topicParam, ...) where topicParam is the matching
//     parameter, resolve via the call-site argument expression.
//
// Topic selectors in subscriber/comparison contexts are NOT collected — only
// direct emit call evidence counts.
func scanCellEmitTopics(root, ownerCell, fileForError string) (map[string]struct{}, []ValidationResult) {
	topics := map[string]struct{}{}
	var results []ValidationResult

	cellDir := filepath.Join(root, "cells", ownerCell)
	pkgConsts := buildPkgConsts(cellDir)
	slicesDir := filepath.Join(cellDir, "slices")
	if _, err := os.Stat(slicesDir); err != nil {
		return topics, results
	}

	fset := token.NewFileSet()

	// Two-pass approach: first collect all function declarations across all files
	// so that helper resolution can reference them, then scan for emits.
	allFiles := collectParsedFiles(fset, slicesDir)
	helperMap := buildHelperEmitMap(allFiles, pkgConsts)

	for _, f := range allFiles {
		scanResults := scanFileForEmits(f, fset, pkgConsts, helperMap, fileForError, root, topics)
		results = append(results, scanResults...)
	}
	return topics, results
}

// parsedFile wraps a parsed AST file for use in the two-pass emit scan.
type parsedFile struct {
	ast *ast.File
}

// collectParsedFiles walks slicesDir and parses all non-test Go files,
// returning their ASTs paired with file-level const maps.
func collectParsedFiles(fset *token.FileSet, slicesDir string) []parsedFile {
	var files []parsedFile
	_ = filepath.WalkDir(slicesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == "mem" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}
		files = append(files, parsedFile{ast: f})
		return nil
	})
	return files
}

// helperEmitFunc describes a same-package helper function whose body contains a
// single outbox.Emit call where the topic comes from one of its parameters.
type helperEmitFunc struct {
	// paramIndex is the 0-based index of the parameter that carries the topic.
	paramIndex int
}

// buildHelperEmitMap scans all files to find functions and methods that:
//   - have ≥1 parameter
//   - contain an outbox.Emit call (or receiver emit via outbox.Entry{EventType:param})
//     whose topic comes from one of the function/method's parameters (not a const)
//
// Returns map of funcName → helperEmitFunc.
// Both plain functions and methods are included (keyed by bare name).
// Only one-topic-from-param functions/methods are supported (first match wins).
// Two-level chains are handled: if a function/method calls another helper whose
// body emits and that helper is also in the map, the caller is also registered.
func buildHelperEmitMap(files []parsedFile, pkgConsts cellPkgConsts) map[string]helperEmitFunc {
	helpers := map[string]helperEmitFunc{}
	// First pass: direct emitters (outbox.Emit or receiver emit with entry var).
	registerDirectEmitters(files, pkgConsts, helpers)
	// Second pass: transitive callers — functions/methods that call a known helper.
	registerTransitiveCallers(files, pkgConsts, helpers)
	return helpers
}

// registerDirectEmitters adds all functions/methods whose body directly emits
// (via outbox.Emit or receiver emit with entry var) using a parameter as the topic.
func registerDirectEmitters(files []parsedFile, pkgConsts cellPkgConsts, helpers map[string]helperEmitFunc) {
	for _, pf := range files {
		scanFuncDeclsForDirectEmit(pf.ast.Decls, pkgConsts, helpers)
	}
}

// scanFuncDeclsForDirectEmit processes a list of declarations looking for
// direct-emit helper functions.
func scanFuncDeclsForDirectEmit(decls []ast.Decl, pkgConsts cellPkgConsts, helpers map[string]helperEmitFunc) {
	for _, decl := range decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil || fn.Type.Params == nil {
			continue
		}
		paramNames := funcParamNames(fn)
		if len(paramNames) == 0 {
			continue
		}
		if idx, found := findHelperTopicParamIndex(fn.Body, paramNames, pkgConsts); found {
			helpers[fn.Name.Name] = helperEmitFunc{paramIndex: idx}
		}
	}
}

// registerTransitiveCallers adds functions/methods that call a known helper and
// pass one of their own parameters as the helper's topic argument.
func registerTransitiveCallers(files []parsedFile, pkgConsts cellPkgConsts, helpers map[string]helperEmitFunc) {
	for _, pf := range files {
		scanFuncDeclsForTransitive(pf.ast.Decls, pkgConsts, helpers)
	}
}

// scanFuncDeclsForTransitive processes a list of declarations looking for
// transitive-emit helper functions.
func scanFuncDeclsForTransitive(decls []ast.Decl, pkgConsts cellPkgConsts, helpers map[string]helperEmitFunc) {
	for _, decl := range decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil || fn.Type.Params == nil {
			continue
		}
		if _, already := helpers[fn.Name.Name]; already {
			continue
		}
		paramNames := funcParamNames(fn)
		if len(paramNames) == 0 {
			continue
		}
		if idx, found := findTransitiveHelperParamIndex(fn.Body, paramNames, pkgConsts, helpers); found {
			helpers[fn.Name.Name] = helperEmitFunc{paramIndex: idx}
		}
	}
}

// funcParamNames returns the ordered list of parameter names for a FuncDecl.
func funcParamNames(fn *ast.FuncDecl) []string {
	var names []string
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// findHelperTopicParamIndex inspects a function/method body for emit evidence
// where the topic comes from one of the function's parameters.
//
// Patterns detected:
//  1. outbox.Emit(ctx, e, topicParam, ...) where topicParam ∈ paramNames.
//  2. entry := outbox.Entry{EventType: eventTypeParam, ...}; recv.Emit(ctx, entry)
//     where eventTypeParam ∈ paramNames.
//
// Returns the matching parameter index and true on first match found.
func findHelperTopicParamIndex(body *ast.BlockStmt, paramNames []string, pkgConsts cellPkgConsts) (int, bool) {
	// Pattern 1: outbox.Emit(ctx, e, topicParam, ...).
	if idx, found := findOutboxEmitTopicParam(body, paramNames, pkgConsts); found {
		return idx, true
	}
	// Pattern 2: entry := outbox.Entry{EventType: param}; recv.Emit(ctx, entry).
	return findReceiverEmitViaEntryParam(body, paramNames, pkgConsts)
}

// findOutboxEmitTopicParam checks if the body contains outbox.Emit(ctx, e, topicParam, ...)
// where topicParam is a parameter identifier. Returns the parameter index on match.
func findOutboxEmitTopicParam(body *ast.BlockStmt, paramNames []string, pkgConsts cellPkgConsts) (int, bool) {
	dummyConsts := pkgConstMap{}
	var foundIdx int
	var found bool
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !isOutboxEmitCall(call) || len(call.Args) < 3 {
			return true
		}
		topicArg := call.Args[2]
		ident, ok := topicArg.(*ast.Ident)
		if !ok {
			return true
		}
		if _, resolved := resolveTopicExpr(topicArg, pkgConsts, dummyConsts); resolved {
			return true // const — direct resolution handles it
		}
		for i, p := range paramNames {
			if ident.Name == p {
				foundIdx = i
				found = true
				return false
			}
		}
		return true
	})
	return foundIdx, found
}

// findReceiverEmitViaEntryParam checks if the body contains:
//
//	entry := outbox.Entry{EventType: paramIdent, ...}
//	recv.Emit(ctx, entry)
//
// where paramIdent is one of paramNames. Returns the parameter index on match.
func findReceiverEmitViaEntryParam(body *ast.BlockStmt, paramNames []string, pkgConsts cellPkgConsts) (int, bool) {
	entryVarToParamIdx := collectEntryVarBindings(body, paramNames, pkgConsts)
	if len(entryVarToParamIdx) == 0 {
		return 0, false
	}
	return findReceiverEmitWithEntry(body, entryVarToParamIdx)
}

// collectEntryVarBindings finds all `varName := outbox.Entry{EventType: paramIdent}`
// assignments where paramIdent is one of paramNames, and returns varName → paramIndex.
func collectEntryVarBindings(body *ast.BlockStmt, paramNames []string, pkgConsts cellPkgConsts) map[string]int {
	dummyConsts := pkgConstMap{}
	bindings := map[string]int{}
	ast.Inspect(body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		collectEntryBindingsFromAssign(stmt, paramNames, pkgConsts, dummyConsts, bindings)
		return true
	})
	return bindings
}

// collectEntryBindingsFromAssign inspects a single assignment statement for
// outbox.Entry{EventType: paramIdent} patterns.
func collectEntryBindingsFromAssign(
	stmt *ast.AssignStmt,
	paramNames []string,
	pkgConsts cellPkgConsts,
	dummyConsts pkgConstMap,
	bindings map[string]int,
) {
	for i, lhs := range stmt.Lhs {
		lhsIdent, ok := lhs.(*ast.Ident)
		if !ok || i >= len(stmt.Rhs) {
			continue
		}
		compLit, ok := stmt.Rhs[i].(*ast.CompositeLit)
		if !ok || !isOutboxEntryType(compLit) {
			continue
		}
		if pi, found := findEventTypeParamInCompLit(compLit, paramNames, pkgConsts, dummyConsts); found {
			bindings[lhsIdent.Name] = pi
		}
	}
}

// findEventTypeParamInCompLit looks for EventType: paramIdent in a composite literal,
// returning the index of the matching parameter name.
func findEventTypeParamInCompLit(
	compLit *ast.CompositeLit,
	paramNames []string,
	pkgConsts cellPkgConsts,
	dummyConsts pkgConstMap,
) (int, bool) {
	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "EventType" {
			continue
		}
		valIdent, ok := kv.Value.(*ast.Ident)
		if !ok {
			continue
		}
		if _, resolved := resolveTopicExpr(kv.Value, pkgConsts, dummyConsts); resolved {
			continue // const — not from param
		}
		for pi, p := range paramNames {
			if valIdent.Name == p {
				return pi, true
			}
		}
	}
	return 0, false
}

// findReceiverEmitWithEntry checks for recv.Emit(ctx, entryVar) where entryVar
// is a key in entryVarToParamIdx. Returns the matching parameter index.
func findReceiverEmitWithEntry(body *ast.BlockStmt, entryVarToParamIdx map[string]int) (int, bool) {
	var foundIdx int
	var found bool
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !isReceiverEmitCall(call) || len(call.Args) < 2 {
			return true
		}
		entryArg, ok := call.Args[1].(*ast.Ident)
		if !ok {
			return true
		}
		if pi, ok := entryVarToParamIdx[entryArg.Name]; ok {
			foundIdx = pi
			found = true
		}
		return !found
	})
	return foundIdx, found
}

// isOutboxEntryType checks whether a composite literal is (or looks like) outbox.Entry.
// We use a heuristic: the type is either a selector "outbox.Entry" or we check for
// the presence of an EventType field (since we can't resolve imports in stdlib-only mode).
func isOutboxEntryType(compLit *ast.CompositeLit) bool {
	if compLit.Type == nil {
		return false
	}
	sel, ok := compLit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "outbox" && sel.Sel.Name == "Entry"
}

// findTransitiveHelperParamIndex detects functions/methods that call a known helper
// (from the helpers map) and pass one of their own parameters as the topic arg.
// Returns the parameter index in the current function and true on first match.
func findTransitiveHelperParamIndex(
	body *ast.BlockStmt,
	paramNames []string,
	pkgConsts cellPkgConsts,
	helpers map[string]helperEmitFunc,
) (int, bool) {
	dummyConsts := pkgConstMap{}
	var foundIdx int
	var found bool
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		idx, ok := resolveTransitiveTopicParam(call, paramNames, pkgConsts, dummyConsts, helpers)
		if ok {
			foundIdx = idx
			found = true
		}
		return !found
	})
	return foundIdx, found
}

// resolveTransitiveTopicParam checks if the call is to a known helper and whether
// one of the caller's paramNames is passed as the helper's topic argument.
func resolveTransitiveTopicParam(
	call *ast.CallExpr,
	paramNames []string,
	pkgConsts cellPkgConsts,
	dummyConsts pkgConstMap,
	helpers map[string]helperEmitFunc,
) (int, bool) {
	calledName := resolveCallName(call)
	if calledName == "" {
		return 0, false
	}
	helper, ok := helpers[calledName]
	if !ok || helper.paramIndex >= len(call.Args) {
		return 0, false
	}
	topicArg := call.Args[helper.paramIndex]
	ident, ok := topicArg.(*ast.Ident)
	if !ok {
		return 0, false
	}
	if _, resolved := resolveTopicExpr(topicArg, pkgConsts, dummyConsts); resolved {
		return 0, false // const — handled by direct scan
	}
	for i, p := range paramNames {
		if ident.Name == p {
			return i, true
		}
	}
	return 0, false
}

// resolveCallName extracts the bare function/method name from a call expression.
// For ident calls (foo(...)), returns "foo".
// For selector calls (s.foo(...) or pkg.foo(...)), returns "foo".
// Returns "" for complex call expressions.
func resolveCallName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// scanFileForEmits walks a single parsed file's AST and collects emitted topics.
// It returns validation results for dynamic-topic errors.
// Emit evidence is exclusively from:
//   - outbox.Emit(ctx, e, TOPIC, ...) direct calls
//   - receiver.Emit(ctx, outbox.Entry{EventType: TOPIC}) inline composite
//   - receiver.Emit(ctx, entryVar) where entryVar was assigned outbox.Entry{EventType:...}
//   - helper(ctx, dto.TopicX) calls where helper's body emits via topicParam
func scanFileForEmits(
	pf parsedFile,
	fset *token.FileSet,
	pkgConsts cellPkgConsts,
	helperMap map[string]helperEmitFunc,
	fileForError string,
	root string,
	topics map[string]struct{},
) []ValidationResult {
	f := pf.ast
	fileConsts := collectFileConsts(f, pkgConsts)
	var results []ValidationResult
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// outbox.Emit(ctx, emitter, TOPIC, payload) — direct call.
		if isOutboxEmitCall(call) && len(call.Args) >= 3 {
			r := collectOutboxEmitTopic(call, fset, pkgConsts, fileConsts, fileForError, root, topics)
			results = append(results, r...)
			return true
		}

		// receiver.Emit(ctx, entry) — receiver-style emit.
		if isReceiverEmitCall(call) && len(call.Args) >= 2 {
			collectReceiverEmitTopics(call.Args[1], f, pkgConsts, fileConsts, topics)
			return true
		}

		// helper(args...) — check if this is a known same-package helper.
		collectHelperCallTopics(call, f, pkgConsts, fileConsts, helperMap, topics)
		return true
	})
	return results
}

// collectHelperCallTopics checks whether call is a call to a known helper function
// or method that emits via a topic parameter, and if so resolves the topic from
// the call-site argument.
//
// Handles both plain function calls (helper(...)) and receiver/selector method
// calls (s.method(...), pkg.Func(...)). The helper map is keyed by bare name
// so we match on the last segment (method name) regardless of receiver type.
func collectHelperCallTopics(
	call *ast.CallExpr,
	f *ast.File,
	pkgConsts cellPkgConsts,
	fileConsts pkgConstMap,
	helperMap map[string]helperEmitFunc,
	topics map[string]struct{},
) {
	calledName := resolveCallName(call)
	if calledName == "" {
		return
	}
	helper, ok := helperMap[calledName]
	if !ok {
		return
	}
	if helper.paramIndex >= len(call.Args) {
		return
	}
	topicArg := call.Args[helper.paramIndex]
	if topic, resolved := resolveTopicExpr(topicArg, pkgConsts, fileConsts); resolved {
		topics[topic] = struct{}{}
	}
}

// collectOutboxEmitTopic extracts the topic from outbox.Emit third arg.
// Appends to topics on success; returns a dynamic-topic error on call expressions.
// root is used to compute a project-relative file path for the finding.
func collectOutboxEmitTopic(
	call *ast.CallExpr,
	fset *token.FileSet,
	pkgConsts cellPkgConsts,
	fileConsts pkgConstMap,
	fileForError string,
	root string,
	topics map[string]struct{},
) []ValidationResult {
	topicExpr := call.Args[2]
	topic, resolved := resolveTopicExpr(topicExpr, pkgConsts, fileConsts)
	if resolved {
		topics[topic] = struct{}{}
		return nil
	}
	if isDynamicExpr(topicExpr) {
		pos := fset.Position(call.Pos())
		relFile := pos.Filename
		if root != "" {
			if rel, err := filepath.Rel(root, pos.Filename); err == nil {
				relFile = rel
			}
		}
		return []ValidationResult{{
			Code:      codeContractConsistencyEmit01,
			Severity:  SeverityError,
			IssueType: IssueInvalid,
			File:      relFile,
			Field:     "triggers",
			Message: fmt.Sprintf(
				"dynamic topic in emit not allowed at %s:%d; topic must be string literal or named constant",
				relFile, pos.Line,
			),
		}}
	}
	return nil
}

// collectReceiverEmitTopics resolves topics from a receiver-style Emit call.
func collectReceiverEmitTopics(
	entryArg ast.Expr,
	f *ast.File,
	pkgConsts cellPkgConsts,
	fileConsts pkgConstMap,
	topics map[string]struct{},
) {
	for _, t := range extractEntryTopics(entryArg, f, pkgConsts, fileConsts) {
		topics[t] = struct{}{}
	}
}

// buildPkgConsts scans internal/dto and internal/domain under cellDir
// for string constants and returns a map of pkgIdent → constName → stringValue.
func buildPkgConsts(cellDir string) cellPkgConsts {
	pkgConsts := cellPkgConsts{}
	for _, sub := range []string{"internal/dto", "internal/domain"} {
		dir := filepath.Join(cellDir, sub)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		parts := strings.Split(sub, "/")
		pkgIdent := parts[len(parts)-1]
		if pkgConsts[pkgIdent] == nil {
			pkgConsts[pkgIdent] = pkgConstMap{}
		}
		parseGoDir(dir, pkgConsts[pkgIdent])
	}
	return pkgConsts
}

// parseGoDir parses all non-test Go files in dir and extracts string const declarations.
func parseGoDir(dir string, consts pkgConstMap) {
	fset := token.NewFileSet()
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}
		extractStringConsts(f, consts)
		return nil
	})
}

// extractStringConsts walks a parsed file and adds string const declarations to consts.
func extractStringConsts(f *ast.File, consts pkgConstMap) {
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		extractStringConstsFromGenDecl(genDecl, consts)
	}
}

// extractStringConstsFromGenDecl extracts string const values from a single GenDecl.
func extractStringConstsFromGenDecl(genDecl *ast.GenDecl, consts pkgConstMap) {
	for _, spec := range genDecl.Specs {
		vspec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		extractStringConstsFromSpec(vspec, consts)
	}
}

// extractStringConstsFromSpec extracts string const values from a single ValueSpec.
func extractStringConstsFromSpec(vspec *ast.ValueSpec, consts pkgConstMap) {
	for i, name := range vspec.Names {
		if i >= len(vspec.Values) {
			continue
		}
		lit, ok := vspec.Values[i].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if val, err := strconv.Unquote(lit.Value); err == nil {
			consts[name.Name] = val
		}
	}
}

// collectFileConsts gathers const declarations from a single parsed file,
// resolving alias consts like: const TopicX = dto.TopicX.
func collectFileConsts(f *ast.File, pkgConsts cellPkgConsts) pkgConstMap {
	fileConsts := pkgConstMap{}
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			collectFileConstSpec(spec, pkgConsts, fileConsts)
		}
	}
	return fileConsts
}

// collectFileConstSpec resolves a single const spec value.
func collectFileConstSpec(spec ast.Spec, pkgConsts cellPkgConsts, fileConsts pkgConstMap) {
	vspec, ok := spec.(*ast.ValueSpec)
	if !ok {
		return
	}
	for i, name := range vspec.Names {
		if i >= len(vspec.Values) {
			continue
		}
		val := resolveConstValue(vspec.Values[i], pkgConsts)
		if val != "" {
			fileConsts[name.Name] = val
		}
	}
}

// resolveConstValue extracts the string value of a const expression.
func resolveConstValue(expr ast.Expr, pkgConsts cellPkgConsts) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if val, err := strconv.Unquote(v.Value); err == nil {
				return val
			}
		}
	case *ast.SelectorExpr:
		if ident, ok := v.X.(*ast.Ident); ok {
			if pkgMap, ok := pkgConsts[ident.Name]; ok {
				return pkgMap[v.Sel.Name] // "" if not found
			}
		}
	}
	return ""
}

// resolveTopicExpr resolves an AST expression to a string topic value.
// Returns ("", false) if unresolvable (not a dynamic/call expression).
func resolveTopicExpr(expr ast.Expr, pkgConsts cellPkgConsts, fileConsts pkgConstMap) (string, bool) {
	if val, ok := resolveBasicLit(expr); ok {
		return val, true
	}
	if val, ok := resolveSelectorExpr(expr, pkgConsts); ok {
		return val, true
	}
	if ident, ok := expr.(*ast.Ident); ok {
		if val, ok := fileConsts[ident.Name]; ok {
			return val, true
		}
	}
	return "", false
}

// resolveBasicLit extracts the string value from a BasicLit expression.
func resolveBasicLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	val, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return val, true
}

// resolveSelectorExpr resolves a pkg.Const selector expression using pkgConsts.
func resolveSelectorExpr(expr ast.Expr, pkgConsts cellPkgConsts) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	pkgMap, ok := pkgConsts[ident.Name]
	if !ok {
		return "", false
	}
	val, ok := pkgMap[sel.Sel.Name]
	return val, ok
}

// isDynamicExpr returns true if the expression is a call expression (fmt.Sprintf etc.).
func isDynamicExpr(expr ast.Expr) bool {
	_, isCall := expr.(*ast.CallExpr)
	return isCall
}

// isOutboxEmitCall returns true if the call is outbox.Emit(...).
func isOutboxEmitCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Emit" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "outbox"
}

// isReceiverEmitCall returns true if the call matches <receiver>.Emit(...)
// where the receiver is not "outbox" package.
func isReceiverEmitCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Emit" {
		return false
	}
	if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "outbox" {
		return false
	}
	return true
}

// extractEntryTopics resolves the EventType from the entry argument of a receiver Emit call.
// Handles both inline composite literals and pre-assigned entry variables.
func extractEntryTopics(entryArg ast.Expr, f *ast.File, pkgConsts cellPkgConsts, fileConsts pkgConstMap) []string {
	if compLit, ok := entryArg.(*ast.CompositeLit); ok {
		topic, found := extractEventTypeFromCompLit(compLit, pkgConsts, fileConsts)
		if found {
			return []string{topic}
		}
		return nil
	}
	ident, ok := entryArg.(*ast.Ident)
	if !ok {
		return nil
	}
	return findEntryTopicsFromIdent(ident.Name, f, pkgConsts, fileConsts)
}

// findEntryTopicsFromIdent searches the file AST for assignments to the given
// identifier and resolves their EventType fields.
func findEntryTopicsFromIdent(name string, f *ast.File, pkgConsts cellPkgConsts, fileConsts pkgConstMap) []string {
	var topics []string
	ast.Inspect(f, func(n ast.Node) bool {
		stmt, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range stmt.Lhs {
			lhsIdent, ok := lhs.(*ast.Ident)
			if !ok || lhsIdent.Name != name || i >= len(stmt.Rhs) {
				continue
			}
			if compLit, ok := stmt.Rhs[i].(*ast.CompositeLit); ok {
				if topic, found := extractEventTypeFromCompLit(compLit, pkgConsts, fileConsts); found {
					topics = append(topics, topic)
				}
			}
		}
		return true
	})
	return topics
}

// extractEventTypeFromCompLit finds the EventType field in a composite literal.
func extractEventTypeFromCompLit(compLit *ast.CompositeLit, pkgConsts cellPkgConsts, fileConsts pkgConstMap) (string, bool) {
	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "EventType" {
			continue
		}
		return resolveTopicExpr(kv.Value, pkgConsts, fileConsts)
	}
	return "", false
}
