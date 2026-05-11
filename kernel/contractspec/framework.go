package contractspec

import "github.com/ghbvf/gocell/kernel/cellvocab"

// NewFrameworkHTTP constructs a ContractSpec for runtime-owned HTTP
// infrastructure endpoints (health probes, devtools catalog, etc.). Kind is
// fixed as cellvocab.ContractHTTP; Transport is fixed as "http". The id
// SHOULD use the "http.framework." prefix to signal runtime-internal
// ownership (no contract.yaml source); the prefix is a convention enforced
// by code review, not at runtime.
//
// This is the ONLY legitimate construction path for ContractSpec values in
// runtime/ HTTP infrastructure code. Composite literal
// `contractspec.ContractSpec{...}` is forbidden under cells/,
// examples/*/cells/, and runtime/ by archtest NO-MANUAL-CONTRACTSPEC-LITERAL-01.
func NewFrameworkHTTP(id, method, path string) ContractSpec {
	return ContractSpec{
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
// Inputs are primitives (not outbox.Subscription) so kernel/contractspec
// stays independent of kernel/outbox; the eventrouter tracing decorator is
// the canonical caller.
func NewEventDerivation(id string, kind cellvocab.ContractKind, transport, topic string) ContractSpec {
	return ContractSpec{
		ID:        id,
		Kind:      kind,
		Transport: transport,
		Topic:     topic,
	}
}
