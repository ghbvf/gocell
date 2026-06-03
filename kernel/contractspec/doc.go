// Package contractspec defines the runtime descriptor type for one
// contract endpoint, shared by the layers that bind contracts to wire
// protocols.
//
// ContractSpec is consumed by:
//
//   - kernel/cell.Registrar.Subscribe (event subscription)
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
//  2. runtime/internal/contractbuild.NewFrameworkHTTP — runtime-owned HTTP
//     infrastructure endpoints (health probes, devtools catalog, etc.); the
//     only legitimate construction path for framework ContractSpec values in
//     runtime/ HTTP infra code. Content invariant Hard (frameworkHTTPIDPrefix
//     A-class panic); upstream Hard — the runtime/internal/ placement makes the
//     Go compiler refuse imports from outside the runtime/ subtree, so business
//     code (cells/, examples/, cmd/, adapters/) cannot call it.
//  3. runtime/internal/contractbuild.NewEventDerivation — tracing/observability
//     projection of a validated outbox.Subscription; returns (ContractSpec,
//     error) with sub.Validate() + ContractSpec.Validate() embedded inside the
//     funnel (shape-Hard, NOT provenance-Hard). Upstream caller-set Hard via the
//     runtime/internal/ placement; the typed Subscription parameter forces shape
//     but does NOT type-enforce value provenance (outbox.Subscription has exported
//     fields — a runtime/ caller can fabricate one), so provenance is a runtime
//     data-flow property (eventrouter), Hard-ifiable only by a sealed Subscription
//     constructor (gh #1532). See contractbuild/doc.go grading.
//  4. runtime/internal/contractbuild.NewWebhookDispatch — derivation of the
//     event-kind subscription spec from a validated webhook.DispatchSpec; returns
//     (ContractSpec, error) with spec.Validate() + ContractSpec.Validate()
//     embedded inside the funnel (shape-Hard, NOT provenance-Hard). Upstream
//     caller-set Hard via runtime/internal/ placement (compiler refuses imports
//     from outside runtime/); the typed webhook.DispatchSpec parameter forces
//     shape but does NOT type-enforce value provenance (exported fields), so
//     provenance is a runtime data-flow property (cellgen→RegistrySnapshot),
//     Hard-ifiable only by a sealed DispatchSpec constructor (gh #1532). See
//     contractbuild/doc.go grading.
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
