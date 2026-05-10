// INVARIANT: EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
//
// # EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
//
// Invariant: every contract with kind="event" and codegen=true must have a
// generated subscription_gen.go containing func NewSubscription in the
// expected package path derived from its ID.
//
// No allowlist: W2 already opted all active event contracts into codegen, so
// this gate should be permanently GREEN from the moment it lands.
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#PR4 W1/W2/W3
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

const subscriptionGenFilename = "subscription_gen.go"

// TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01 verifies that every
// kind=event contract with codegen=true has a generated subscription_gen.go
// that contains func NewSubscription.
func TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	for _, contract := range project.Contracts {
		if contract.Kind != "event" {
			continue
		}
		if !contract.Codegen {
			continue // not opted into codegen — gate ignores these
		}

		pkgDir := filepath.Join(root, contractIDToExpectedPkgPath(contract.ID))
		subPath := filepath.Join(pkgDir, subscriptionGenFilename)

		if err := scanEventSubscriptionCoverage(contract.ID, subPath); err != nil {
			t.Error(err.Error())
		}
	}
}

// contractIDToExpectedPkgPath (from codegen_contract_gen_test.go, same package)
// converts a contract ID to expected filesystem path, e.g.:
//
//	"event.session.created.v1"        → "generated/contracts/event/session/created/v1"
//	"event.config.entry-upserted.v1"  → "generated/contracts/event/config/entry-upserted/v1"

// scanEventSubscriptionCoverage checks whether the event contract identified by
// contractID has a valid subscription_gen.go at subPath. It returns an error when
// the file is absent or does not declare func NewSubscription.
//
// This helper is the reusable core of
// TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01; the negative-fixture test
// uses it to verify that the scanner catches the violation rather than hand-
// stat-ing the fixture file.
func scanEventSubscriptionCoverage(contractID, subPath string) error {
	info, err := os.Stat(subPath)
	if err != nil || info.IsDir() {
		return fmt.Errorf(
			"EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01: contract %q (kind=event, codegen=true) "+
				"is missing %s; run `gocell generate contract %s`",
			contractID, subPath, contractID,
		)
	}
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, subPath, nil, parser.SkipObjectResolution)
	if parseErr != nil {
		return fmt.Errorf(
			"EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01: cannot parse %s: %w",
			subPath, parseErr,
		)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv == nil && fn.Name.Name == "NewSubscription" {
			return nil
		}
	}
	return fmt.Errorf(
		"EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01: contract %q: %s exists but does not "+
			"declare func NewSubscription; regenerate with `gocell generate contract %s`",
		contractID, subPath, contractID,
	)
}

// TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01_NegativeFixture_VarShadowsFunc
// asserts the scanner rejects a subscription_gen.go that contains the bytes
// "func NewSubscription" only inside a comment, a string literal, and a
// `var NewSubscription = ...` declaration — no real *ast.FuncDecl.
//
// The legacy bytes.Contains scan FALSE-PASSes because the substring is
// literally present three times. This Wave 1 RED test FAILS pre-refactor
// and PASSes after the GREEN AST scan replaces bytes.Contains.
func TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01_NegativeFixture_VarShadowsFunc(t *testing.T) {
	t.Parallel()
	archDir := findArchTestDir(t)
	fixturePath := filepath.Join(archDir, "testdata", "event_subscription_coverage_fixtures", "var_shadows_func", "subscription_gen.go")

	if err := scanEventSubscriptionCoverage("event.fake.notify.v1", fixturePath); err == nil {
		t.Errorf("EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01 negative fixture var_shadows_func: " +
			"legacy bytes.Contains FALSE-PASSes — only comment/literal/var carriers exist, no real " +
			"FuncDecl; AST GREEN refactor required (parser.ParseFile + *ast.FuncDecl scan)")
	}
}

// TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01_NegativeFixture verifies that
// scanEventSubscriptionCoverage catches a contract with codegen=true that is
// missing subscription_gen.go. The fixture is a tmpdir with a contract.yaml but
// no generated file.
func TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01_NegativeFixture(t *testing.T) {
	t.Parallel()

	// Build a tmpdir that simulates the expected pkg path for a fake event contract.
	// We deliberately do NOT create subscription_gen.go inside it.
	tmp := t.TempDir()
	contractID := "event.fake.notify.v1"
	subPath := filepath.Join(tmp, subscriptionGenFilename)

	// The scanner must return an error when subscription_gen.go is absent.
	err := scanEventSubscriptionCoverage(contractID, subPath)
	if err == nil {
		t.Errorf(
			"expected scanEventSubscriptionCoverage to return an error for missing subscription_gen.go, got nil",
		)
	}
}

// TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01_NegativeFixture_WrongContent
// verifies that the scanner catches a subscription_gen.go that exists but does
// not declare func NewSubscription.
func TestEVENT_SUBSCRIPTION_CONTRACTGEN_COVERAGE_01_NegativeFixture_WrongContent(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	contractID := "event.fake.notify.v1"
	subPath := filepath.Join(tmp, subscriptionGenFilename)

	// Write a file without func NewSubscription.
	if err := os.WriteFile(subPath, []byte("package fake\n// no NewSubscription here\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	err := scanEventSubscriptionCoverage(contractID, subPath)
	if err == nil {
		t.Errorf(
			"expected scanEventSubscriptionCoverage to return an error when func NewSubscription is absent, got nil",
		)
	}
}
