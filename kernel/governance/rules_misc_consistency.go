package governance

// Rule CONTRACT-CONSISTENCY-EMIT-01 validates bidirectional alignment between
// HTTP contract triggers and outbox.Emit calls in the slice serving that HTTP contract.
//
// Three constraints:
//  1. L2+ HTTP contract without triggers → Error (triggers required)
//  2. Triggers present but consistencyLevel ∈ {L0, L1} → Error (level mismatch)
//  3. Bidirectional AST check: every trigger must appear in a resolvable emit
//     call in the serving slice, and every resolvable emit topic in that serving
//     slice must be covered by one of the HTTP contracts served by the slice.
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
	"maps"
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

type consistencyIndex struct {
	contractByID       map[string]*metadata.ContractMeta
	httpServeSlices    map[string][]sliceRef
	eventPublishSlices map[string][]sliceRef
}

type sliceRef struct {
	cellID  string
	sliceID string
	dir     string
	file    string
}

// validateCONTRACTCONSISTENCYEMIT01 checks three consistency constraints
// between HTTP contract triggers and outbox emit calls in service files.
// Contracts under examples/ are excluded — they are illustrative and may
// not conform to the full governance model.
//
// Structured as two phases:
//
//  1. Per-slice phase: scan emit topics only for slices that serve L2+ HTTP
//     contracts with triggers, then reverse-check those emits against contracts
//     served by the same slice.
//
//  2. Per-contract phase: run constraint-1/2 checks and forward-check (every
//     declared trigger must reference a real event contract and appear in the
//     serving slice's emit set).
//
// The per-slice phase runs before the per-contract loop so reverse-checks are
// evaluated once per serving slice instead of once per contract.
func (v *Validator) validateCONTRACTCONSISTENCYEMIT01() []ValidationResult {
	idx := buildConsistencyIndex(v.project)
	sliceEmitSets, perSliceResults := v.runPerSlicePhase(idx)
	perContractResults := v.runPerContractPhase(idx, sliceEmitSets)
	return append(perSliceResults, perContractResults...)
}

func buildConsistencyIndex(project *metadata.ProjectMeta) consistencyIndex {
	idx := consistencyIndex{
		contractByID:       map[string]*metadata.ContractMeta{},
		httpServeSlices:    map[string][]sliceRef{},
		eventPublishSlices: map[string][]sliceRef{},
	}
	if project == nil {
		return idx
	}
	maps.Copy(idx.contractByID, project.Contracts)
	for key, s := range project.Slices {
		if s == nil {
			continue
		}
		ref := newSliceRef(key, s)
		for _, cu := range s.ContractUsages {
			switch cu.Role {
			case "serve":
				idx.httpServeSlices[cu.Contract] = append(idx.httpServeSlices[cu.Contract], ref)
			case "publish":
				idx.eventPublishSlices[cu.Contract] = append(idx.eventPublishSlices[cu.Contract], ref)
			}
		}
	}
	return idx
}

func newSliceRef(key string, s *metadata.SliceMeta) sliceRef {
	cellID := s.BelongsToCell
	sliceID := s.ID
	if cellID == "" || sliceID == "" {
		parts := strings.Split(key, "/")
		if len(parts) == 2 {
			if cellID == "" {
				cellID = parts[0]
			}
			if sliceID == "" {
				sliceID = parts[1]
			}
		}
	}
	cellDir := s.CellDir
	if cellDir == "" {
		cellDir = cellID
	}
	sliceDir := s.Dir
	if sliceDir == "" {
		sliceDir = sliceID
	}
	dir := filepath.ToSlash(filepath.Join("cells", cellDir, "slices", sliceDir))
	if s.File != "" {
		dir = filepath.ToSlash(filepath.Dir(s.File))
	}
	return sliceRef{
		cellID:  cellID,
		sliceID: sliceID,
		dir:     dir,
		file:    s.File,
	}
}

func (r sliceRef) key() string {
	return r.cellID + "/" + r.sliceID + "@" + r.dir
}

