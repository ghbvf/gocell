// Package scanner provides shared walk + parse + report primitives for
// tools/archtest scanners. All operations are fail-closed by construction:
//
//   - [Scope] zero value is rejected; must be created via [ModuleScope] or [DirsScope].
//   - [EachFile] treats any parse error as a fatal test failure (no silent fallback).
//   - [Scope.Files] returns an error on any walk failure.
//   - [Report] deduplicates and sorts diagnostics before emitting t.Errorf calls.
//
// # Choosing between ImportBan, EachFile, EachInSubtree, and EachInChildren
//
// Use [ImportBan] when the entire invariant is "file must not import package X".
//
// Use [EachFile] with [Report] for custom AST patterns: combine with
// [EachInSubtree] or [EachInChildren] for typed node iteration so the
// per-node-kind handler is statically constrained to the right
// *ast.<NodeKind> type.
//
// Use [EachInSubtree] / [EachInChildren] inside any rule that iterates AST
// nodes. Bare [go/ast.Inspect], [go/ast.Walk], [go/ast.Preorder], and
// [golang.org/x/tools/go/ast/inspector] APIs are forbidden in
// tools/archtest/*_test.go (enforced by SCANNER-FRAMEWORK-USAGE-01); the
// generic typed funnels make "wrong node kind" a compile error rather than
// a silent runtime miss — critical for AI-rebust archtest authoring
// (see .claude/rules/gocell/ai-collab.md AI-rebust 三档分级).
//
// # Choosing walk depth: EachInSubtree vs EachInChildren
//
// Walk depth is a compile-time choice — picking the wrong API shows at the
// call site, not in runtime AST drift:
//
//   - [EachInSubtree]: recursive over the full sub-tree (root + every
//     descendant). For "any FuncDecl in the file" / "any IfStmt anywhere in
//     fn.Body" style rules.
//   - [EachInChildren]: depth-1 only. For "container's direct elements" —
//     KeyValueExpr of CompositeLit, CaseClause of SwitchStmt.Body,
//     CommClause of SelectStmt.Body, top-level Decl of *ast.File.
//
// Both silently no-op on nil root; callers need not guard.
//
// # Subpackage scan exemption
//
// SCANNER-FRAMEWORK-USAGE-01 only scans top-level archtest test files
// (tools/archtest/<file>_test.go). Files under tools/archtest/internal/...
// (this package, typeseval, etc.) are exempt by design — they ARE the
// framework and may legitimately use bare go/ast walks. AI authors must NOT
// extend "subpackage exempt" reasoning to other tools/archtest/internal/
// helpers: only the framework implementation files (this package) and the
// stdlib type-checker plumbing (typeseval) qualify.
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
//	    scanner.EachInSubtree[ast.CallExpr](fc.File, func(call *ast.CallExpr) {
//	        // call is *ast.CallExpr — typed by Go's generic constraint
//	    })
//	})
//
// ref: go/ast.Preorder@go1.23 — stdlib typed iteration for [EachInSubtree]
// ref: go/ast.Walk — stdlib Visitor pattern for [EachInChildren]
// ref: golang.org/x/tools/go/analysis analysis.go — Analyzer.RunDespiteErrors=false default (fail-closed)
// ref: kubernetes/kubernetes test/typecheck/main.go ignoredPaths — driver-level skip set
// ref: golangci-lint pkg/golinters/depguard — high-level import-ban encapsulation
// ref: golang.org/x/tools/go/packages packages.go — NeedSyntax + Errors collection
package scanner
