//go:build archtest_fixture

// Package reddotimport is a RED fixture for PROD-MAIN-WIRING-NOOP-REJECT-01
// blind-spot self-test (dot-import): it dot-imports kernel/outbox and constructs
// NoopWriter{} as a bare identifier (no SelectorExpr). It proves the detector's
// ast.Ident walk catches the dot-import form, not just the qualified
// outbox.NoopWriter form. The bare Writer type reference must NOT be flagged.
// Expect one diagnostic.
package reddotimport

import . "github.com/ghbvf/gocell/framework/kernel/outbox"

var _ Writer = NoopWriter{}