// runPerSlicePhase scans only HTTP-serving slices and runs reverse-checks
// against the triggers declared by HTTP contracts served by that same slice.
func (v *Validator) runPerSlicePhase(idx consistencyIndex) (
	map[string]map[string]struct{}, []ValidationResult,
) {
	sliceTriggerSets := map[string]map[string]struct{}{}
	slicesByKey := map[string]sliceRef{}

	for _, c := range v.project.Contracts {
		addServingSliceTriggers(c, idx, slicesByKey, sliceTriggerSets)
	}
	return v.scanServingSlices(slicesByKey, sliceTriggerSets)
}

func addServingSliceTriggers(
	c *metadata.ContractMeta,
	idx consistencyIndex,
	slicesByKey map[string]sliceRef,
	sliceTriggerSets map[string]map[string]struct{},
) {
	if cell.ContractKind(c.Kind) != cell.ContractHTTP || isExamplePath(c.File) || isExamplePath(c.Dir) {
		return
	}
	if len(c.Triggers) == 0 || !isL2OrHigher(c.ConsistencyLevel) {
		return
	}
	for _, ref := range idx.httpServeSlices[c.ID] {
		key := ref.key()
		slicesByKey[key] = ref
		if sliceTriggerSets[key] == nil {
			sliceTriggerSets[key] = map[string]struct{}{}
		}
		addTriggers(sliceTriggerSets[key], c.Triggers)
	}
}

func addTriggers(dst map[string]struct{}, triggers []string) {
	for _, trigger := range triggers {
		dst[trigger] = struct{}{}
	}
}

func (v *Validator) scanServingSlices(
	slicesByKey map[string]sliceRef,
	sliceTriggerSets map[string]map[string]struct{},
) (map[string]map[string]struct{}, []ValidationResult) {
	var results []ValidationResult
	sliceEmitSets := map[string]map[string]struct{}{}

	for key, ref := range slicesByKey {
		emits, scanResults := scanSliceEmitTopics(v.root, ref, "")
		results = append(results, scanResults...)
		sliceEmitSets[key] = emits
		results = append(results, v.checkReverseEmits(ref, emits, sliceTriggerSets[key])...)
	}
	return sliceEmitSets, results
}

// runPerContractPhase runs constraint-1/2 and forward-check for each HTTP contract.
func (v *Validator) runPerContractPhase(
	idx consistencyIndex,
	sliceEmitSets map[string]map[string]struct{},
) []ValidationResult {
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
		servingSlices := idx.httpServeSlices[c.ID]
		results = append(results, v.checkTriggerContracts(c, servingSlices, idx)...)
		results = append(results, v.checkForwardTriggers(c, servingSlices, sliceEmitSets)...)
	}
	return results
}

