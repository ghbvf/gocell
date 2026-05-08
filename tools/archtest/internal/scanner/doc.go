// Package scanner provides shared walk + parse + report primitives for
// tools/archtest scanners. All operations are fail-closed by construction:
//
//   - [Scope] zero value is rejected; must be created via [ModuleScope] or [DirsScope].
//   - [EachFile] treats any parse error as a fatal test failure (no silent fallback).
//   - [Scope.Files] returns an error on any walk failure.
//   - [Report] deduplicates and sorts diagnostics before emitting t.Errorf calls.
//
// # Choosing between ImportBan and EachFile
//
// Use [ImportBan] when the entire invariant is "file must not import package X".
// Use [EachFile] with [Report] for custom AST patterns such as struct literal
// field checks, argument value constraints, or multi-node correlations.
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
// ref: golang.org/x/tools/go/analysis analysis.go — Analyzer.RunDespiteErrors=false default (fail-closed)
// ref: kubernetes/kubernetes test/typecheck/main.go ignoredPaths — driver-level skip set
// ref: golangci-lint pkg/golinters/depguard — high-level import-ban encapsulation
// ref: golang.org/x/tools/go/packages packages.go — NeedSyntax + Errors collection
package scanner
