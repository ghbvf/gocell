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
// Cells MUST NOT construct ContractSpec literals directly. The only
// valid construction site is `generated/contracts/**/spec_gen.go`
// (private `var spec`); subscription/route mounting goes through the
// generated `NewSubscription` / `NewHandler` adapters in
// `generated/contracts/**`. Three archtest gates enforce this:
//
//   - CELLS-NO-CONTRACTSPEC-IMPORT-01
//   - NO-MANUAL-CONTRACTSPEC-LITERAL-01
//   - EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
//
// ref: k8s.io/apimachinery — lightweight value types shared across
// layers without runtime parsing dependencies.
package contractspec