// isExamplePath returns true if the path is under an examples/ subtree.
func isExamplePath(p string) bool {
	return strings.HasPrefix(p, "examples/")
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

func (v *Validator) checkTriggerContracts(
	c *metadata.ContractMeta,
	servingSlices []sliceRef,
	idx consistencyIndex,
) []ValidationResult {
	var results []ValidationResult
	if len(servingSlices) == 0 {
		results = append(results, v.newResult(
			codeContractConsistencyEmit01, SeverityError, IssueRefNotFound,
			contractFile(c), "triggers",
			fmt.Sprintf("contract %q declares triggers but no slice declares role: serve for it", c.ID),
		))
	}
	for _, t := range c.Triggers {
		eventContract, ok := idx.contractByID[t]
		if !ok {
			results = append(results, v.newResult(
				codeContractConsistencyEmit01, SeverityError, IssueRefNotFound,
				contractFile(c), "triggers",
				fmt.Sprintf("contract %q declares trigger %q but it does not reference an existing event contract", c.ID, t),
			))
			continue
		}
		if cell.ContractKind(eventContract.Kind) != cell.ContractEvent {
			results = append(results, v.newResult(
				codeContractConsistencyEmit01, SeverityError, IssueMismatch,
				contractFile(c), "triggers",
				fmt.Sprintf(advHintCCE01TriggerNotEvent, c.ID, t, eventContract.Kind),
			))
			continue
		}
		if eventContract.OwnerCell != c.OwnerCell || eventContract.Endpoints.Publisher != c.OwnerCell {
			results = append(results, v.newResult(
				codeContractConsistencyEmit01, SeverityError, IssueMismatch,
				contractFile(c), "triggers",
				fmt.Sprintf(advHintCCE01OwnerMismatch, c.ID, t, c.OwnerCell),
			))
		}
		for _, ref := range servingSlices {
			if !slicePublishes(idx, ref, t) {
				results = append(results, v.newResult(
					codeContractConsistencyEmit01, SeverityError, IssueMismatch,
					contractFile(c), "triggers",
					fmt.Sprintf(advHintCCE01SliceNotPublish, c.ID, t, ref.cellID, ref.sliceID),
				))
			}
		}
	}
	return results
}

func slicePublishes(idx consistencyIndex, ref sliceRef, contractID string) bool {
	for _, publisher := range idx.eventPublishSlices[contractID] {
		if publisher.key() == ref.key() {
			return true
		}
	}
	return false
}

// checkForwardTriggers validates that each contract trigger appears in emitTopics
// from each slice that serves that HTTP contract.
func (v *Validator) checkForwardTriggers(
	c *metadata.ContractMeta,
	servingSlices []sliceRef,
	sliceEmitSets map[string]map[string]struct{},
) []ValidationResult {
	var results []ValidationResult
	for _, ref := range servingSlices {
		emitTopics := sliceEmitSets[ref.key()]
		for _, t := range c.Triggers {
			if _, found := emitTopics[t]; !found {
				results = append(results, v.newResult(
					codeContractConsistencyEmit01, SeverityError, IssueRefNotFound,
					contractFile(c), "triggers",
					fmt.Sprintf(advHintCCE01TriggerNotEmitted, c.ID, t, ref.dir, ref.cellID, ref.sliceID),
				))
			}
		}
	}
	return results
}

// checkReverseEmits validates that each emitted topic appears in declared triggers.
// declared may be nil when a cell has no HTTP contracts; in that case every emit fails.
func (v *Validator) checkReverseEmits(
	ref sliceRef,
	emitTopics map[string]struct{},
	declared map[string]struct{},
) []ValidationResult {
	var results []ValidationResult
	for t := range emitTopics {
		if _, found := declared[t]; !found {
			results = append(results, v.newScopedResult(
				codeContractConsistencyEmit01, SeverityError, IssueRefNotFound,
				"project", "triggers",
				fmt.Sprintf(advHintCCE01ReverseEmit, t, ref.cellID, ref.sliceID, t),
			))
		}
	}
	return results
}

func scanSliceEmitTopics(root string, ref sliceRef, fileForError string) (map[string]struct{}, []ValidationResult) {
	topics := map[string]struct{}{}
	var results []ValidationResult

	cellDir := filepath.Join(root, "cells", ref.cellID)
	pkgConsts, constResults := buildPkgConsts(cellDir, fileForError)
	results = append(results, constResults...)
	sliceDir := filepath.Join(root, filepath.FromSlash(ref.dir))
	if _, err := os.Stat(sliceDir); err != nil {
		return topics, results
	}

	fset := token.NewFileSet()
	allFiles, err := collectParsedFiles(fset, sliceDir)
	if err != nil {
		results = append(results, ValidationResult{
			Code:      codeContractConsistencyEmit01,
			Severity:  SeverityError,
			IssueType: IssueInvalid,
			File:      fileForError,
			Field:     "triggers",
			Message:   fmt.Sprintf("cannot scan emitted topics in %s/%s: %v", ref.cellID, ref.sliceID, err),
		})
		return topics, results
	}
	helperMap := buildHelperEmitMap(allFiles, pkgConsts)

	for _, f := range allFiles {
		scanResults := scanFileForEmits(f, fset, pkgConsts, helperMap, fileForError, root, topics)
		results = append(results, scanResults...)
	}
	return topics, results
}

// parsedFile wraps a parsed AST file for use in the two-pass emit scan.
type parsedFile struct {
	ast         *ast.File
	path        string
	dir         string
	packageName string
}

// collectParsedFiles walks slicesDir and parses all non-test Go files,
// returning their ASTs paired with file-level const maps.
func collectParsedFiles(fset *token.FileSet, slicesDir string) ([]parsedFile, error) {
	var files []parsedFile
	err := filepath.WalkDir(slicesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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
			return parseErr
		}
		files = append(files, parsedFile{
			ast:         f,
			path:        path,
			dir:         filepath.Dir(path),
			packageName: f.Name.Name,
		})
		return nil
	})
	return files, err
}

