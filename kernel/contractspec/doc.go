// Package contractspec defines the runtime descriptor type for one
// contract endpoint, shared by the layers that bind contracts to wire
// protocols.
//
// ContractSpec is a leaf vocabulary type with no kernel→kernel
// dependencies — it is consumed by:
//
//   - kernel/cell.Registry.Subscribe (event subscription)
//   - kernel/wrapper.WrapConsumer / WrapSubscriber / HTTPHandler (decorators)
//   - runtime/auth.Mount (HTTP route binding)
//   - runtime/eventrouter (subscription routing + tracing)
//
// Extracted from kernel/wrapper to break the cell→wrapper reverse edge
// (cell.Registry referenced contractspec.ContractSpec only as a type
// signature). After the extraction kernel/wrapper sits at the top tier
// and depends only on outbox + leaves (ctxkeys, contractspec).
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
// The boundary is one-directional: cell, wrapper, runtime/* may import
// contractspec; contractspec must not import any other kernel sub-module.
// kernel/contractspec carries no runtime dependencies and is safe to
// import from any layer.
//
// ref: k8s.io/apimachinery — lightweight value types shared across
// layers without runtime parsing dependencies.
package contractspec
