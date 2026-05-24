package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen/sharedschema"
)

// sharedSchemaCanonicalRel is the canonical schema path the shared-schema
// generator mirrors. It matches the single key of sharedschema.Mirrors.
const sharedSchemaCanonicalRel = "contracts/shared/errors/error-response-v1.schema.json"

// sharedSchemaCanonicalContent is a minimal valid JSON payload (not a real
// schema — these tests only exercise the byte-copy/diff plumbing).
var sharedSchemaCanonicalContent = []byte("{\"$id\":\"x\",\"type\":\"object\"}\n")

// writeSharedSchemaCanonical writes the canonical schema under root so the
// shared-schema generator/verifier has a source. Shared by the verify tests.
func writeSharedSchemaCanonical(t *testing.T, root string) {
	t.Helper()
	dst := filepath.Join(root, filepath.FromSlash(sharedSchemaCanonicalRel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir canonical dir: %v", err)
	}
	if err := os.WriteFile(dst, sharedSchemaCanonicalContent, 0o644); err != nil {
		t.Fatalf("write canonical: %v", err)
	}
}

// TestGenerateSharedSchema_UsageError asserts that omitting --all returns a
// usage error before any filesystem work.
func TestGenerateSharedSchema_UsageError(t *testing.T) {
	if err := generateSharedSchema(nil); err == nil {
		t.Fatal("generateSharedSchema(nil): expected usage error for missing --all, got nil")
	}
}

// TestGenerateSharedSchema_FlagParseError asserts an unknown flag fails parsing.
func TestGenerateSharedSchema_FlagParseError(t *testing.T) {
	if err := generateSharedSchema([]string{"-nonexistent-flag"}); err == nil {
		t.Fatal("generateSharedSchema([-nonexistent-flag]): expected flag parse error, got nil")
	}
}

// TestGenerateSharedSchema_WritesAndIdempotent drives the generator through the
// runGenerate dispatcher (covering the generateSubcommands registry closure)
// against a temp repo: it writes every declared mirror, is idempotent on a
// second run, and --dry-run does not recreate a removed mirror.
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestGenerateSharedSchema_WritesAndIdempotent(t *testing.T) {
	root := makeMinimalProject(t)
	writeSharedSchemaCanonical(t, root)

	// First generate via the dispatcher → writes every mirror.
	if err := runGenerate(context.Background(), []string{"shared-schema", "--all"}); err != nil {
		t.Fatalf("runGenerate shared-schema --all: %v", err)
	}
	destRoots := sharedschema.Mirrors[sharedSchemaCanonicalRel]
	if len(destRoots) == 0 {
		t.Fatal("sharedschema.Mirrors has no destRoots for the canonical key")
	}
	for _, dr := range destRoots {
		p := filepath.Join(root, filepath.FromSlash(dr), filepath.FromSlash(sharedSchemaCanonicalRel))
		got, err := os.ReadFile(p) //nolint:gosec // controlled temp path under t.TempDir
		if err != nil {
			t.Fatalf("mirror not written %s: %v", p, err)
		}
		if !bytes.Equal(got, sharedSchemaCanonicalContent) {
			t.Errorf("mirror %s content = %q, want %q", p, got, sharedSchemaCanonicalContent)
		}
	}

	// Second run is idempotent: nothing to write, no error.
	if err := generateSharedSchema([]string{"--all"}); err != nil {
		t.Fatalf("idempotent generateSharedSchema --all: %v", err)
	}

	// Remove one mirror; --dry-run must report but NOT recreate it.
	removed := filepath.Join(root, filepath.FromSlash(destRoots[0]), filepath.FromSlash(sharedSchemaCanonicalRel))
	if err := os.Remove(removed); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	if err := generateSharedSchema([]string{"--all", "--dry-run"}); err != nil {
		t.Fatalf("generateSharedSchema --all --dry-run: %v", err)
	}
	if _, err := os.Stat(removed); !os.IsNotExist(err) {
		t.Errorf("--dry-run must not recreate %s (stat err: %v)", removed, err)
	}
}

// TestGenerateSharedSchema_GenerateError asserts that a repo missing the
// canonical source makes sharedschema.Generate fail and the error propagates.
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestGenerateSharedSchema_GenerateError(t *testing.T) {
	makeMinimalProject(t) // go.mod only, no canonical schema
	if err := generateSharedSchema([]string{"--all"}); err == nil {
		t.Fatal("generateSharedSchema --all without canonical: expected error, got nil")
	}
}
