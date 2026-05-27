//go:build archtest_fixture

// Package reddotimportrand is a RED fixture for SAGA-EXECUTOR-RAND-INJECTED-01
// blind spot B2 (dot-import): it dot-imports math/rand/v2 and calls the
// package-level global Int64N as a bare Ident (no SelectorExpr). This proves
// the detector's ast.Ident walk catches the dot-import form, not just the
// qualified rand.Int64N form. Expect one diagnostic.
package reddotimportrand
