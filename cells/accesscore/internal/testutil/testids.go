// Package testutil provides shared test fixtures for cells/accesscore tests.
//
// IMPORTANT: This package MUST NOT be imported by production code. It is
// scoped under internal/ so only cells/accesscore/** can reach it; archtest
// rule LAYER-TESTUTIL further enforces the rule.
//
// Why a non-_test.go file: the helpers are needed across multiple sibling
// packages (cells/accesscore/slices/{identitymanage,sessionlogout,rbaccheck}/...);
// _test.go visibility cannot cross package boundaries.
//
// Verified: no production (non-_test.go) file imports this package
// (confirmed by `git grep -l "cells/accesscore/internal/testutil" -- '*.go' ':!*_test.go'`
// returning empty output).
package testutil

import "github.com/google/uuid"

var testNamespace = uuid.NewSHA1(uuid.Nil, []byte("gocell-pr-a45-test"))

// TestID returns a deterministic canonical lowercase UUID for the given label.
// Used so handler-level tests pass the UUID-validator added in PR-A45 while
// keeping the source readable (label = test fixture identity).
func TestID(label string) string {
	return uuid.NewSHA1(testNamespace, []byte(label)).String()
}
