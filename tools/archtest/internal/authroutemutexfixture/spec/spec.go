//go:build archtest_fixture

// Package authroutemutexfixturespec is the sibling-package half of the
// AUTH-BOOTSTRAP-CLIENTS-MUTEX-01 fixture. It exposes one ContractSpec var
// (WithClients) so the main fixture package can exercise the
// cross-package-SelectorExpr coverage class.
//
// Loaded only with the archtest_fixture build tag; excluded from real-repo
// `go build ./...` and `go test ./...`. AI co-authors who modify this file
// must keep `WithClients` exported, of type contractspec.ContractSpec, and
// with a non-empty Clients literal — the companion test asserts the
// resulting cross-package SelectorExpr violation is detected.
package authroutemutexfixturespec

import "github.com/ghbvf/gocell/kernel/contractspec"

// WithClients is referenced from the sibling fixture package's auth.Route
// composite literal as `spec.WithClients`. The static detector must resolve
// the SelectorExpr to this package-level *types.Var and observe its
// non-empty Clients field.
var WithClients = contractspec.ContractSpec{
	ID:        "http.fixture.crosspkg.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/internal/v1/crosspkg",
	Clients:   []string{"sibling-caller"},
}
