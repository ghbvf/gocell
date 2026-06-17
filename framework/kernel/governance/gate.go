package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// RegistrationGate is the runtime governance gate for the contract registry
// (303-US3, #2234). It wraps the existing kernel/governance Validator — today
// only reachable via the `gocell validate` CLI — as a K8s AdmissionResponse-style
// {allowed, result, warnings} decision, exposing two entry points that share one
// validation path (mirroring Confluent Schema Registry's /compatibility (dry-run)
// vs /versions (write) split):
//
//   - Check  : dry-run. Validates a candidate contract and returns the verdict
//     WITHOUT persisting. Side-effect free (K8s SideEffects: NoneOnDryRun).
//   - Submit : validate-before-persist. Runs the SAME validation; only when
//     Allowed does it record the registration via the registrar's Submit entry.
//     A contract that fails the gate never reaches the `submitted` state — there
//     is no "submitted but invalid" intermediate state.
//
// # FailurePolicy=Fail (fail-closed)
//
// The gate never fails open. It denies (Allowed=false) when it cannot fully and
// safely evaluate a request: an invalid tenant (FR-002 "缺租户 → deny"), a
// validation run that is interrupted (ctx canceled) or panics
// (ReasonValidatorUnavailable), or any governance error in the candidate
// (ReasonValidationFailed). The store (registrar) is a required construction
// dependency (nil → fail-fast in NewRegistrationGate); a live-backend
// "unavailable" deny path lands with the PG-backed store in US5. Mirrors K8s
// ValidatingWebhook FailurePolicy=Fail.
//
// # Validation scope — single candidate contract
//
// The gate validates ONE submitted contract in isolation: it builds a
// single-contract ProjectMeta and runs the CURATED subset of governance rules
// that validate a contract's DECLARATION (CH-01 ownerCell, CH-02 lifecycle,
// CH-03 HTTP schemaRefs, REG-01 runtime fanout completeness). It deliberately
// does NOT run the rules that validate in-tree IMPLEMENTATION artifacts — REF/
// TOPO cross-references (a runtime contract has no sibling cells/slices in the
// gate's project), CH-04/CH-05 handler-file scans (the handler is out-of-process),
// CH-06/CH-07 codegen, VERIFY test-file closure. Those are structurally
// inapplicable to an out-of-process runtime contract: that inapplicability is the
// very "扇出 archtest vacuous for runtime contracts" the epic-303 ADR documents,
// and REG-01 (runtimeFanoutCompleteness) is its equivalent runtime compensation.
//
// # AI-robust grading (FR-013 — honest, NOT disguised as Hard)
//
// The gate VERDICT ("does this runtime contract satisfy governance?") is a
// runtime guard = MEDIUM. A runtime-submitted contract does not exist at compile
// time, so its validity cannot be a compile fact — this is the dual-track ceiling
// the epic-303 ADR mandates be graded Medium, not faked as Hard (threat T10).
// GateReason / GovernanceGateResult value sets ARE Hard (sealed unexported field +
// accessor funcs + frozen-registry test). The structural invariant "submitted is
// only reachable through this gate" is locked at MEDIUM by the caller-funnel
// archtest REGISTRAR-SUBMIT-CALLER-01 (only this package may call
// registry.ContractRegistrar.Submit in production). A genuinely-Hard
// sealed-construction token funnel is blocked by kernel layering — a token sealed
// in registry cannot be minted by governance, and registry cannot import
// governance (cycle) — so the caller-funnel is the documented Go ceiling, same
// family as COMMAND-ASYNC-EMIT-CALLER-01.
//
// RegistrationGate is safe for concurrent Check/Submit: it builds a fresh
// Validator per request (the Validator itself is not concurrency-safe) and the
// registrar is internally locked.
type RegistrationGate struct {
	store *registry.ContractRegistrar
	clk   clock.Clock
}

// NewRegistrationGate builds a gate over the given registrar. store and clk are
// required strong dependencies and fail-fast when nil (a gate without a store
// cannot persist, and the clock stamps validation; a nil dep is a wiring bug,
// not a degraded mode). clk is a positional dependency per the clock.Clock
// convention.
func NewRegistrationGate(store *registry.ContractRegistrar, clk clock.Clock) *RegistrationGate {
	clock.MustHaveClock(clk, "governance.NewRegistrationGate")
	if store == nil {
		panic(panicregister.Approved("registration-gate-nil-store",
			errcode.Assertion("governance.NewRegistrationGate: store (*registry.ContractRegistrar) is required (nil rejected); "+
				"pass a constructed registrar at the composition root")))
	}
	return &RegistrationGate{store: store, clk: clk}
}

// Check is the dry-run entry point (no persistence). It returns the gate verdict
// for candidate under tenant tnt, leaving the registry untouched. A wire-compliant
// contract with non-blocking advisories returns {Allowed:true, Warnings:[…]}.
func (g *RegistrationGate) Check(ctx context.Context, tnt tenant.TenantID, candidate *metadata.ContractMeta) GovernanceGateResult {
	return g.evaluate(ctx, tnt, candidate)
}

