// Package walkdepthfixtures holds typed-loadable .go fixtures for
// WALK-DEPTH-API-EACHCHILDREN-01 (tools/archtest/walk_depth_funcdecl_children_test.go).
//
// The rule bans instantiating the recursive subtree-axis walk helpers
// (scanner.EachInSubtree / EachInSubtreeStopAt, and their archtest façade
// twins) with ast.FuncDecl: a *ast.FuncDecl is always a direct child of
// *ast.File (Go forbids nested function declarations — a nested func is an
// *ast.FuncLit), so the recursive descent visits zero extra FuncDecls and the
// typed function choice is wrong (use the depth-1 EachInChildren instead;
// ai-robust.md Hard 范本 #1 "typed function choice for walk depth").
//
// These fixtures call the REAL scanner helpers so the detector resolves the
// callee by package path (the same Hard type-resolution the live dogfood
// uses), not by name string. They live under internal/ (not testdata/) so the
// typed pipeline loads them via ./tools/archtest/...; the live dogfood scopes
// to the top-level tools/archtest dir and therefore never scans them.
package walkdepthfixtures
