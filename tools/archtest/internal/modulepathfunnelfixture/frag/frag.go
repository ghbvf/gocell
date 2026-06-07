//go:build archtest_fixture

// Package frag is a sibling fragment package for the cross-package reconstruction
// RED fixture (red_cross_pkg.go). It exports a platform-path FRAGMENT so the
// reconstruction must be resolved across a package boundary — a form the old
// same-package AST flatten could not follow.
package frag

// Host is a platform-path FRAGMENT (a prefix OF github.com/ghbvf/gocell, not the
// bare value itself), so it is not a bare-literal violation on its own. The
// cross-package reconstruction that concatenates it back into the full path is
// caught only by the typed detector (residual (b)).
const Host = "github.com/ghbvf/"
