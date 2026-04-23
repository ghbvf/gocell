// Package metadata — strict-field regression matrix.
//
// This file proves the parser's yaml.KnownFields(true) invariant rejects all
// dynamic delivery-state fields when they appear in non-status-board metadata
// YAML types, while confirming that the subset of those fields that belong to
// StatusBoardEntry (risk / blocker / updatedAt) are accepted there.
//
// Regression coverage for: G-1 FMT-11 DYNAMIC-FIELD-ISOLATION-01
// The invariant is silently enforced by kernel/metadata/parser.go unmarshalFile
// (dec2.KnownFields(true)); these tests make it explicit and machine-verifiable.
//
// ref: gopkg.in/yaml.v3 KnownFields(true) — unknown-field rejection at decode time.
package metadata

import (
	"errors"
	"fmt"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dynamicStateFields lists all delivery-state fields that must never appear in
// any metadata YAML other than journeys/status-board.yaml.
var dynamicStateFields = []string{
	"readiness",
	"risk",
	"blocker",
	"done",
	"verified",
	"nextAction",
	"updatedAt",
}

// minimalCellYAML is the smallest valid cell.yaml content.
const minimalCellYAML = `id: testcell
type: core
consistencyLevel: L1
owner:
  team: test
  role: cell-owner
schema:
  primary: cell_test
verify:
  smoke:
    - smoke.testcell.startup
`

// minimalSliceYAML is the smallest valid slice.yaml content.
const minimalSliceYAML = `id: testslice
belongsToCell: testcell
contractUsages: []
verify:
  unit: []
  contract: []
`

// minimalActorYAML is the smallest valid actors.yaml element.
// actors.yaml is a YAML sequence at the top level ([]ActorMeta), so injection
// inserts the dynamic field as a sibling of id/type within the sequence entry.
const minimalActorYAML = `- id: test-actor
  type: external
  maxConsistencyLevel: L2
`

// minimalContractYAML is the smallest valid contract.yaml content.
const minimalContractYAML = `id: http.test.v1
kind: http
consistencyLevel: L1
lifecycle: active
endpoints:
  server: testcell
  clients: []
`

// minimalAssemblyYAML is the smallest valid assembly.yaml content.
const minimalAssemblyYAML = `id: testassembly
cells:
  - testcell
build:
  entrypoint: cmd/main.go
  binary: testbin
  deployTemplate: k8s
`

// minimalJourneyYAML is the smallest valid journey YAML content.
const minimalJourneyYAML = `id: J-test
goal: test journey
owner:
  team: test
  role: journey-owner
cells: []
contracts: []
passCriteria: []
`

// yamlKind describes a metadata YAML file type for the test matrix.
type yamlKind struct {
	name      string
	path      string
	baseYAML  string
	peerFiles fstest.MapFS // additional files the walker needs to not choke
}

// metadataKinds enumerates the six YAML types under test.
var metadataKinds = []yamlKind{
	{
		name:     "cell",
		path:     "cells/testcell/cell.yaml",
		baseYAML: minimalCellYAML,
	},
	{
		// B6: parser.parseSlice is unconditional — it does not check for the
		// existence of a peer cell.yaml before decoding slice.yaml. The
		// walkDir switch dispatches on path pattern alone (matchSliceYAML).
		// These slice rejection cases are therefore genuine and not fake-green.
		name:     "slice",
		path:     "cells/testcell/slices/testslice/slice.yaml",
		baseYAML: minimalSliceYAML,
	},
	{
		name:     "contract",
		path:     "contracts/http/test/v1/contract.yaml",
		baseYAML: minimalContractYAML,
	},
	{
		name:     "assembly",
		path:     "assemblies/testassembly/assembly.yaml",
		baseYAML: minimalAssemblyYAML,
	},
	{
		name:     "journey",
		path:     "journeys/J-test.yaml",
		baseYAML: minimalJourneyYAML,
	},
	{
		// actors.yaml is a YAML sequence ([]ActorMeta). The dynamic field is
		// injected as a sibling key within the single sequence element, not at
		// the document root, so the injection template differs from the map
		// kinds above. minimalActorYAML already contains "- id: ..." at
		// column 0; we append "  <field>: injected-value\n" (2-space indent)
		// to insert the field inside that sequence element.
		name:     "actor",
		path:     "actors.yaml",
		baseYAML: minimalActorYAML,
	},
}

// lineNumberRe matches the "line N" substring that yaml.v3 includes in
// KnownFields rejection errors. Future yaml.v3 upgrades that drop line
// numbers will cause this assertion to fail, surfacing the regression.
var lineNumberRe = regexp.MustCompile(`line \d+`)

// TestParser_StrictKnownFields_RejectsDynamicFields is the 6×7 rejection matrix.
// For each (YAML type, dynamic field) pair it constructs a minimal valid
// YAML file, injects the dynamic field, and asserts that ParseFS returns an
// ErrMetadataInvalid error whose message names the field and includes a line number.
func TestParser_StrictKnownFields_RejectsDynamicFields(t *testing.T) {
	for _, kind := range metadataKinds {
		kind := kind // capture
		for _, field := range dynamicStateFields {
			field := field // capture
			name := kind.name + "_rejects_" + field
			t.Run(name, func(t *testing.T) {
				// Build the injected YAML. For map-based YAML kinds the dynamic
				// field is appended at the document root (column 0). For
				// actors.yaml (a sequence), the field is indented 2 spaces so it
				// lands inside the single sequence element.
				var injected string
				if kind.name == "actor" {
					injected = kind.baseYAML + "  " + field + ": injected-value\n"
				} else {
					injected = kind.baseYAML + field + ": injected-value\n"
				}

				fsys := fstest.MapFS{
					kind.path: &fstest.MapFile{Data: []byte(injected)},
				}
				// Seed peer files from the kind definition, if any.
				for k, v := range kind.peerFiles {
					fsys[k] = v
				}

				p := NewParser("")
				_, err := p.ParseFS(fsys)

				require.Error(t, err, "expected rejection of field %q in %s", field, kind.path)

				var ecErr *errcode.Error
				require.True(t, errors.As(err, &ecErr),
					"expected *errcode.Error, got %T: %v", err, err)
				assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
				assert.Contains(t, err.Error(), field,
					fmt.Sprintf("expected error to contain field %q but got: %s", field, err.Error()))
				assert.True(t, lineNumberRe.MatchString(err.Error()),
					fmt.Sprintf("expected error to contain 'line N' (yaml.v3 line number) but got: %s", err.Error()))
			})
		}
	}
}

// TestParser_StrictKnownFields_StatusBoardAcceptsLegitimateFields verifies that
// the three dynamic fields that are part of StatusBoardEntry (risk, blocker,
// updatedAt) are accepted when they appear in journeys/status-board.yaml.
func TestParser_StrictKnownFields_StatusBoardAcceptsLegitimateFields(t *testing.T) {
	legitimateFields := []struct {
		name string
		yaml string
	}{
		{
			name: "risk_accepted",
			yaml: "- journeyId: J-test\n  state: doing\n  risk: high\n  blocker: \"\"\n  updatedAt: \"2026-01-01\"\n",
		},
		{
			name: "blocker_accepted",
			yaml: "- journeyId: J-test\n  state: doing\n  risk: \"\"\n  blocker: some-blocker\n  updatedAt: \"2026-01-01\"\n",
		},
		{
			name: "updatedAt_accepted",
			yaml: "- journeyId: J-test\n  state: doing\n  risk: \"\"\n  blocker: \"\"\n  updatedAt: \"2026-04-24\"\n",
		},
	}

	for _, tc := range legitimateFields {
		tc := tc // capture
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(tc.yaml)},
			}
			p := NewParser("")
			pm, err := p.ParseFS(fsys)
			require.NoError(t, err,
				"legitimate StatusBoardEntry field must not be rejected")
			require.NotEmpty(t, pm.StatusBoard)
		})
	}
}

