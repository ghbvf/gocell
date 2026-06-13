package governance

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// validateFRAMEWORKOWNEDCONTRACTSCOPED01 governs framework-owned contracts —
// contracts whose ownerCell is metadata.FrameworkOwnerSentinel ("_framework").
//
// Framework ownership exists for neutral, provider-agnostic wire contracts whose
// correctness requires interchangeable providers (the cert-manager
// CertificateRequest / SPIFFE Workload-API / k8s CSI·Gateway-API pattern: the
// contract is owned by the framework/control-plane, never by a single consumer
// cell). A framework-owned contract resolves ContractOwner.Cell() to ok=false,
// which structurally excludes it from the cell-owner reference rules
// (REF-03 owner-is-a-cell, REF-13 provider-is-a-cell/actor) and from the
// cell-slice emit-coupling rule (CONTRACT-CONSISTENCY-EMIT-01, whose
// triggers↔outbox.Emit coupling assumes a serving cell slice the framework
// contract does not have).
//
// This rule is the EQUIVALENT framework-side governance that makes that
// structural exclusion a re-route, not a hole: a contract that opts into
// `ownerCell: _framework` to dodge cell governance lands here instead. Two
// constraints:
//
//  1. Eligible kind: only http and event contracts may be framework-owned —
//     the neutral provider-agnostic wire surfaces. command/projection/saga/
//     webhook/grpc carry cell-internal semantics (a dispatch handler, a read
//     model, an orchestrator, a receiver, a service) and MUST name a cell owner.
//     New kinds are admitted by extending this allow-set in a later PR (and its
//     red case), so an un-vetted kind is fail-closed today.
//
//  2. Fail-closed lifecycle: a framework-owned contract MUST be lifecycle
//     draft|deprecated. Active framework SERVING (a framework RouteGroup that
//     mounts the contract — runtime/internal/contractbuild.NewFrameworkHTTP +
//     bootstrap) is not yet wired, so an `active` framework contract would be
//     silently dead with no serving-side coverage to catch it. Until that
//     serving scan lands, active is rejected. When framework serving is wired,
//     this rule is extended to scan the serving RouteGroup and permit served
//     active framework contracts (the DEAD-CONTRACT-01 analog for the framework
//     side).
//
//  3. Provider is the framework: a framework-owned contract's provider endpoint
//     (http server / event publisher) MUST be the FrameworkOwnerSentinel — the
//     framework, not a cell or actor, provides it. This is the framework-side
//     replacement for REF-13's provider-actor-exists check, which validateREF13
//     skips for framework owners (rules_ref.go): without it, a framework-owned
//     contract could name an arbitrary — even nonexistent — provider cell/actor
//     and nothing would catch it. An empty provider is left to FMT-07 (not
//     re-reported here).
//
// AI-robust: the FrameworkOwnerSentinel recognition + ContractOwner.Cell() type
// boundary is Hard (a framework owner can never be mistaken for a cell); this
// rule is the Medium governance companion (type-aware scan + synthetic red case
// in rules_framework_owned_test.go).
// See: docs/architecture/202606130635-1939-adr-framework-owned-contract.md §D3.
func (v *Validator) validateFRAMEWORKOWNEDCONTRACTSCOPED01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !c.Owner().IsFramework() {
			continue
		}
		results = append(results, v.checkFrameworkOwnedKind(c)...)
		results = append(results, v.checkFrameworkOwnedLifecycle(c)...)
		results = append(results, v.checkFrameworkOwnedProvider(c)...)
	}
	return results
}

// checkFrameworkOwnedKind enforces constraint 1 (eligible kind).
func (v *Validator) checkFrameworkOwnedKind(c *metadata.ContractMeta) []ValidationResult {
	switch cellvocab.ContractKind(c.Kind) {
	case cellvocab.ContractHTTP, cellvocab.ContractEvent:
		return nil
	default:
		return []ValidationResult{v.newError(
			codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueForbidden,
			contractFile(c), "ownerCell",
			fmt.Sprintf(
				"contract %q is framework-owned (ownerCell %q) but kind %q is not eligible; "+
					"only http and event contracts may be framework-owned",
				c.ID, metadata.FrameworkOwnerSentinel, c.Kind,
			),
			"if this contract has a cell owner, set ownerCell to an existing cell id; "+
				"if it must be framework-owned, change the kind to http or event",
		)}
	}
}

// checkFrameworkOwnedLifecycle enforces constraint 2 (fail-closed lifecycle).
// "active" is the literal used by the sibling lifecycle rules (ADV-05,
// DEAD-CONTRACT-01); reusing it keeps the lifecycle vocabulary consistent.
func (v *Validator) checkFrameworkOwnedLifecycle(c *metadata.ContractMeta) []ValidationResult {
	if c.Lifecycle != lifecycleActive {
		return nil
	}
	return []ValidationResult{v.newError(
		codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueForbidden,
		contractFile(c), "lifecycle",
		fmt.Sprintf(
			"framework-owned contract %q is lifecycle %q but active framework serving is not yet wired; "+
				"framework contracts must be draft|deprecated (fail-closed)",
			c.ID, c.Lifecycle,
		),
		"set lifecycle to draft or deprecated until a framework RouteGroup serves this contract",
	)}
}

// checkFrameworkOwnedProvider enforces constraint 3 (provider is the framework).
// A framework-owned contract's provider IS the framework: its provider endpoint
// (http server / event publisher) must be the FrameworkOwnerSentinel, never a
// cell or actor id. validateREF13 skips framework owners (the provider is the
// framework, not an actors.yaml entry), so without this check a framework-owned
// contract could name an arbitrary — even nonexistent — provider that nothing
// validates. Only http/event are checked: constraint 1 already rejects every
// other kind, and an empty provider is FMT-07's job (not re-reported here).
func (v *Validator) checkFrameworkOwnedProvider(c *metadata.ContractMeta) []ValidationResult {
	var field string
	switch cellvocab.ContractKind(c.Kind) {
	case cellvocab.ContractHTTP:
		field = "endpoints.server"
	case cellvocab.ContractEvent:
		field = "endpoints.publisher"
	default:
		return nil // ineligible kind: checkFrameworkOwnedKind (constraint 1) reports it
	}
	provider := contractProvider(c)
	if provider == "" || provider == metadata.FrameworkOwnerSentinel {
		return nil // empty → FMT-07; sentinel → correct (the framework is the provider)
	}
	return []ValidationResult{v.newError(
		codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueForbidden,
		contractFile(c), field,
		fmt.Sprintf(
			"framework-owned contract %q declares provider %q in %s, but a framework-owned "+
				"contract's provider must be the framework (%q)",
			c.ID, provider, field, metadata.FrameworkOwnerSentinel,
		),
		"set the provider endpoint to _framework, or give the contract an existing cell owner via ownerCell",
	)}
}
