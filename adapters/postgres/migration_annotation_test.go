package postgres

import (
	"bufio"
	"bytes"
	"io/fs"
	"strings"
	"testing"
)

// TestMigrations_NoStrayGooseAnnotation guards against a `+goose` token appearing
// in a migration comment as PROSE rather than as a standalone directive.
//
// Why this exists: goose's SQL parser treats a line carrying `-- +goose <...>` as
// an annotation. A `+goose` token buried in prose (e.g. quoting the no-transaction
// directive verbatim inside an explanatory comment) is parsed as a MALFORMED
// annotation and fails `migrate up` with "invalid annotation". That failure only
// surfaces in the integration shards (which spin up PostgreSQL to apply
// migrations), costing a full red CI round. This fast non-integration check catches
// it at the source — and is non-vacuous: it fails on exactly the prose-token shape
// that broke migration 061 during #1868.
//
// Rule: every line that contains `+goose`, after trimming leading whitespace, MUST
// start with `-- +goose` (a clean directive). Anything else is a stray token —
// paraphrase the directive in prose instead of quoting it verbatim.
func TestMigrations_NoStrayGooseAnnotation(t *testing.T) {
	t.Parallel()
	fsys, err := MigrationsFS()
	if err != nil {
		t.Fatalf("MigrationsFS: %v", err)
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		scanned++

		sc := bufio.NewScanner(bytes.NewReader(body))
		for ln := 1; sc.Scan(); ln++ {
			line := sc.Text()
			if !strings.Contains(line, "+goose") {
				continue
			}
			if !strings.HasPrefix(strings.TrimSpace(line), "-- +goose") {
				t.Errorf("%s:%d: stray `+goose` token in a non-directive line — goose parses "+
					"this as a malformed annotation and `migrate up` fails. Paraphrase the "+
					"directive instead of quoting it verbatim.\n  line: %q", e.Name(), ln, line)
			}
		}
		if err := sc.Err(); err != nil {
			t.Fatalf("scan %s: %v", e.Name(), err)
		}
	}

	if scanned == 0 {
		t.Fatal("no .sql migrations scanned; the embedded migration set must be non-empty")
	}
}