// helperEmitFunc describes a same-package helper function whose body contains a
// single outbox.Emit call where the topic comes from one of its parameters.
type helperEmitFunc struct {
	// paramIndex is the 0-based index of the parameter that carries the topic.
	paramIndex int
}

type helperKey struct {
	dir      string
	pkg      string
	name     string
	receiver string
}

// buildHelperEmitMap scans all files to find functions and methods that:
//   - have ≥1 parameter
//   - contain an outbox.Emit call (or receiver emit via outbox.Entry{EventType:param})
//     whose topic comes from one of the function/method's parameters (not a const)
//
// Returns map of package-dir + function/method identity → helperEmitFunc.
// Only one-topic-from-param functions/methods are supported (first match wins).
// Two-level chains are handled: if a function/method calls another helper whose
// body emits and that helper is also in the map, the caller is also registered.
func buildHelperEmitMap(files []parsedFile, pkgConsts cellPkgConsts) map[helperKey]helperEmitFunc {
	helpers := map[helperKey]helperEmitFunc{}
	// First pass: direct emitters (outbox.Emit or receiver emit with entry var).
	registerDirectEmitters(files, pkgConsts, helpers)
	// Second pass: transitive callers — functions/methods that call a known helper.
	registerTransitiveCallers(files, pkgConsts, helpers)
	return helpers
}

// registerDirectEmitters adds all functions/methods whose body directly emits
// (via outbox.Emit or receiver emit with entry var) using a parameter as the topic.
func registerDirectEmitters(files []parsedFile, pkgConsts cellPkgConsts, helpers map[helperKey]helperEmitFunc) {
	for _, pf := range files {
		scanFuncDeclsForDirectEmit(pf, pkgConsts, helpers)
	}
}

// scanFuncDeclsForDirectEmit processes a list of declarations looking for
// direct-emit helper functions.
func scanFuncDeclsForDirectEmit(pf parsedFile, pkgConsts cellPkgConsts, helpers map[helperKey]helperEmitFunc) {
	for _, decl := range pf.ast.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil || fn.Type.Params == nil {
			continue
		}
		paramNames := funcParamNames(fn)
		if len(paramNames) == 0 {
			continue
		}
		if idx, found := findHelperTopicParamIndex(fn.Body, paramNames, pkgConsts); found {
			helpers[funcHelperKey(pf, fn)] = helperEmitFunc{paramIndex: idx}
		}
	}
}

// registerTransitiveCallers adds functions/methods that call a known helper and
// pass one of their own parameters as the helper's topic argument.
func registerTransitiveCallers(files []parsedFile, pkgConsts cellPkgConsts, helpers map[helperKey]helperEmitFunc) {
	for _, pf := range files {
		scanFuncDeclsForTransitive(pf, pkgConsts, helpers)
	}
}

// scanFuncDeclsForTransitive processes a list of declarations looking for
// transitive-emit helper functions.
func scanFuncDeclsForTransitive(pf parsedFile, pkgConsts cellPkgConsts, helpers map[helperKey]helperEmitFunc) {
	for _, decl := range pf.ast.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil || fn.Type.Params == nil {
			continue
		}
		key := funcHelperKey(pf, fn)
		if _, already := helpers[key]; already {
			continue
		}
		paramNames := funcParamNames(fn)
		if len(paramNames) == 0 {
			continue
		}
		scope := functionScope(fn)
		if idx, found := findTransitiveHelperParamIndex(fn.Body, pf, scope, paramNames, pkgConsts, helpers); found {
			helpers[key] = helperEmitFunc{paramIndex: idx}
		}
	}
}

