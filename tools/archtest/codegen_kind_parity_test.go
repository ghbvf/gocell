// COMMAND-PROJECTION-EXPLICIT-01 — forward-guard for kind=command|projection codegen.
// SPEC-GEN-VALUE-PARITY-01       — guard for generated spec_gen.go value accuracy.
//
// # COMMAND-PROJECTION-EXPLICIT-01
//
// Invariant: when a kind=command or kind=projection contract opts into codegen
// (codegen=true), the generator emits only types_gen.go + iface_gen.go; it must
// NOT emit handler_gen.go (HTTP-only), spec_gen.go, or subscription_gen.go
// (event-only). This gate detects generator regressions that would accidentally
// emit event-style or http-style artifacts for these placeholder kinds.
//
// No contracts currently have kind=command|projection with codegen=true, so this
// gate is permanently GREEN today. It becomes load-bearing the moment a future
// maintainer adds codegen: true to a command or projection contract.
//
// # SPEC-GEN-VALUE-PARITY-01
//
// Invariant: for every kind=event contract with codegen=true, the generated
// spec_gen.go must contain:
//   - ID field value equal to the contract ID
//   - Topic field value equal to contractID with the trailing .vN suffix stripped
//
// A template bug or manual edit could silently produce wrong ID/Topic values that
// compile successfully but route events to the wrong queue at runtime. This gate
// catches that class of drift before it reaches production.
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#06
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCOMMAND_PROJECTION_EXPLICIT_01 verifies that kind=command|projection contracts
// with codegen=true do not have event-style or http-style generated artifacts.
func TestCOMMAND_PROJECTION_EXPLICIT_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	project := mustParseProjectContracts(t, root)

	for _, contract := range project.Contracts {
		if contract.Kind != "command" && contract.Kind != "projection" {
			continue
		}
		if !contract.Codegen {
			continue // not opted into codegen — gate ignores these
		}

		pkgDir := filepath.Join(root, contractIDToExpectedPkgPath(contract.ID))
		contract := contract // capture loop var

		t.Run(contract.ID, func(t *testing.T) {
			t.Parallel()

			// handler_gen.go must NOT exist (http-only artifact).
			handlerPath := filepath.Join(pkgDir, "handler_gen.go")
			if _, err := os.Stat(handlerPath); err == nil {
				t.Errorf(
					"COMMAND-PROJECTION-EXPLICIT-01: contract %q (kind=%s) has unexpected handler_gen.go at %s;"+
						" handler generation is HTTP-only — remove it and re-run `gocell generate contract %s`",
					contract.ID, contract.Kind, handlerPath, contract.ID,
				)
			}

			// spec_gen.go must NOT exist (event-only artifact).
			specPath := filepath.Join(pkgDir, "spec_gen.go")
			if _, err := os.Stat(specPath); err == nil {
				t.Errorf(
					"COMMAND-PROJECTION-EXPLICIT-01: contract %q (kind=%s) has unexpected spec_gen.go at %s;"+
						" spec generation is event-only — remove it and re-run `gocell generate contract %s`",
					contract.ID, contract.Kind, specPath, contract.ID,
				)
			}

			// subscription_gen.go must NOT exist (event-only artifact).
			subPath := filepath.Join(pkgDir, "subscription_gen.go")
			if _, err := os.Stat(subPath); err == nil {
				t.Errorf(
					"COMMAND-PROJECTION-EXPLICIT-01: contract %q (kind=%s) has unexpected subscription_gen.go at %s;"+
						" subscription generation is event-only — remove it and re-run `gocell generate contract %s`",
					contract.ID, contract.Kind, subPath, contract.ID,
				)
			}
		})
	}
}

// TestSPEC_GEN_VALUE_PARITY_01 verifies that for every kind=event contract with
// codegen=true, the generated spec_gen.go contains the correct ID and Topic values.
func TestSPEC_GEN_VALUE_PARITY_01(t *testing.T) {
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
		specPath := filepath.Join(pkgDir, "spec_gen.go")
		contract := contract // capture loop var

		t.Run(contract.ID, func(t *testing.T) {
			t.Parallel()

			content, err := os.ReadFile(specPath) //nolint:gosec // archtest reads paths it discovered
			if err != nil {
				t.Errorf(
					"SPEC-GEN-VALUE-PARITY-01: contract %q (kind=event, codegen=true) "+
						"cannot read spec_gen.go at %s: %v; run `gocell generate contract %s`",
					contract.ID, specPath, err, contract.ID,
				)
				return
			}

			src := string(content)

			// Verify ID value: look for the literal   ID:        "<contractID>",
			wantID := fmt.Sprintf("%q", contract.ID)
			wantIDLine := "ID:        " + wantID
			if !strings.Contains(src, wantIDLine) {
				t.Errorf(
					"SPEC-GEN-VALUE-PARITY-01: contract %q: spec_gen.go ID field does not match;"+
						" expected line containing %q; regenerate with `gocell generate contract %s`",
					contract.ID, wantIDLine, contract.ID,
				)
			}

			// Verify Topic value: topic = stripVersionSuffix(contractID).
			wantTopic := specGenStripVersionSuffix(contract.ID)
			wantTopicLine := "Topic:     " + fmt.Sprintf("%q", wantTopic)
			if !strings.Contains(src, wantTopicLine) {
				t.Errorf(
					"SPEC-GEN-VALUE-PARITY-01: contract %q: spec_gen.go Topic field does not match;"+
						" expected line containing %q; regenerate with `gocell generate contract %s`",
					contract.ID, wantTopicLine, contract.ID,
				)
			}
		})
	}
}

// specGenStripVersionSuffix removes the trailing .vN segment from a contract ID.
// "event.session.created.v1" → "event.session.created"
// "event.order-created.v2"   → "event.order-created"
// Mirrors contractgen.stripVersionSuffix without importing the tool package.
func specGenStripVersionSuffix(id string) string {
	parts := strings.Split(id, ".")
	if len(parts) < 2 {
		return id
	}
	last := parts[len(parts)-1]
	if specGenIsVersionSegment(last) {
		return strings.Join(parts[:len(parts)-1], ".")
	}
	return id
}

// specGenIsVersionSegment reports whether s matches the pattern vN (v followed by digits).
func specGenIsVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
