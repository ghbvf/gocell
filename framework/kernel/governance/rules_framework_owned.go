package governance

import (
	"fmt"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
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
// `ownerCell: _framework` to dodge cell governance lands here instead. Three
// constraints:
//
//  1. Eligible kind: only http and event contracts may be framework-owned —
//     the neutral provider-agnostic wire surfaces. command/projection/saga/
//     webhook/grpc carry cell-internal semantics (a dispatch handler, a read
//     model, an orchestrator, a receiver, a service) and MUST name a cell owner.
//     New kinds are admitted by extending this allow-set in a later PR (and its
//     red case), so an un-vetted kind is fail-closed today.
//
//  2. Serving-scan lifecycle: framework serving is now wired (bootstrap framework
//     RouteGroup + startup fail-fast). This rule permits active framework http/event
//     contracts that appear in some assembly.frameworkContracts (serving-scan), and
//     rejects active framework contracts served by no assembly (the
//     DEAD-CONTRACT-01 analog for the framework side). draft/deprecated are always
//     permitted. The complementary check (constraint 2b) validates each
//     assembly.frameworkContracts entry: it must be an active framework http/event
//     contract (bidirectional closure).
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
	// Build the served-contract set: union of all assembly.frameworkContracts entries.
	served := make(map[string]bool)
	for _, a := range v.project.Assemblies {
		for _, id := range a.FrameworkContracts {
			served[id] = true
		}
	}

	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !c.Owner().IsFramework() {
			continue
		}
		results = append(results, v.checkFrameworkOwnedKind(c)...)
		results = append(results, v.checkFrameworkOwnedLifecycle(c, served)...)
		results = append(results, v.checkFrameworkOwnedProvider(c)...)
	}
	results = append(results, v.checkAssemblyFrameworkContracts()...)
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

// checkFrameworkOwnedLifecycle enforces constraint 2 (serving-scan lifecycle).
// draft/deprecated are always permitted. active is permitted only when the
// contract appears in at least one assembly's frameworkContracts list (serving-scan).
// An active framework contract served by no assembly is the DEAD-CONTRACT-01 analog
// for the framework side: it would be silently dead with no serving-side coverage.
// "active" is the literal used by the sibling lifecycle rules (ADV-05,
// DEAD-CONTRACT-01); reusing it keeps the lifecycle vocabulary consistent.
func (v *Validator) checkFrameworkOwnedLifecycle(c *metadata.ContractMeta, served map[string]bool) []ValidationResult {
	if c.Lifecycle != lifecycleActive {
		return nil // draft / deprecated always permitted
	}
	if served[c.ID] {
		return nil // active and served by some assembly
	}
	return []ValidationResult{v.newError(
		codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueForbidden,
		contractFile(c), "lifecycle",
		fmt.Sprintf(
			"active framework contract %q is not served by any assembly "+
				"(not in any assembly.frameworkContracts); framework serving is wired via "+
				"the bootstrap framework RouteGroup + startup fail-fast",
			c.ID,
		),
		"add the contract id to the serving assembly's frameworkContracts, "+
			"or set lifecycle back to draft",
	)}
}

// checkAssemblyFrameworkContracts validates each entry in every assembly's
// frameworkContracts list — bidirectional closure with checkFrameworkOwnedLifecycle:
// ① active framework contracts must appear in some assembly (serving-scan);
// ② assembly entries must be active framework http/event contracts (drift guard).
func (v *Validator) checkAssemblyFrameworkContracts() []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		for _, id := range a.FrameworkContracts {
			c, exists := v.project.Contracts[id]
			if !exists {
				results = append(results, v.newError(
					codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueRefNotFound,
					assemblyFile(a), "frameworkContracts",
					fmt.Sprintf(
						"assembly %q frameworkContracts references unknown contract %q",
						a.ID, id,
					),
					"add the contract declaration under contracts/ or remove this entry",
				))
				continue
			}
			if !c.Owner().IsFramework() {
				results = append(results, v.newError(
					codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueForbidden,
					assemblyFile(a), "frameworkContracts",
					fmt.Sprintf(
						"assembly %q frameworkContracts references contract %q which is not framework-owned "+
							"(ownerCell must be %q)",
						a.ID, id, metadata.FrameworkOwnerSentinel,
					),
					"set the contract's ownerCell to _framework, or remove it from frameworkContracts",
				))
				continue
			}
			switch cellvocab.ContractKind(c.Kind) {
			case cellvocab.ContractHTTP, cellvocab.ContractEvent:
				// eligible kind — continue to lifecycle check
			default:
				results = append(results, v.newError(
					codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueForbidden,
					assemblyFile(a), "frameworkContracts",
					fmt.Sprintf(
						"assembly %q frameworkContracts references contract %q with kind %q which is not eligible "+
							"(only http/event framework contracts may be served)",
						a.ID, id, c.Kind,
					),
					"change the contract kind to http or event, or remove it from frameworkContracts",
				))
				continue
			}
			if c.Lifecycle != lifecycleActive {
				results = append(results, v.newError(
					codeFRAMEWORKOWNEDCONTRACTSCOPED01, IssueForbidden,
					assemblyFile(a), "frameworkContracts",
					fmt.Sprintf(
						"assembly %q frameworkContracts references contract %q with lifecycle %q; "+
							"only active framework contracts are served",
						a.ID, id, c.Lifecycle,
					),
					"set the contract lifecycle to active, or remove it from frameworkContracts "+
						"(draft contracts are not yet ready for serving)",
				))
			}
		}
	}
	return results
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