func funcHelperKey(pf parsedFile, fn *ast.FuncDecl) helperKey {
	return helperKey{
		dir:      pf.dir,
		pkg:      pf.packageName,
		name:     fn.Name.Name,
		receiver: receiverTypeName(fn),
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

type emitScope struct {
	receiverVars map[string]string
}

func functionScope(fn *ast.FuncDecl) emitScope {
	scope := emitScope{receiverVars: map[string]string{}}
	if fn.Recv != nil {
		addReceiverVars(scope.receiverVars, fn.Recv.List)
	}
	if fn.Type.Params != nil {
		addReceiverVars(scope.receiverVars, fn.Type.Params.List)
	}
	return scope
}

func addReceiverVars(receiverVars map[string]string, fields []*ast.Field) {
	for _, field := range fields {
		typ := exprTypeName(field.Type)
		if typ == "" {
			continue
		}
		for _, name := range field.Names {
			receiverVars[name.Name] = typ
		}
	}
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return exprTypeName(fn.Recv.List[0].Type)
}

func exprTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return exprTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
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
	pf parsedFile,
	scope emitScope,
	paramNames []string,
	pkgConsts cellPkgConsts,
	helpers map[helperKey]helperEmitFunc,
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
		idx, ok := resolveTransitiveTopicParam(call, pf, scope, paramNames, pkgConsts, dummyConsts, helpers)
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
	pf parsedFile,
	scope emitScope,
	paramNames []string,
	pkgConsts cellPkgConsts,
	dummyConsts pkgConstMap,
	helpers map[helperKey]helperEmitFunc,
) (int, bool) {
	calledKey, ok := resolveHelperCallKey(call, pf, scope)
	if !ok {
		return 0, false
	}
	helper, ok := helpers[calledKey]
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

func resolveHelperCallKey(call *ast.CallExpr, pf parsedFile, scope emitScope) (helperKey, bool) {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return helperKey{
			dir:  pf.dir,
			pkg:  pf.packageName,
			name: fn.Name,
		}, true
	case *ast.SelectorExpr:
		ident, ok := fn.X.(*ast.Ident)
		if !ok {
			return helperKey{}, false
		}
		receiver, ok := scope.receiverVars[ident.Name]
		if !ok {
			return helperKey{}, false
		}
		return helperKey{
			dir:      pf.dir,
			pkg:      pf.packageName,
			name:     fn.Sel.Name,
			receiver: receiver,
		}, true
	}
	return helperKey{}, false
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
	helperMap map[helperKey]helperEmitFunc,
	_ string,
	root string,
	topics map[string]struct{},
) []ValidationResult {
	var results []ValidationResult
	fileConsts := collectFileConsts(pf.ast, pkgConsts)
	for _, decl := range pf.ast.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ctx := emitScanContext{
			pf:         pf,
			fset:       fset,
			pkgConsts:  pkgConsts,
			fileConsts: fileConsts,
			helperMap:  helperMap,
			root:       root,
			topics:     topics,
			scope:      functionScope(fn),
			paramNames: funcParamNames(fn),
		}
		if helper, ok := helperMap[funcHelperKey(pf, fn)]; ok {
			ctx.currentHelperParam = helper.paramIndex
		} else {
			ctx.currentHelperParam = -1
		}
		state := emitScanState{entryTopics: map[string][]string{}}
		results = append(results, scanBlockForEmits(fn.Body, ctx, &state)...)
	}
	return results
}

type emitScanContext struct {
	pf                 parsedFile
	fset               *token.FileSet
	pkgConsts          cellPkgConsts
	fileConsts         pkgConstMap
	helperMap          map[helperKey]helperEmitFunc
	root               string
	topics             map[string]struct{}
	scope              emitScope
	paramNames         []string
	currentHelperParam int
}

type emitScanState struct {
	entryTopics map[string][]string
}

func scanBlockForEmits(block *ast.BlockStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	if block == nil {
		return nil
	}
	var results []ValidationResult
	for _, stmt := range block.List {
		results = append(results, scanStmtForEmits(stmt, ctx, state)...)
	}
	return results
}

