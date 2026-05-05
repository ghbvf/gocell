// SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01 — forward gate that enforces
// generated event spec Topic == contract.ID (fully versioned).
//
// # Background
//
// Before PR-CODEGEN-FULL-MIGRATION-FU (B1), the generator called
// stripVersionSuffix and set Topic = contractID without the trailing .vN
// segment.  That produced a system-level topic mismatch: producer constants
// use the versioned topic (e.g. "event.session.created.v1") but the generated
// ContractSpec used the unversioned form ("event.session.created"), so no
// consumer ever received messages.
//
// After the fix: topic := contract.ID — Topic equals ID, both versioned.
//
// # What this gate checks
//
// For every generated/contracts/event/**/spec_gen.go, parse the
// wrapper.ContractSpec composite literal and assert Topic == ID.
//
// A negative fixture (testdata/spec_gen_topic_fixtures/topic_mismatch/spec_gen.go)
// contains a deliberately broken spec (Topic != ID); the second sub-test
// verifies the scanner reports that as a violation.
//
// ref: ThreeDotsLabs/watermill message/router.go (explicit topic passthrough)
// ref: docs/plans/202605011500-029-master-roadmap.md B1
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSpecGenTopicEqualsContractID is the SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01 gate.
func TestSpecGenTopicEqualsContractID(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	eventDir := filepath.Join(root, "generated", "contracts", "event")

	t.Run("production_specs_all_pass", func(t *testing.T) {
		t.Parallel()
		specFiles, err := findSpecGenFiles(eventDir)
		if err != nil {
			t.Fatalf("SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01: cannot walk %s: %v", eventDir, err)
		}
		if len(specFiles) == 0 {
			t.Fatalf("SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01: no spec_gen.go files found under %s; "+
				"run `gocell generate contract --all`", eventDir)
		}

		for _, path := range specFiles {
			path := path
			t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
				t.Parallel()
				id, topic, ok := parseContractSpecFields(t, path)
				if !ok {
					return // parse error already reported
				}
				if topic != id {
					t.Errorf(
						"SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01: %s: Topic %q != ID %q; "+
							"run `gocell generate contract --all` to regenerate",
						path, topic, id,
					)
				}
			})
		}
	})

	t.Run("negative_fixture_detected", func(t *testing.T) {
		t.Parallel()
		archTestDir := findArchTestDir(t)
		fixturePath := filepath.Join(archTestDir, "testdata", "spec_gen_topic_fixtures", "topic_mismatch", "spec_gen.go")

		id, topic, ok := parseContractSpecFields(t, fixturePath)
		if !ok {
			return
		}
		if topic == id {
			t.Errorf(
				"SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01 negative fixture: expected Topic %q != ID %q "+
					"(fixture must have a mismatch to prove the scanner works); fix the fixture file at %s",
				topic, id, fixturePath,
			)
		}
	})
}

// findSpecGenFiles walks dir recursively and returns all spec_gen.go paths.
func findSpecGenFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "spec_gen.go" {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// parseContractSpecFields parses a spec_gen.go file and extracts the ID and
// Topic string values from the wrapper.ContractSpec composite literal.
// Returns (id, topic, ok); ok is false when the file cannot be parsed or the
// fields are not found (the caller has already reported the error via t.Errorf).
func parseContractSpecFields(t *testing.T, path string) (id, topic string, ok bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Errorf("SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01: cannot parse %s: %v", path, err)
		return "", "", false
	}

	var foundID, foundTopic string
	ast.Inspect(f, func(n ast.Node) bool {
		// Look for composite literals that look like wrapper.ContractSpec{...}
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "ContractSpec" {
			return true
		}
		// Found a ContractSpec literal — extract ID and Topic fields.
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			val, ok := kv.Value.(*ast.BasicLit)
			if !ok {
				continue
			}
			if val.Kind != token.STRING {
				continue
			}
			unquoted, err := strconv.Unquote(val.Value)
			if err != nil {
				continue
			}
			switch key.Name {
			case "ID":
				foundID = unquoted
			case "Topic":
				foundTopic = unquoted
			}
		}
		return false // do not recurse into the literal
	})

	if foundID == "" {
		t.Errorf("SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01: %s: could not find ContractSpec.ID field", path)
		return "", "", false
	}
	if foundTopic == "" {
		t.Errorf("SPEC-GEN-TOPIC-EQUALS-CONTRACT-ID-01: %s: could not find ContractSpec.Topic field", path)
		return "", "", false
	}
	return foundID, foundTopic, true
}

// specGenIsVersionSegmentLocal reports whether s matches the pattern vN
// (letter 'v' followed by one or more decimal digits). Used only within this
// file; does not import the contractgen tool package to avoid a tools/ cycle.
func specGenIsVersionSegmentLocal(s string) bool {
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

// contractIDHasVersionSuffix returns true if the last dot-segment of id
// matches the vN pattern. Used only in documentation tests below.
func contractIDHasVersionSuffix(id string) bool {
	parts := strings.Split(id, ".")
	if len(parts) < 2 {
		return false
	}
	return specGenIsVersionSegmentLocal(parts[len(parts)-1])
}
