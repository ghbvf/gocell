// Package scanner provides shared walk + parse + report primitives for
// tools/archtest scanners. All operations are fail-closed by construction:
//
//   - [Scope] zero value is rejected; must be created via [ModuleScope] or [DirsScope].
//   - [EachFile] treats any parse error as a fatal test failure (no silent fallback).
//   - [Scope.Files] returns an error on any walk failure.
//   - [Report] deduplicates and sorts diagnostics before emitting t.Errorf calls.
//
// # Choosing between ImportBan, EachFile, and EachNode
//
// Use [ImportBan] when the entire invariant is "file must not import package X".
//
// Use [EachFile] with [Report] for custom AST patterns: combine with
// [EachNode] for typed node iteration so the per-node-kind handler is
// statically constrained to the right *ast.<NodeKind> type.
//
// Use [EachNode] inside any rule that iterates AST sub-trees. Bare
// [go/ast.Inspect], [go/ast.Walk], [go/ast.Preorder], and
// [golang.org/x/tools/go/ast/inspector] APIs are forbidden in
// tools/archtest/*_test.go (enforced by SCANNER-FRAMEWORK-USAGE-01); the
// generic [EachNode] funnel makes "wrong node kind" a compile error rather
// than a silent runtime miss — critical for AI-rebust archtest authoring
// (see .claude/rules/gocell/ai-collab.md AI-rebust 三档分级).
//
// # Why not go/analysis
//
// GoCell archtests have no inter-rule dependencies (no Requires/FactType DAG),
// so the lighter AST scanner API is preferred over go/analysis.Analyzer.
//
// # Typical usage
//
//	root, err := scanner.FindModuleRoot(...)  // or use findModuleRoot testing helper
//	scanner.ImportBan{
//	    RuleID:    "MY-RULE-01",
//	    Forbidden: []string{"forbidden/path"},
//	}.Run(t, scanner.ModuleScope(root))
//
//	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
//	    scanner.EachNode[ast.CallExpr](fc.File, func(call *ast.CallExpr) {
//	        // call is *ast.CallExpr — typed by Go's generic constraint
//	    })
//	})
//
// ref: go/ast.Preorder@go1.23 — stdlib typed iteration for [EachNode]
// ref: golang.org/x/tools/go/analysis analysis.go — Analyzer.RunDespiteErrors=false default (fail-closed)
// ref: kubernetes/kubernetes test/typecheck/main.go ignoredPaths — driver-level skip set
// ref: golangci-lint pkg/golinters/depguard — high-level import-ban encapsulation
// ref: golang.org/x/tools/go/packages packages.go — NeedSyntax + Errors collection
package scanner
