//go:build ignore_codegen_contract_archtest_fixtures

// NOT generated — deliberately a hand-written *_test.go file in
// generated/contracts/. Used by TestCodegenContractGates_NegativeFixtures/user_file_overlap
// to verify that CODEGEN-CONTRACT-USER-OVERLAP-01's IncludeTests() option is
// load-bearing: removing IncludeTests() must turn this fixture red, otherwise
// hand-written test helpers can silently slip into generated/.

package v1
