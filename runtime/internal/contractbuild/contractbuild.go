package contractbuild

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// frameworkHTTPIDPrefix is the required prefix for IDs passed to
// NewFrameworkHTTP. The prefix signals runtime-internal ownership and
// distinguishes framework infra specs from business contracts (which use
// kind.domain.v1 style IDs and live in generated/contracts/).
//
// Enforcement is Hard: NewFrameworkHTTP panics at process start when the
// prefix is absent. All call sites use static string literals, so the panic
// fires during initialization, not during request handling. The constant is
// unexported because every caller passes a literal ID, never the constant.
const frameworkHTTPIDPrefix = "http.framework."

// NewFrameworkHTTP constructs a ContractSpec for runtime-owned HTTP
// infrastructure endpoints (health probes, devtools catalog, etc.). Kind is
// fixed as cellvocab.ContractHTTP; Transport is fixed as "http".
//
// The id MUST start with "http.framework.". This constraint is enforced at
// construction time with a panic (A-class assertion), not merely by code
// review. All legitimate call sites use static string literals so the panic
// fires at process initialization.
//
// This is the ONLY legitimate construction path for ContractSpec values in
// runtime/ HTTP infrastructure code. Composite literal
// `contractspec.ContractSpec{...}` is forbidden under cells/,
// examples/*/cells/, and runtime/ by archtest NO-MANUAL-CONTRACTSPEC-LITERAL-01.
// The package lives under runtime/internal/ so the Go compiler refuses imports
// from outside the runtime/ subtree — business code physically cannot call
// this funnel (see doc.go for the compiler-Hard upstream rationale).
func NewFrameworkHTTP(id, method, path string) contractspec.ContractSpec {
	if !strings.HasPrefix(id, frameworkHTTPIDPrefix) {
		panic(panicregister.Approved(
			"contractspec-framework-id-prefix",
			errcode.Assertion("NewFrameworkHTTP id must start with the framework prefix %q, got %q", frameworkHTTPIDPrefix, id),
		))
	}
	return contractspec.ContractSpec{
		ID:        id,
		Kind:      cellvocab.ContractHTTP,
		Transport: "http",
		Method:    method,
		Path:      path,
	}
}

// NewEventDerivation projects already-validated event metadata into a
// ContractSpec shape for tracing / observability consumers. It is a
// derivation funnel, NOT a declaration funnel — callers MUST NOT fabricate
// new specs through this path; the source metadata must already pass
// upstream validation (typically outbox.Subscription.Validate()).
//
// Validation is enforced at construction time: the funnel runs
// ContractSpec.Validate() before returning and wraps any failure as a
// derivation error. Callers MUST handle the returned error — content
// invariants are funnel-owned, not caller discipline.
//
// Inputs are primitives (not outbox.Subscription) so this package stays
// independent of kernel/outbox; the eventrouter tracing decorator is the
// canonical caller. The runtime/internal/ placement makes the upstream gate
// compiler-Hard (non-runtime packages cannot import this funnel), so no
// caller-allowlist archtest is needed (see doc.go).
func NewEventDerivation(id string, kind cellvocab.ContractKind, transport, topic string) (contractspec.ContractSpec, error) {
	spec := contractspec.ContractSpec{
		ID:        id,
		Kind:      kind,
		Transport: transport,
		Topic:     topic,
	}
	if err := spec.Validate(); err != nil {
		return contractspec.ContractSpec{}, fmt.Errorf("contractbuild: NewEventDerivation: %w", err)
	}
	return spec, nil
}
