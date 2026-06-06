package sharedschema_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen/sharedschema"
)

// canonicalKey is the single entry currently declared in sharedschema.Mirrors.
const canonicalKey = "contracts/shared/errors/error-response-v1.schema.json"

// canonicalContent is the fixed JSON bytes used as the canonical file payload
// across all sub-tests. It is not a real schema — it just has to be valid JSON
// and must not carry a gocell generated header (JSON files cannot carry
// Go-style line comments).
var canonicalContent = []byte("{\"$id\":\"x\",\"type\":\"object\"}\n")

// altContent is a different payload used to simulate drift.
var altContent = []byte("{\"$id\":\"y\",\"type\":\"string\"}\n")

// writeCanonical writes canonicalContent to <root>/canonicalKey, creating
// intermediate directories as needed.
func writeCanonical(t *testing.T, root string) {
	t.Helper()
	dst := filepath.Join(root, filepath.FromSlash(canonicalKey))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("writeCanonical MkdirAll: %v", err)
	}
	if err := os.WriteFile(dst, canonicalContent, 0o644); err != nil {
		t.Fatalf("writeCanonical WriteFile: %v", err)
	}
}

// destPath returns the expected destination file path for a given destRoot.
func destPath(root, destRoot string) string {
	return filepath.Join(root, filepath.FromSlash(destRoot), filepath.FromSlash(canonicalKey))
}

// TestMirrorsManifest guards the shape of the Mirrors manifest so accidental
// additions or removals are caught immediately.
func TestMirrorsManifest(t *testing.T) {
	t.Parallel()

	if got := len(sharedschema.Mirrors); got != 1 {
		t.Errorf("len(Mirrors) = %d, want 1", got)
	}

	destRoots, ok := sharedschema.Mirrors[canonicalKey]
	if !ok {
		t.Fatalf("Mirrors missing key %q", canonicalKey)
	}

	wantDestRoots := []string{
		"examples/demo",
		"examples/iotdevice",
		"examples/orderfulfillment",
		"examples/todoorder",
		"tests/contracttest/testdata",
	}
	sort.Strings(destRoots)
	sort.Strings(wantDestRoots)

	if len(destRoots) != len(wantDestRoots) {
		t.Errorf("Mirrors[%q] has %d destRoots, want %d: got %v", canonicalKey, len(destRoots), len(wantDestRoots), destRoots)
		return
	}
	for i := range wantDestRoots {
		if destRoots[i] != wantDestRoots[i] {
			t.Errorf("destRoots[%d] = %q, want %q", i, destRoots[i], wantDestRoots[i])
		}
	}
}

func TestGenerate(t *testing.T) {
	t.Parallel()

	t.Run("normal: writes all dest files and they match canonical", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeCanonical(t, root)

		written, err := sharedschema.Generate(root, false)
		if err != nil {
			t.Fatalf("Generate returned error: %v", err)
		}

		destRoots := sharedschema.Mirrors[canonicalKey]
		if len(written) != len(destRoots) {
			t.Errorf("Generate wrote %d files, want %d", len(written), len(destRoots))
		}

		for _, dr := range destRoots {
			p := destPath(root, dr)
			got, err := os.ReadFile(p) //nolint:gosec // G304: test-controlled path under t.TempDir
			if err != nil {
				t.Errorf("dest file missing or unreadable %s: %v", p, err)
				continue
			}
			if !bytes.Equal(got, canonicalContent) {
				t.Errorf("dest %s content = %q, want %q", p, got, canonicalContent)
			}
			// Verify MkdirAll worked: parent directory exists
			if _, statErr := os.Stat(filepath.Dir(p)); statErr != nil {
				t.Errorf("parent dir of %s should exist: %v", p, statErr)
			}
		}
	})

	t.Run("idempotent: second Generate returns empty written list", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeCanonical(t, root)

		if _, err := sharedschema.Generate(root, false); err != nil {
			t.Fatalf("first Generate error: %v", err)
		}

		written, err := sharedschema.Generate(root, false)
		if err != nil {
			t.Fatalf("second Generate error: %v", err)
		}
		if len(written) != 0 {
			t.Errorf("idempotent Generate should return 0 written, got %d: %v", len(written), written)
		}
	})

	t.Run("dryRun: returns would-write list but dest files are absent", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeCanonical(t, root)

		written, err := sharedschema.Generate(root, true)
		if err != nil {
			t.Fatalf("Generate DryRun error: %v", err)
		}

		destRoots := sharedschema.Mirrors[canonicalKey]
		if len(written) != len(destRoots) {
			t.Errorf("DryRun: Generate returned %d entries, want %d", len(written), len(destRoots))
		}

		// The canonical file exists, but none of the dest copies should have been written.
		for _, dr := range destRoots {
			p := destPath(root, dr)
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("DryRun must not create dest file %s (stat err: %v)", p, err)
			}
		}
	})
}

func TestVerify(t *testing.T) {
	t.Parallel()

	t.Run("all synced: drifted is empty", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeCanonical(t, root)

		// Write all dest files first.
		if _, err := sharedschema.Generate(root, false); err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		drifted, err := sharedschema.Verify(root)
		if err != nil {
			t.Fatalf("Verify error: %v", err)
		}
		if len(drifted) != 0 {
			t.Errorf("Verify expected 0 drifted, got %d: %v", len(drifted), drifted)
		}
	})

	t.Run("one dest tampered: only that dest reported as drifted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeCanonical(t, root)

		if _, err := sharedschema.Generate(root, false); err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		// Tamper the first destRoot.
		destRoots := sharedschema.Mirrors[canonicalKey]
		sort.Strings(destRoots) // deterministic pick
		tamperedDR := destRoots[0]
		tamperedPath := destPath(root, tamperedDR)
		if err := os.WriteFile(tamperedPath, altContent, 0o644); err != nil {
			t.Fatalf("tamper WriteFile: %v", err)
		}

		drifted, err := sharedschema.Verify(root)
		if err != nil {
			t.Fatalf("Verify error: %v", err)
		}
		if len(drifted) != 1 {
			t.Fatalf("Verify expected 1 drifted, got %d: %v", len(drifted), drifted)
		}
		// The reported path should be the dest-relative path (not including root),
		// always in slash form (Verify returns filepath.ToSlash paths).
		want := filepath.ToSlash(filepath.Join(filepath.FromSlash(tamperedDR), filepath.FromSlash(canonicalKey)))
		if drifted[0] != want {
			t.Errorf("drifted[0] = %q, want %q", drifted[0], want)
		}
	})

	t.Run("one dest deleted: that dest reported as drifted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeCanonical(t, root)

		if _, err := sharedschema.Generate(root, false); err != nil {
			t.Fatalf("Generate error: %v", err)
		}

		destRoots := sharedschema.Mirrors[canonicalKey]
		sort.Strings(destRoots)
		deletedDR := destRoots[0]
		deletedPath := destPath(root, deletedDR)
		if err := os.Remove(deletedPath); err != nil {
			t.Fatalf("Remove: %v", err)
		}

		drifted, err := sharedschema.Verify(root)
		if err != nil {
			t.Fatalf("Verify error: %v", err)
		}
		if len(drifted) != 1 {
			t.Fatalf("Verify expected 1 drifted (missing file), got %d: %v", len(drifted), drifted)
		}
		// Verify returns filepath.ToSlash paths; construct want in the same form.
		want := filepath.ToSlash(filepath.Join(filepath.FromSlash(deletedDR), filepath.FromSlash(canonicalKey)))
		if drifted[0] != want {
			t.Errorf("drifted[0] = %q, want %q", drifted[0], want)
		}
	})
}