// Submit is the validate-before-persist entry point. It runs the SAME validation
// as Check; only when the verdict is Allowed does it record the registration via
// the registrar (entering the sealed state machine at `submitted`). A denied
// candidate is never persisted (no "submitted but invalid" intermediate state),
// and the returned result carries the machine-readable Reason.
//
// On a store-side error the gate stays fail-closed: a duplicate id maps to
// ReasonDuplicate, any other store error to ReasonValidationFailed; the store
// error is also returned so callers can inspect it. submitter is the audit
// identity recorded against the registration.
func (g *RegistrationGate) Submit(
	ctx context.Context, tnt tenant.TenantID, candidate *metadata.ContractMeta, submitter string,
) (registry.ContractRegistration, GovernanceGateResult, error) {
	res := g.evaluate(ctx, tnt, candidate)
	if !res.Allowed {
		return registry.ContractRegistration{}, res, nil
	}
	reg, err := g.store.Submit(registry.SubmitInput{
		ID:        candidate.ID,
		Kind:      candidate.Kind,
		Submitter: submitter,
		// PayloadSchema is opaque to the state machine and optional at US3; the
		// concrete schema artifact/hash is wired with the persisting store (US5).
	})
	if err != nil {
		return registry.ContractRegistration{}, denyForStoreError(res, err), err
	}
	return reg, res, nil
}

// evaluate is the single validation path shared by Check and Submit. It is total
// (never panics out) and fail-closed: any condition under which the gate cannot
// safely conclude "allowed" yields Allowed=false with a machine-readable Reason.
func (g *RegistrationGate) evaluate(ctx context.Context, tnt tenant.TenantID, candidate *metadata.ContractMeta) GovernanceGateResult {
	if err := tnt.Validate(); err != nil {
		return GovernanceGateResult{Allowed: false, Reason: reasonTenantInvalid}
	}
	if candidate == nil {
		return GovernanceGateResult{Allowed: false, Reason: reasonValidationFailed}
	}
	findings, err := g.runRules(ctx, candidate)
	if err != nil {
		return GovernanceGateResult{Allowed: false, Reason: reasonValidatorUnavailable}
	}
	errs := FilterErrors(findings)
	warns := FilterWarnings(findings)
	if len(errs) > 0 {
		return GovernanceGateResult{Allowed: false, Result: errs, Warnings: warns, Reason: reasonValidationFailed}
	}
	return GovernanceGateResult{Allowed: true, Warnings: warns, Reason: reasonAllowed}
}

// runRules builds a single-contract Validator and runs the curated intrinsic
// declaration rule set (see RegistrationGate godoc §"Validation scope"). It
// honors ctx cancellation between rules (→ validator-unavailable fail-closed) and
// recovers any rule panic into a non-nil error so the gate denies rather than
// crashes (FailurePolicy=Fail).
func (g *RegistrationGate) runRules(ctx context.Context, candidate *metadata.ContractMeta) (findings []ValidationResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			findings = nil
			err = errcode.Assertion("governance: registration gate validation panicked")
		}
	}()
	v := NewValidator(singleContractProject(candidate), "", g.clk)
	rules := []func() []ValidationResult{
		v.checkCH01, v.checkCH02, v.checkCH03,
		v.runtimeFanoutCompleteness, v.runtimeRegistrationAdvisories,
	}
	for _, run := range rules {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		findings = append(findings, run()...)
	}
	return findings, nil
}

// singleContractProject wraps one candidate contract in an otherwise-empty
// ProjectMeta. Empty sibling maps make every cross-reference rule (REF/TOPO/
// VERIFY/DEP) naturally vacuous, so only the contract's intrinsic rules fire.
func singleContractProject(c *metadata.ContractMeta) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{c.ID: c},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// denyForStoreError maps a registrar Submit error to a fail-closed verdict,
// preserving the (passing) validation findings for context. A duplicate id is a
// conflict (the contract was valid, just already registered); any other store
// error is treated as a validation failure.
func denyForStoreError(prior GovernanceGateResult, err error) GovernanceGateResult {
	reason := reasonValidationFailed
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Code == errcode.ErrRegistrationDuplicate {
		reason = reasonDuplicate
	}
	return GovernanceGateResult{Allowed: false, Result: prior.Result, Warnings: prior.Warnings, Reason: reason}
}

