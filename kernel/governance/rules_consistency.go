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
func (v *Validator) validateCONTRACTCONSISTENCYEMIT01() []ValidationResult {
	var results []ValidationResult

	// Pre-compute per-cell declared triggers (union of all HTTP contracts in cell).
	cellTriggerCache := buildCellTriggerCache(v.project)
	cellEmitCache := map[string]map[string]struct{}{} // ownerCell → emitted topics
	reverseChecked := map[string]bool{}

	for _, c := range v.project.Contracts {
		if cell.ContractKind(c.Kind) != cell.ContractHTTP {
			continue
		}
		if isExamplePath(c.File) || isExamplePath(c.Dir) {
			continue
		}
		r, skip := v.checkConsistencyConstraints12(c)
		results = append(results, r...)
		if skip || len(c.Triggers) == 0 {
			continue
		}
		emitTopics, scanResults := lazyLoadEmitTopics(cellEmitCache, c.OwnerCell, v.root, contractFile(c))
		results = append(results, scanResults...)
		results = append(results, v.checkForwardTriggers(c, emitTopics)...)
		if !reverseChecked[c.OwnerCell] {
			reverseChecked[c.OwnerCell] = true
			results = append(results, v.checkReverseEmits(c.OwnerCell, emitTopics, cellTriggerCache)...)
		}
	}
	return results
}

// isExamplePath returns true if the path is under an examples/ subtree.
func isExamplePath(p string) bool {
	return strings.HasPrefix(p, "examples/")
}

// buildCellTriggerCache pre-computes per-cell declared triggers for all HTTP contracts.
func buildCellTriggerCache(project *metadata.ProjectMeta) map[string]map[string]struct{} {
	cache := map[string]map[string]struct{}{}
	for _, c := range project.Contracts {
		if cell.ContractKind(c.Kind) != cell.ContractHTTP {
			continue
		}
		if isExamplePath(c.File) || isExamplePath(c.Dir) {
			continue
		}
		if cache[c.OwnerCell] == nil {
			cache[c.OwnerCell] = map[string]struct{}{}
		}
		for _, t := range c.Triggers {
			cache[c.OwnerCell][t] = struct{}{}
		}
	}
	return cache
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

// lazyLoadEmitTopics loads or returns cached emit topics for the given cell.
func lazyLoadEmitTopics(
	cache map[string]map[string]struct{},
	ownerCell, root, fileForError string,
) (map[string]struct{}, []ValidationResult) {
	if topics, ok := cache[ownerCell]; ok {
		return topics, nil
	}
	topics, results := scanCellEmitTopics(root, ownerCell, fileForError)
	cache[ownerCell] = topics
	return topics, results
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
func (v *Validator) checkReverseEmits(
	ownerCell string,
	emitTopics map[string]struct{},
	cellTriggerCache map[string]map[string]struct{},
) []ValidationResult {
	var results []ValidationResult
	declaredTriggers := cellTriggerCache[ownerCell]
	for t := range emitTopics {
		if _, found := declaredTriggers[t]; !found {
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
// Resolution strategy:
//  1. outbox.Emit(ctx, emitter, TOPIC, ...) — third arg resolved if literal or const;
//     call expressions → dynamic-topic error.
//  2. EventType: EXPR in outbox.Entry composite literals — resolved if literal or const.
//  3. All dto/domain TopicXxx selectors anywhere in the file — catches indirect helper patterns.
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
		fileConsts := collectFileConsts(f, pkgConsts)
		scanResults, hasEmit := scanFileForEmits(f, fset, pkgConsts, fileConsts, fileForError, root, topics)
		results = append(results, scanResults...)
		// Only collect topic selectors from files that contain real emit calls.
		// Restricting to emit-call files prevents subscriber topic constants
		// (e.g. in subscribe(ctx, dto.TopicX, handler)) from being counted as
		// emit evidence, which would cause false-negatives in the reverse check.
		if hasEmit {
			collectAllTopicSelectors(f, pkgConsts, topics)
		}
		return nil
	})
	return topics, results
}

// scanFileForEmits walks a single parsed file's AST and collects emitted topics.
// Returns the validation results and a boolean indicating whether the file
// contained at least one real emit call site (outbox.Emit or receiver *.Emit).
func scanFileForEmits(
	f *ast.File,
	fset *token.FileSet,
	pkgConsts cellPkgConsts,
	fileConsts pkgConstMap,
	fileForError string,
	root string,
	topics map[string]struct{},
) ([]ValidationResult, bool) {
	var results []ValidationResult
	var hasEmit bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isOutboxEmitCall(call) && len(call.Args) >= 3 {
			hasEmit = true
			r := collectOutboxEmitTopic(call, fset, pkgConsts, fileConsts, fileForError, root, topics)
			results = append(results, r...)
			return true
		}
		if isReceiverEmitCall(call) && len(call.Args) >= 2 {
			hasEmit = true
			collectReceiverEmitTopics(call.Args[1], f, pkgConsts, fileConsts, topics)
		}
		return true
	})
	return results, hasEmit
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

// collectAllTopicSelectors scans the file for dto/domain TopicXxx selectors
// and adds resolvable ones to topics. This handles indirect patterns where
// topics are passed to helper methods rather than used directly in emit calls.
//
// Only dto/domain package qualifiers are collected; file-level string constants
// are intentionally excluded to avoid treating subscriber topic constants as emits.
func collectAllTopicSelectors(f *ast.File, pkgConsts cellPkgConsts, topics map[string]struct{}) {
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if (ident.Name == "dto" || ident.Name == "domain") && strings.HasPrefix(sel.Sel.Name, "Topic") {
			if pkgMap, ok := pkgConsts[ident.Name]; ok {
				if val, ok := pkgMap[sel.Sel.Name]; ok {
					topics[val] = struct{}{}
				}
			}
		}
		return true
	})
}
