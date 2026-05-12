// Package contractspec defines the runtime descriptor type for one
// contract endpoint, shared by the layers that bind contracts to wire
// protocols.
//
// ContractSpec is consumed by:
//
//   - kernel/cell.Registry.Subscribe (event subscription)
//   - kernel/wrapper.WrapConsumer / WrapSubscriber / HTTPHandler (decorators)
//   - runtime/auth.Mount (HTTP route binding)
//   - runtime/eventrouter (subscription routing + tracing)
//   - runtime/http/router (route attribution + cell label)
//
// Extracted from kernel/wrapper to break the cell→wrapper reverse edge.
// After the extraction kernel/wrapper sits at the top tier and depends
// only on outbox + leaves (ctxkeys, contractspec). contractspec imports
// kernel/cellvocab for the ContractKind type and InternalPathPrefix
// constant (single source of truth, no lockstep duplication).
//
// Cells MUST NOT construct ContractSpec literals directly. The valid
// construction sites are:
//
//  1. generated/contracts/**/spec_gen.go (private `var spec`) — business
//     contracts produced by contractgen codegen; subscription/route mounting
//     goes through the generated NewSubscription / NewHandler adapters.
//  2. kernel/contractspec.NewFrameworkHTTP — runtime-owned HTTP infrastructure
//     endpoints (health probes, devtools catalog, etc.); the only legitimate
//     construction path for ContractSpec values in runtime/ HTTP infra code.
//  3. kernel/contractspec.NewEventDerivation — tracing/observability projections
//     of already-validated event metadata; returns (ContractSpec, error) with
//     Validate() embedded inside the funnel. Closed to a single caller
//     (runtime/eventrouter/contract_tracing_subscriber.go) by archtest
//     NO-MANUAL-CONTRACTSPEC-LITERAL-01; no other production file may invoke
//     this funnel.
//
// Three archtest gates enforce this invariant:
//
//   - CELLS-NO-CONTRACTSPEC-IMPORT-01
//   - NO-MANUAL-CONTRACTSPEC-LITERAL-01
//   - EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
//
// ref: k8s.io/apimachinery — lightweight value types shared across
// layers without runtime parsing dependencies.
package contractspec