// TestParser_StrictKnownFields_StatusBoardRejectsNonStructFields verifies that
// dynamic state fields not present in the StatusBoardEntry struct are still
// rejected even in journeys/status-board.yaml. The fields readiness, done,
// verified, and nextAction have no corresponding yaml tag on StatusBoardEntry.
func TestParser_StrictKnownFields_StatusBoardRejectsNonStructFields(t *testing.T) {
	// These fields have no yaml tag in StatusBoardEntry; they must be rejected.
	rejectedInStatusBoard := []string{
		"readiness",
		"done",
		"verified",
		"nextAction",
	}

	for _, field := range rejectedInStatusBoard {
		field := field // capture
		name := "statusboard_rejects_" + field
		t.Run(name, func(t *testing.T) {
			// Inject the field alongside legitimate status-board fields.
			injected := "- journeyId: J-test\n  state: doing\n  risk: low\n  blocker: \"\"\n  updatedAt: \"2026-01-01\"\n  " + field + ": injected\n"

			fsys := fstest.MapFS{
				"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(injected)},
			}

			p := NewParser("")
			_, err := p.ParseFS(fsys)

			require.Error(t, err,
				"field %q must be rejected from journeys/status-board.yaml", field)

			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr),
				"expected *errcode.Error, got %T: %v", err, err)
			assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
			assert.Contains(t, err.Error(), field,
				fmt.Sprintf("expected error to contain field %q but got: %s", field, err.Error()))
			assert.True(t, lineNumberRe.MatchString(err.Error()),
				fmt.Sprintf("expected error to contain 'line N' (yaml.v3 line number) but got: %s", err.Error()))
		})
	}
}

// TestParser_StrictKnownFields_RejectsNestedDynamicFields proves that
// yaml.KnownFields(true) propagates into nested structs. It injects an
// unknown field under the "owner" sub-struct of a cell.yaml and asserts
// rejection.
func TestParser_StrictKnownFields_RejectsNestedDynamicFields(t *testing.T) {
	// owner.readiness is not a field on OwnerMeta; KnownFields must reject it.
	injected := minimalCellYAML + "  readiness: \"x\"\n" // indented under owner block

	// Re-construct with explicit nested injection: append a readiness key
	// inside the owner mapping. We do this by building a fresh YAML that
	// places readiness inside owner rather than at the top level, to exercise
	// nested-struct KnownFields propagation specifically.
	injectedNested := `id: testcell
type: core
consistencyLevel: L1
owner:
  team: test
  role: cell-owner
  readiness: "x"
schema:
  primary: cell_test
verify:
  smoke:
    - smoke.testcell.startup
`
	_ = injected // top-level variant not used in this test

	fsys := fstest.MapFS{
		"cells/testcell/cell.yaml": &fstest.MapFile{Data: []byte(injectedNested)},
	}

	p := NewParser("")
	_, err := p.ParseFS(fsys)

	require.Error(t, err, "expected rejection of nested field 'readiness' inside owner")

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr),
		"expected *errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
	assert.Contains(t, err.Error(), "readiness",
		fmt.Sprintf("expected error to contain 'readiness' but got: %s", err.Error()))
	assert.True(t, lineNumberRe.MatchString(err.Error()),
		fmt.Sprintf("expected error to contain 'line N' but got: %s", err.Error()))
}
