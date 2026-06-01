package contractbuild

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
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

// NewEventDerivation projects a validated outbox.Subscription into a
// ContractSpec shape for tracing / observability consumers. It is a derivation
// funnel, NOT a declaration funnel — the spec is derived from the subscription's
// already-bound contract identity, never fabricated.
//
// Provenance is type- AND content-enforced: the parameter is a typed
// outbox.Subscription (not loose primitives), and the funnel runs
// sub.Validate() before deriving — so a caller cannot mint an event spec from
// arbitrary strings, it must supply a fully-populated, valid subscription
// (Topic / ConsumerGroup / CellID / ContractID / ContractKind / ContractTransport
// all required). This closes the provenance gap left when the single-file caller
// allowlist was retired (#1038); the typed parameter is now natural because this
// package lives in runtime/ and may import kernel/outbox (the former primitive
// signature was a vestige of the old kernel/contractspec placement).
//
// The derived spec is additionally run through ContractSpec.Validate() (defense
// in depth: e.g. ContractID need not be a valid contract ID just because the
// subscription validated). Callers MUST handle the returned error — content
// invariants are funnel-owned, not caller discipline.
func NewEventDerivation(sub outbox.Subscription) (contractspec.ContractSpec, error) {
	if err := sub.Validate(); err != nil {
		return contractspec.ContractSpec{}, fmt.Errorf("contractbuild: NewEventDerivation: subscription invalid: %w", err)
	}
	spec := contractspec.ContractSpec{
		ID:        sub.ContractID,
		Kind:      cellvocab.ContractKind(sub.ContractKind),
		Transport: sub.ContractTransport,
		Topic:     sub.Topic,
	}
	if err := spec.Validate(); err != nil {
		return contractspec.ContractSpec{}, fmt.Errorf("contractbuild: NewEventDerivation: %w", err)
	}
	return spec, nil
}