// runtimeFanoutCompleteness is the REG-01 runtime fanout-completeness rule — the
// register-time compensation for the in-tree reverse-coverage archtests
// (DEAD-CONTRACT-01 / IMPL-DECL-COVER-01 / EMIT-DECL-COVER-01 / DEAD-CODE-01)
// being vacuous over runtime-registered contracts (epic-303 ADR §"与扇出 archtest
// 的关系"). For the candidate it asserts the contract carries an identity
// (id/kind), a provider endpoint (publisher/server/handler/provider per kind), and
// — for kinds with an in-repo consumer set (event/command/projection/webhook) — at
// least one consumer, mirroring kernel/registry ContractRegistry.Provider /
// Consumers kind dispatch.
//
// It is deliberately NOT named validate*/checkDEP*/checkCH* so the
// GOVERNANCE-RULES-REGISTRATION-GUARD-01 orphan check does not require it in
// allRules: REG-01 is a gate-only rule, never run by `gocell validate` /
// `gocell check`, so it cannot regress in-tree static governance (FR-004). It
// still emits through the locator constructor funnel with a rulecodes.go const
// (GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01 / -ERROR-FIX-FIELD-01).
func (v *Validator) runtimeFanoutCompleteness() []ValidationResult {
	var out []ValidationResult
	for _, c := range v.sortedContracts() {
		out = append(out, v.fanoutFindingsFor(c)...)
	}
	return out
}

// fanoutFindingsFor returns the REG-01 findings for a single contract.
func (v *Validator) fanoutFindingsFor(c *metadata.ContractMeta) []ValidationResult {
	var out []ValidationResult
	if strings.TrimSpace(c.ID) == "" {
		out = append(out, v.newError(codeREG01, IssueRequired, c.File, "id",
			"runtime contract is missing an id",
			"set the contract id before submitting it for registration"))
	}
	if strings.TrimSpace(c.Kind) == "" {
		out = append(out, v.newError(codeREG01, IssueRequired, c.File, "kind",
			"runtime contract is missing a kind",
			"set the contract kind (http/event/command/projection/webhook/grpc/saga)"))
		return out // kind unknown → provider/consumer dispatch is undefined
	}
	if provider, err := v.contracts.Provider(c.ID); err != nil || strings.TrimSpace(provider) == "" {
		out = append(out, v.newError(codeREG01, IssueRequired, c.File, fanoutProviderField(c.Kind),
			fmt.Sprintf("runtime contract %q (kind %q) is missing its provider endpoint — fanout incomplete", c.ID, c.Kind),
			"declare the provider endpoint so the registered contract has a producer"))
	}
	if fanoutRequiresConsumer(c.Kind) {
		if consumers, err := v.contracts.Consumers(c.ID); err != nil || len(consumers) == 0 {
			out = append(out, v.newError(codeREG01, IssueRequired, c.File, fanoutConsumerField(c.Kind),
				fmt.Sprintf("runtime contract %q (kind %q) has no consumer — dead contract, fanout incomplete", c.ID, c.Kind),
				"declare at least one consumer (subscriber/invoker/reader/receiver) or retire the contract"))
		}
	}
	return out
}

// runtimeRegistrationAdvisories emits the gate's non-blocking registration
// warnings (REG-02). Today it surfaces one advisory: submitting a contract whose
// lifecycle is already "deprecated" is allowed, but warned — mirroring the
// canonical K8s AdmissionResponse warning for a deprecated API version
// (research.md §2.2). Like runtimeFanoutCompleteness it is gate-only (not a
// validate*/checkDEP*/checkCH* method, not in allRules) so it never runs in
// `gocell validate` / `gocell check` (FR-004).
func (v *Validator) runtimeRegistrationAdvisories() []ValidationResult {
	var out []ValidationResult
	for _, c := range v.sortedContracts() {
		if c.Lifecycle == "deprecated" {
			out = append(out, v.newWarning(codeREG02, IssueForbidden, c.File, "lifecycle",
				fmt.Sprintf("runtime contract %q is being registered with lifecycle %q", c.ID, c.Lifecycle),
				"register an active (non-deprecated) version of this contract, or confirm the deprecated registration is intentional"))
		}
	}
	return out
}

// fanoutProviderField names the YAML field carrying the provider for a kind, for
// the REG-01 finding's Field anchor (mirrors ContractMeta.ProviderEndpoint).
func fanoutProviderField(kind string) string {
	switch kind {
	case "event":
		return "endpoints.publisher"
	case "command":
		return "endpoints.handler"
	case "projection":
		return "endpoints.provider"
	case "webhook":
		return "ownerCell"
	default: // http, grpc, saga
		return "endpoints.server"
	}
}

// fanoutRequiresConsumer reports whether a kind has an in-repo consumer set whose
// emptiness means a dead contract. http/grpc consumers (clients) may legitimately
// be external/out-of-process and saga has no consumer endpoints, so those kinds
// are exempt from the consumer-completeness arm.
func fanoutRequiresConsumer(kind string) bool {
	switch kind {
	case "event", "command", "projection", "webhook":
		return true
	default:
		return false
	}
}

// fanoutConsumerField names the YAML field carrying the consumer set for a kind,
// for the REG-01 finding's Field anchor.
func fanoutConsumerField(kind string) string {
	switch kind {
	case "event":
		return "endpoints.subscribers"
	case "command":
		return "endpoints.invokers"
	case "projection":
		return "endpoints.readers"
	case "webhook":
		return "endpoints.receivers"
	default:
		return "endpoints"
	}
}
