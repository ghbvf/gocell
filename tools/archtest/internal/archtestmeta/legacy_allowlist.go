// Package archtestmeta carries migration-period metadata for the archtest
// pass-funnel migration (refactor/574-archtest-pass-funnel-pr1 onwards).
//
// THIS PACKAGE IS A FIXED-TERM MIGRATION SCAFFOLD.
//
// Stage 4 of the migration (see docs/plans/202605141519-040-archtest-pass-funnel-plan.md
// and ADR docs/architecture/202605141519-adr-archtest-pass-funnel.md) deletes
// this package in its entirety. The permanent enforcement layers are:
//
//   - Type system: archtest.Pass.Pkg is *types.Package (no .Syntax access);
//   - Lint: depguard rule archtest-no-direct-packages-load in .golangci.yml;
//   - Meta-archtest: PASS-FUNNEL-EACHFILE-01 / LOADPACKAGES-01 / PACKAGES-IMPORT-01.
//
// While the migration is in progress, the meta-archtest consults
// [LegacyAllowlist] to skip files that have not yet been ported from the
// scanner / typeseval direct entry points to archtest.Pass.
//
// .golangci.yml's archtest-no-direct-packages-load rule carries a separate
// (smaller) negative-glob list: only files that DIRECTLY import
// golang.org/x/tools/go/packages need a depguard exemption, which is a
// subset of this LegacyAllowlist. Stage 2/3 PRs porting a file MUST
// remove its entry here AND — if the file imports packages directly —
// the matching negative-glob in .golangci.yml. The
// TestPassFunnelGuardListSync archtest cross-validates the two lists.
package archtestmeta

// LegacyAllowlist enumerates archtest *_test.go files (module-relative slash
// paths) that still use scanner.EachFile, typeseval.LoadPackages,
// typeseval.SharedResolver, or directly import golang.org/x/tools/go/packages
// as of refactor/574-archtest-pass-funnel-pr1 (stage 1 baseline).
//
// Mutation rules:
//
//   - Stage 2/3 PRs remove exactly one entry as they port that file to
//     archtest.Pass + Run/RunTyped. If the file also directly imports
//     golang.org/x/tools/go/packages, its negative-glob entry in
//     .golangci.yml's archtest-no-direct-packages-load rule must be
//     removed in the same commit. TestPassFunnelGuardListSync fail-loud
//     enforces this — manual drift becomes a CI failure, not a silent
//     reviewer-only check.
//   - Stage 4 PR empties the map AND deletes this package entirely.
//
// FixtureBuildTag is the build-tag string shared by every archtest fixture
// sub-package whose violation samples must remain invisible to the default
// build context. Used by:
//
//   - tools/archtest/internal/passfunnelfixture/redfixture.go (`//go:build
//     archtest_fixture` directive — kept as a literal, since Go's build
//     directive syntax cannot reference Go constants).
//   - tools/archtest/pass_funnel_test.go: TestPassFunnel_FixtureCoverage
//     loads the fixture package with this tag passed to
//     typeseval.SharedResolver.
//
// The two sites must agree. The build directive in redfixture.go carries a
// godoc cross-reference to this constant; changing the value here without
// updating the directive (or vice versa) leaves the fixture invisible to
// the coverage test, which fails fast on "loaded with 0 files".
const FixtureBuildTag = "archtest_fixture"

// Lookup is by module-relative slash path (e.g. "tools/archtest/foo_test.go").
//
// Stage 1.5 additions (PASS-FUNNEL-RESOLVE-01 baseline):
//   - build_constraint_test.go: new file using typeseval.ParseBuildConstraint/BuildContextPredicate
//   - ci_integration_discovery_invariants_test.go: new file using typeseval.ParseBuildConstraint/FlatNonDefaultTags
var LegacyAllowlist = map[string]bool{
	"tools/archtest/archtest_test.go":                                true,
	"tools/archtest/archtest_verify_coverage_test.go":                true,
	"tools/archtest/audit_ledger_composition_root_test.go":           true,
	"tools/archtest/auth_bootstrap_invariants_test.go":               true,
	"tools/archtest/build_constraint_test.go":                        true, // Stage 1.5: uses ParseBuildConstraint/BuildContextPredicate
	"tools/archtest/cell_id_pattern_single_source_test.go":           true,
	"tools/archtest/cell_init_test.go":                               true,
	"tools/archtest/cell_public_option_param_test.go":                true,
	"tools/archtest/ci_integration_discovery_invariants_test.go":     true, // Stage 1.5: uses ParseBuildConstraint/FlatNonDefaultTags
	"tools/archtest/clock_invariants_test.go":                        true,
	"tools/archtest/credential_invalidate_funnel_invariants_test.go": true,
	"tools/archtest/domain_authz_mutation_funnel_invariants_test.go": true,
	"tools/archtest/errcode_invariants_test.go":                      true,
	"tools/archtest/errcode_message_const_fixtures_test.go":          true,
	"tools/archtest/eval_predicate_centralization_test.go":           true,
	"tools/archtest/exported_error_new_fixtures_test.go":             true,
	"tools/archtest/goose_session_locker_fixtures_test.go":           true,
	"tools/archtest/goose_session_locker_test.go":                    true,
	"tools/archtest/governance_rules_invariants_test.go":             true,
	"tools/archtest/health_aggregation_test.go":                      true,
	"tools/archtest/httputil_invariants_test.go":                     true,
	"tools/archtest/identitymanage_last_admin_protection_test.go":    true,
	"tools/archtest/inventory_anchor_required_test.go":               true,
	"tools/archtest/kernel_metadata_no_wire_test.go":                 true,
	"tools/archtest/kernel_poolstats_location_test.go":               true,
	"tools/archtest/listener_dx_test.go":                             true,
	"tools/archtest/managed_resource_contract_test.go":               true,
	"tools/archtest/outbox_invariants_test.go":                       true,
	"tools/archtest/panic_invariants_test.go":                        true,
	"tools/archtest/pg_repo_ambient_tx_test.go":                      true,
	"tools/archtest/prod_clock_injection_fixtures_test.go":           true,
	"tools/archtest/prod_duration_fixtures_test.go":                  true,
	"tools/archtest/prod_invariants_test.go":                         true,
	"tools/archtest/production_loader_funnel_test.go":                true,
	"tools/archtest/prom_cell_label_funnel_test.go":                  true,
	"tools/archtest/provision_state_removed_test.go":                 true,
	"tools/archtest/refresh_invariants_test.go":                      true,
	"tools/archtest/rmq_invariants_test.go":                          true,
	"tools/archtest/role_admin_literal_test.go":                      true,
	"tools/archtest/scaffold_bundle_invariants_test.go":              true,
	"tools/archtest/scaffold_write_funnel_test.go":                   true,
	"tools/archtest/scanner_framework_usage_test.go":                 true,
	"tools/archtest/sessionrefresh_no_session_create_test.go":        true,
	"tools/archtest/slowgate_allowlist_test.go":                      true,
	"tools/archtest/span_record_error_redact_test.go":                true,
	"tools/archtest/svctoken_caller_cell_test.go":                    true,
	"tools/archtest/test_sleep_discipline_test.go":                   true,
	"tools/archtest/test_time_literal_fixtures_test.go":              true,
	"tools/archtest/test_time_literal_test.go":                       true,
	"tools/archtest/wrapper_location_test.go":                        true,
}
