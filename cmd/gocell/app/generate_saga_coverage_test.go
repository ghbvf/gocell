package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen/sagacoveragegen"
)

// sagaCoverageGenRel / readyzRel / alertingRel are the three targets the
// saga-coverage generator writes, relative to the project root.
const (
	sagaCoverageGenRel = "kernel/saga/sagajournaltest/terminal_coverage_gen.go"
	sagaReadyzRel      = "docs/ops/readyz.md"
	sagaAlertingRel    = "docs/ops/alerting-rules.md"
)

// seedSagaCoverageDocs writes docs/ops/readyz.md and docs/ops/alerting-rules.md
// under root with the marker-delimited regions the generator replaces, but with
// stale bodies so a successful run is observable as a region change.
func seedSagaCoverageDocs(t *testing.T, root string) {
	t.Helper()
	readyz := "# Readyz\n\nlead-in\n" +
		sagacoveragegen.ReadyzTableStartMarker + "\nSTALE READYZ TABLE\n" + sagacoveragegen.ReadyzTableEndMarker +
		"\ntrailer\n"
	writeSagaDoc(t, root, sagaReadyzRel, readyz)

	alerting := "# Alerting\n\nlead-in\n" +
		sagacoveragegen.KindLegendStartMarker + "\nSTALE LEGEND\n" + sagacoveragegen.KindLegendEndMarker +
		"\ntrailer\n"
	writeSagaDoc(t, root, sagaAlertingRel, alerting)
}

func writeSagaDoc(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readSagaFile(t *testing.T, root, rel string) string {
	t.Helper()
	//nolint:gosec // controlled temp path under t.TempDir
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestGenerateSagaCoverage_FlagParseError asserts an unknown flag fails parsing
// before any filesystem work.
func TestGenerateSagaCoverage_FlagParseError(t *testing.T) {
	if err := generateSagaCoverage([]string{"-nonexistent-flag"}); err == nil {
		t.Fatal("generateSagaCoverage([-nonexistent-flag]): expected flag parse error, got nil")
	}
}

// TestGenerateSagaCoverage_WritesAndIdempotent drives the generator through the
// runGenerate dispatcher (covering the generateSubcommands registry closure)
// against a temp repo seeded with the two ops docs: it writes the gen file and
// replaces both doc regions with the rendered fragments, then is a clean no-op
// on a second run (writeDocRegion's "unchanged" branch).
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestGenerateSagaCoverage_WritesAndIdempotent(t *testing.T) {
	root := makeMinimalProject(t)
	seedSagaCoverageDocs(t, root)

	art, err := sagacoveragegen.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if err := runGenerate(context.Background(), []string{"saga-coverage"}); err != nil {
		t.Fatalf("runGenerate saga-coverage: %v", err)
	}

	// Gen file is byte-identical to Render().TerminalCoverageGo.
	if got := readSagaFile(t, root, sagaCoverageGenRel); got != string(art.TerminalCoverageGo) {
		t.Errorf("gen file content mismatch:\n got: %q\nwant: %q", got, art.TerminalCoverageGo)
	}

	// Each doc region equals the rendered fragment.
	gotReadyz, err := sagacoveragegen.ExtractRegion(readSagaFile(t, root, sagaReadyzRel),
		sagacoveragegen.ReadyzTableStartMarker, sagacoveragegen.ReadyzTableEndMarker)
	if err != nil {
		t.Fatalf("extract readyz region: %v", err)
	}
	if gotReadyz != art.ReadyzTable {
		t.Errorf("readyz region mismatch:\n got: %q\nwant: %q", gotReadyz, art.ReadyzTable)
	}
	gotLegend, err := sagacoveragegen.ExtractRegion(readSagaFile(t, root, sagaAlertingRel),
		sagacoveragegen.KindLegendStartMarker, sagacoveragegen.KindLegendEndMarker)
	if err != nil {
		t.Fatalf("extract legend region: %v", err)
	}
	if gotLegend != art.KindLegend {
		t.Errorf("legend region mismatch:\n got: %q\nwant: %q", gotLegend, art.KindLegend)
	}

	// Second run is idempotent: nothing changed, no error.
	if err := generateSagaCoverage(nil); err != nil {
		t.Fatalf("idempotent generateSagaCoverage: %v", err)
	}
}

// TestGenerateSagaCoverage_DryRunDoesNotWrite asserts --dry-run reports the
// targets but mutates nothing: the gen file is not created and the seeded doc
// regions keep their stale bodies.
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestGenerateSagaCoverage_DryRunDoesNotWrite(t *testing.T) {
	root := makeMinimalProject(t)
	seedSagaCoverageDocs(t, root)

	if err := generateSagaCoverage([]string{"--dry-run"}); err != nil {
		t.Fatalf("generateSagaCoverage --dry-run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(sagaCoverageGenRel))); !os.IsNotExist(err) {
		t.Errorf("--dry-run must not create the gen file (stat err: %v)", err)
	}
	region, err := sagacoveragegen.ExtractRegion(readSagaFile(t, root, sagaReadyzRel),
		sagacoveragegen.ReadyzTableStartMarker, sagacoveragegen.ReadyzTableEndMarker)
	if err != nil {
		t.Fatalf("extract readyz region: %v", err)
	}
	if region != "STALE READYZ TABLE\n" {
		t.Errorf("--dry-run must not rewrite the readyz region; got %q", region)
	}
}

// TestGenerateSagaCoverage_MissingDocErrors asserts the generator fails when a
// target doc is absent (writeDocRegion's read-error branch).
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestGenerateSagaCoverage_MissingDocErrors(t *testing.T) {
	makeMinimalProject(t) // go.mod only; no docs/ops/*.md to replace regions in
	if err := generateSagaCoverage(nil); err == nil {
		t.Fatal("generateSagaCoverage without seeded docs: expected read error, got nil")
	}
}

// TestGenerateSagaCoverage_MissingMarkersErrors asserts a doc that exists but
// lacks the generated-region markers fails (writeDocRegion's ReplaceRegion
// error branch).
//
// Not parallel: makeMinimalProject uses os.Chdir (process-global).
func TestGenerateSagaCoverage_MissingMarkersErrors(t *testing.T) {
	root := makeMinimalProject(t)
	// readyz.md present but without markers → ReplaceRegion fails.
	writeSagaDoc(t, root, sagaReadyzRel, "# Readyz\n\nno markers here\n")
	writeSagaDoc(t, root, sagaAlertingRel, "# Alerting\n\nno markers here\n")
	if err := generateSagaCoverage(nil); err == nil {
		t.Fatal("generateSagaCoverage with markerless docs: expected ReplaceRegion error, got nil")
	}
}