func scanStmtForEmits(stmt ast.Stmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		results := collectEntryAssignments(s, ctx, state)
		return append(results, scanNodeForEmitCalls(s, ctx, state)...)
	case *ast.DeclStmt:
		results := collectEntryDecls(s, ctx, state)
		return append(results, scanNodeForEmitCalls(s, ctx, state)...)
	case *ast.ReturnStmt, *ast.ExprStmt, *ast.GoStmt, *ast.DeferStmt, *ast.SendStmt:
		return scanNodeForEmitCalls(s, ctx, state)
	case *ast.IfStmt:
		return scanIfForEmits(s, ctx, state)
	case *ast.ForStmt:
		return scanForForEmits(s, ctx, state)
	case *ast.RangeStmt:
		return scanRangeForEmits(s, ctx, state)
	case *ast.SwitchStmt:
		return scanSwitchForEmits(s, ctx, state)
	case *ast.TypeSwitchStmt:
		return scanTypeSwitchForEmits(s, ctx, state)
	case *ast.SelectStmt:
		return scanSelectForEmits(s, ctx, state)
	case *ast.BlockStmt:
		return scanBlockForEmits(s, ctx, state)
	default:
		return scanNodeForEmitCalls(s, ctx, state)
	}
}

func scanIfForEmits(stmt *ast.IfStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	if stmt.Init != nil {
		results = append(results, scanStmtForEmits(stmt.Init, ctx, state)...)
	}
	if stmt.Cond != nil {
		results = append(results, scanNodeForEmitCalls(stmt.Cond, ctx, state)...)
	}
	results = append(results, scanBlockForEmits(stmt.Body, ctx, state)...)
	if stmt.Else != nil {
		results = append(results, scanElseForEmits(stmt.Else, ctx, state)...)
	}
	return results
}

func scanForForEmits(stmt *ast.ForStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	if stmt.Init != nil {
		results = append(results, scanStmtForEmits(stmt.Init, ctx, state)...)
	}
	if stmt.Cond != nil {
		results = append(results, scanNodeForEmitCalls(stmt.Cond, ctx, state)...)
	}
	if stmt.Post != nil {
		results = append(results, scanStmtForEmits(stmt.Post, ctx, state)...)
	}
	return append(results, scanBlockForEmits(stmt.Body, ctx, state)...)
}

func scanRangeForEmits(stmt *ast.RangeStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	results := scanNodeForEmitCalls(stmt.X, ctx, state)
	return append(results, scanBlockForEmits(stmt.Body, ctx, state)...)
}

func scanElseForEmits(node ast.Stmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	if block, ok := node.(*ast.BlockStmt); ok {
		return scanBlockForEmits(block, ctx, state)
	}
	return scanStmtForEmits(node, ctx, state)
}

func scanSwitchForEmits(stmt *ast.SwitchStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	if stmt.Init != nil {
		results = append(results, scanStmtForEmits(stmt.Init, ctx, state)...)
	}
	if stmt.Tag != nil {
		results = append(results, scanNodeForEmitCalls(stmt.Tag, ctx, state)...)
	}
	for _, item := range stmt.Body.List {
		clause, ok := item.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range clause.List {
			results = append(results, scanNodeForEmitCalls(expr, ctx, state)...)
		}
		for _, bodyStmt := range clause.Body {
			results = append(results, scanStmtForEmits(bodyStmt, ctx, state)...)
		}
	}
	return results
}

func scanTypeSwitchForEmits(stmt *ast.TypeSwitchStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	if stmt.Init != nil {
		results = append(results, scanStmtForEmits(stmt.Init, ctx, state)...)
	}
	if stmt.Assign != nil {
		results = append(results, scanStmtForEmits(stmt.Assign, ctx, state)...)
	}
	for _, item := range stmt.Body.List {
		clause, ok := item.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, bodyStmt := range clause.Body {
			results = append(results, scanStmtForEmits(bodyStmt, ctx, state)...)
		}
	}
	return results
}

func scanSelectForEmits(stmt *ast.SelectStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	for _, item := range stmt.Body.List {
		clause, ok := item.(*ast.CommClause)
		if !ok {
			continue
		}
		if clause.Comm != nil {
			results = append(results, scanStmtForEmits(clause.Comm, ctx, state)...)
		}
		for _, bodyStmt := range clause.Body {
			results = append(results, scanStmtForEmits(bodyStmt, ctx, state)...)
		}
	}
	return results
}

