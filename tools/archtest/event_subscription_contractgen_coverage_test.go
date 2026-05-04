// EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01
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
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

		info, err := os.Stat(subPath)
		if err != nil || info.IsDir() {
			t.Errorf(
				"EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01: contract %q (kind=event, codegen=true) "+
					"is missing %s; run `gocell generate contract %s`",
				contract.ID, subPath, contract.ID,
			)
			continue
		}

		content, readErr := os.ReadFile(subPath) //nolint:gosec // archtest reads paths it discovered
		if readErr != nil {
			t.Errorf(
				"EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01: cannot read %s: %v",
				subPath, readErr,
			)
			continue
		}
		if !bytes.Contains(content, []byte("func NewSubscription")) {
			t.Errorf(
				"EVENT-SUBSCRIPTION-CONTRACTGEN-COVERAGE-01: contract %q: %s exists but does not "+
					"declare func NewSubscription; regenerate with `gocell generate contract %s`",
				contract.ID, subPath, contract.ID,
			)
		}
	}
}

// contractEventIDToExpectedPkgPath reuses contractIDToExpectedPkgPath from
// codegen_contract_gen_test.go (same package, same function visible here).
// Keeping a local alias comment for readability.
//
// "event.session.created.v1"  → "generated/contracts/event/session/created/v1"
// "event.config.entry-upserted.v1" → "generated/contracts/event/config/entry-upserted/v1"
var _ = func() string {
	return strings.Join(strings.Split("event.session.created.v1", "."), "/")
}
