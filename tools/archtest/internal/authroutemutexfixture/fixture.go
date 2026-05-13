//go:build archtest_fixture

// Package authroutemutexfixture is a deliberate AUTH-BOOTSTRAP-CLIENTS-MUTEX-01
// negative fixture loaded only when the archtest_fixture build tag is set.
//
// The fixture enumerates every Contract-expression shape that the type-aware
// detector must catch. Each violation case binds a non-nil BootstrapAuth
// alongside a ContractSpec value whose Clients field is non-empty:
//
//  1. file-scope-var-DETECTED — `var specFileScope = ContractSpec{Clients:[...]}`
//     referenced as `Contract: specFileScope` from a sibling Route literal in
//     the same file.
//  2. inline-literal-DETECTED — inline `Contract: contractspec.ContractSpec{Clients:[...]}`.
//  3. funcbody-local-DETECTED — `specLocal := ContractSpec{Clients:[...]}` inside
//     a function body, referenced by `Contract: specLocal` in the same scope.
//  4. cross-package-SelectorExpr-DETECTED — `Contract: spec.WithClients` where
//     `spec` is a sibling fixture package.
//
// Two CLEAN cases must NOT trigger: BootstrapAuth without Clients (legitimate
// setup/admin pattern) and Clients without BootstrapAuth (legitimate internal
// route pattern). The companion test asserts hits == 4 to prevent both false
// negatives and over-eager detection.
//
// AI co-author guidance:
//
//   - Adding a new DETECTED case requires updating the companion test's
//     wantSubstr slice AND raising the required hit count.
//   - Removing or weakening any DETECTED case is a coverage regression and
//     must be accompanied by an ADR explaining why the corresponding
//     production shape is no longer possible.
//   - The fixture compiles under the archtest_fixture build tag only; do not
//     remove the build constraint, and do not introduce side effects (Mount
//     calls, init() bodies) — composite literals alone provide the AST and
//     type information the detector needs.
package authroutemutexfixture

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/runtime/auth"
	spec "github.com/ghbvf/gocell/tools/archtest/internal/authroutemutexfixture/spec"
)

// bootstrapAuth is a no-op middleware kept solely so the auth.Route literals
// below have a non-nil BootstrapAuth value. The function is never invoked at
// runtime — the fixture exists for AST/type-info analysis.
func bootstrapAuth(next http.Handler) http.Handler { return next }

// noopHandler keeps the Handler field non-nil so auth.Route literals are
// syntactically legitimate. Like bootstrapAuth, it is never invoked.
var noopHandler http.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

// specFileScope is the file-scope-var DETECTED case. Non-empty Clients +
// referenced by routeFileScopeVarViolation below.
var specFileScope = contractspec.ContractSpec{
	ID:        "http.fixture.filescope.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/internal/v1/filescope",
	Clients:   []string{"filescope-caller"},
}

// specClean is the BootstrapAuth-without-Clients CLEAN case backing.
// Empty Clients + setup/admin path — the legitimate bootstrap pattern.
var specClean = contractspec.ContractSpec{
	ID:        "http.fixture.clean.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "POST",
	Path:      "/api/v1/fixture/setup/admin",
}

// Case 1 (DETECTED): file-scope-var.
var routeFileScopeVarViolation = auth.Route{
	BootstrapAuth: bootstrapAuth,
	Handler:       noopHandler,
	Contract:      specFileScope,
}

// Case 2 (DETECTED): inline ContractSpec composite literal.
var routeInlineLiteralViolation = auth.Route{
	BootstrapAuth: bootstrapAuth,
	Handler:       noopHandler,
	Contract: contractspec.ContractSpec{
		ID:        "http.fixture.inline.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "GET",
		Path:      "/internal/v1/inline",
		Clients:   []string{"inline-caller"},
	},
}

// Case 4 (DETECTED): cross-package SelectorExpr — references spec.WithClients
// declared in the sibling authroutemutexfixturespec package.
var routeCrossPackageSelectorViolation = auth.Route{
	BootstrapAuth: bootstrapAuth,
	Handler:       noopHandler,
	Contract:      spec.WithClients,
}

// Case bootstrap-without-clients (CLEAN): must NOT trigger.
var routeBootstrapWithoutClientsClean = auth.Route{
	BootstrapAuth: bootstrapAuth,
	Handler:       noopHandler,
	Contract:      specClean,
}

// Case clients-without-bootstrap (CLEAN): must NOT trigger. Same Clients-
// bearing spec as Case 1, but no BootstrapAuth — the legitimate internal
// route pattern.
var routeClientsWithoutBootstrapClean = auth.Route{
	Handler:  noopHandler,
	Contract: specFileScope,
}

// funcbodyLocalDetected is Case 3 (DETECTED): a function-body-local
// ContractSpec built via `:=` whose binding still resolves through
// pkg.TypesInfo.Defs to a canonical *types.Var.
func funcbodyLocalDetected() {
	specLocal := contractspec.ContractSpec{
		ID:        "http.fixture.funcbodylocal.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "GET",
		Path:      "/internal/v1/funcbodylocal",
		Clients:   []string{"funcbody-caller"},
	}
	_ = auth.Route{
		BootstrapAuth: bootstrapAuth,
		Handler:       noopHandler,
		Contract:      specLocal,
	}
}