func scanNodeForEmitCalls(node ast.Node, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		results = append(results, collectEmitCallTopics(call, ctx, state)...)
		return true
	})
	return results
}

func collectEmitCallTopics(call *ast.CallExpr, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	if isOutboxEmitCall(call) && len(call.Args) >= 3 {
		return collectOutboxEmitTopic(call, ctx, state)
	}
	if isReceiverEmitCall(call) && len(call.Args) >= 2 {
		results = append(results, collectReceiverEmitTopics(call.Args[1], ctx, state)...)
	}
	results = append(results, collectHelperCallTopics(call, ctx)...)
	return results
}

// collectHelperCallTopics checks whether call is a call to a known helper function
// or method that emits via a topic parameter, and if so resolves the topic from
// the call-site argument.
//
// Handles plain function calls (helper(...)) and receiver method calls when the
// receiver variable can be tied to the current function's receiver or parameter
// type. Package selectors and arbitrary selector last-segment matches are not
// accepted as helper evidence.
func collectHelperCallTopics(
	call *ast.CallExpr,
	ctx emitScanContext,
) []ValidationResult {
	calledKey, ok := resolveHelperCallKey(call, ctx.pf, ctx.scope)
	if !ok {
		return nil
	}
	helper, ok := ctx.helperMap[calledKey]
	if !ok {
		return nil
	}
	if helper.paramIndex >= len(call.Args) {
		return nil
	}
	topicArg := call.Args[helper.paramIndex]
	if topic, resolved := resolveTopicExpr(topicArg, ctx.pkgConsts, ctx.fileConsts); resolved {
		ctx.topics[topic] = struct{}{}
		return nil
	}
	if isCurrentHelperTopicParam(topicArg, ctx) {
		return nil
	}
	return []ValidationResult{dynamicTopicResult(
		topicArg, ctx.fset, ctx.root,
		"dynamic topic in helper emit not allowed; topic argument must resolve to a string literal or named constant",
	)}
}

// collectOutboxEmitTopic extracts the topic from outbox.Emit third arg.
// Appends to topics on success; returns a dynamic-topic error on call expressions.
// root is used to compute a project-relative file path for the finding.
func collectOutboxEmitTopic(
	call *ast.CallExpr,
	ctx emitScanContext,
	state *emitScanState,
) []ValidationResult {
	topicExpr := call.Args[2]
	topic, resolved := resolveTopicExpr(topicExpr, ctx.pkgConsts, ctx.fileConsts)
	if resolved {
		ctx.topics[topic] = struct{}{}
		return nil
	}
	if isCurrentHelperTopicParam(topicExpr, ctx) {
		return nil
	}
	if isDynamicExpr(topicExpr) {
		return []ValidationResult{dynamicTopicResult(
			topicExpr, ctx.fset, ctx.root,
			"dynamic topic in emit not allowed; topic must be string literal or named constant",
		)}
	}
	_ = state
	return nil
}

// collectReceiverEmitTopics resolves topics from a receiver-style Emit call.
func collectReceiverEmitTopics(
	entryArg ast.Expr,
	ctx emitScanContext,
	state *emitScanState,
) []ValidationResult {
	resolved, results := extractEntryTopics(entryArg, ctx, state)
	for _, t := range resolved {
		ctx.topics[t] = struct{}{}
	}
	return results
}

func collectEntryAssignments(stmt *ast.AssignStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	var results []ValidationResult
	for i, lhs := range stmt.Lhs {
		lhsIdent, ok := lhs.(*ast.Ident)
		if !ok || i >= len(stmt.Rhs) {
			continue
		}
		compLit, ok := stmt.Rhs[i].(*ast.CompositeLit)
		if !ok || !isOutboxEntryType(compLit) {
			delete(state.entryTopics, lhsIdent.Name)
			continue
		}
		results = append(results, bindEntryTopic(lhsIdent.Name, compLit, ctx, state)...)
	}
	return results
}

