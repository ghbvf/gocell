//go:build archtest_fixture

// Package executorlike mimics runtime/saga/executor: an UNEXPORTED safeRun
// (the taint seed, matched by name) plus an EXPORTED Wrapper — the only path by
// which a different package can transitively reach the unexported safeRun.
package executorlike

import "context"

func safeRun(_ context.Context) error { return nil }

// Wrapper is exported and reaches safeRun; a sibling package calling Wrapper
// inside a RunInTx closure is the cross-package threat A2 must catch.
func Wrapper(ctx context.Context) error {
	return safeRun(ctx)
}