func collectEntryDecls(stmt *ast.DeclStmt, ctx emitScanContext, state *emitScanState) []ValidationResult {
	genDecl, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || genDecl.Tok != token.VAR {
		return nil
	}
	var results []ValidationResult
	for _, spec := range genDecl.Specs {
		vspec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vspec.Names {
			if i >= len(vspec.Values) {
				continue
			}
			compLit, ok := vspec.Values[i].(*ast.CompositeLit)
			if !ok || !isOutboxEntryType(compLit) {
				continue
			}
			results = append(results, bindEntryTopic(name.Name, compLit, ctx, state)...)
		}
	}
	return results
}

func bindEntryTopic(name string, compLit *ast.CompositeLit, ctx emitScanContext, state *emitScanState) []ValidationResult {
	topic, found, results := extractEventTypeFromCompLit(compLit, ctx)
	if found {
		state.entryTopics[name] = []string{topic}
		return results
	}
	delete(state.entryTopics, name)
	return results
}

func isCurrentHelperTopicParam(expr ast.Expr, ctx emitScanContext) bool {
	if ctx.currentHelperParam < 0 || ctx.currentHelperParam >= len(ctx.paramNames) {
		return false
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == ctx.paramNames[ctx.currentHelperParam]
}

func dynamicTopicResult(expr ast.Expr, fset *token.FileSet, root, message string) ValidationResult {
	pos := fset.Position(expr.Pos())
	relFile := pos.Filename
	if root != "" {
		if rel, err := filepath.Rel(root, pos.Filename); err == nil {
			relFile = filepath.ToSlash(rel)
		}
	}
	return ValidationResult{
		Code:      codeContractConsistencyEmit01,
		Severity:  SeverityError,
		IssueType: IssueInvalid,
		File:      relFile,
		Field:     "triggers",
		Message: fmt.Sprintf(
			"%s at %s:%d:%d",
			message, relFile, pos.Line, pos.Column,
		),
		Line:   pos.Line,
		Column: pos.Column,
	}
}

// buildPkgConsts scans internal/dto and internal/domain under cellDir
// for string constants and returns a map of pkgIdent → constName → stringValue.
func buildPkgConsts(cellDir, fileForError string) (cellPkgConsts, []ValidationResult) {
	pkgConsts := cellPkgConsts{}
	var results []ValidationResult
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
		if err := parseGoDir(dir, pkgConsts[pkgIdent]); err != nil {
			results = append(results, ValidationResult{
				Code:      codeContractConsistencyEmit01,
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      fileForError,
				Field:     "triggers",
				Message:   fmt.Sprintf("cannot scan constants in %s: %v", filepath.ToSlash(dir), err),
			})
		}
	}
	return pkgConsts, results
}

// parseGoDir parses all non-test Go files in dir and extracts string const declarations.
func parseGoDir(dir string, consts pkgConstMap) error {
	fset := token.NewFileSet()
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
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
// Handles both inline composite literals and entry variables assigned earlier in the
// current function body.
func extractEntryTopics(entryArg ast.Expr, ctx emitScanContext, state *emitScanState) ([]string, []ValidationResult) {
	if compLit, ok := entryArg.(*ast.CompositeLit); ok {
		topic, found, results := extractEventTypeFromCompLit(compLit, ctx)
		if found {
			return []string{topic}, results
		}
		return nil, results
	}
	ident, ok := entryArg.(*ast.Ident)
	if !ok {
		return nil, nil
	}
	return state.entryTopics[ident.Name], nil
}

// extractEventTypeFromCompLit finds the EventType field in a composite literal.
func extractEventTypeFromCompLit(compLit *ast.CompositeLit, ctx emitScanContext) (string, bool, []ValidationResult) {
	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "EventType" {
			continue
		}
		if topic, resolved := resolveTopicExpr(kv.Value, ctx.pkgConsts, ctx.fileConsts); resolved {
			return topic, true, nil
		}
		if isCurrentHelperTopicParam(kv.Value, ctx) {
			return "", false, nil
		}
		if isDynamicExpr(kv.Value) {
			return "", false, []ValidationResult{dynamicTopicResult(
				kv.Value, ctx.fset, ctx.root,
				"dynamic topic in receiver emit not allowed; EventType must resolve to a string literal or named constant",
			)}
		}
		return "", false, nil
	}
	return "", false, nil
}
