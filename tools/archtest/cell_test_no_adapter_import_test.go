// INVARIANT: CELL-TEST-NO-ADAPTER-IMPORT-01: cell unit tests must not import
// the platform adapters/ layer; they must use canonical in-mem fakes instead.
//
// # Why
//
// CLAUDE.md layering: cells/ depend on kernel/ + runtime/ but NOT adapters/
// (decoupled via interfaces). Production cell code already obeys this via the
// cells-isolation depguard rule. But every depguard isolation rule EXEMPTS
// *_test.go files, so cell *unit* tests could import the real adapters/ package
// (pulling in heavy SDKs like pgx and coupling tests to adapter internals)
// instead of the framework's canonical in-mem implementations. This rule closes
// that gap: it makes the "decoupling" benefit transfer from prod to test, which
// the 2026-05-04 systems-layer review (§3) flagged but never enforced (#803).
//
// # Canonical in-mem fakes (use these, never the real adapter)
//
//   - session.Store         → runtime/auth/session.MemStore
//   - refresh.Store         → runtime/auth/refresh/memstore
//   - outbox publish/sub     → runtime/eventbus.InMemoryEventBus
//   - outbox.Store / record  → runtime/outbox/outboxtest.{Recorder,FakeStore}
//   - distlock.Driver        → runtime/distlock/locktest.FakeDriver
//   - crypto.KeyProvider     → runtime/crypto.LocalAESKeyProvider
//   - persistence.TxRunner   → kernel/outbox.DemoTxRunner
//   - cell ports (repos)     → cells/<cell>/internal/mem, cells/accesscore/mem.Bundle
//
// Why NOT an adapters/<name>/<name>fake/ subpackage (the #803 literal ask):
// (1) cells importing it would make cells/ → adapters/ — a layering violation;
// (2) it duplicates the interface-layer in-mem impls above (parallel structure);
// (3) s3/oidc/websocket/rabbitmq are not in any cell's injection surface — dead
// code. OSS precedent agrees: the correct shape is in-mem at the interface layer
// (Watermill pubsub/gochannel, go-micro registry/memory), not a sibling fake per
// adapter (K8s client-go/fake works only because its interface+impl share a
// package — a premise GoCell's layering does not have).
//
// # Exemption boundary = build tag, NOT filename
//
// Real integration/e2e tests legitimately use real adapters + testcontainers and
// are gated behind //go:build integration (or e2e, etc.). The exemption is keyed
// on the ACTUAL build constraint — a file is exempt iff it is NOT visible under
// the default (no-extra-tags) build context. This is tag-name-agnostic (covers
// integration / e2e / any future tag) and cannot be silently gamed by renaming a
// file to *_integration_test.go (a filename-based exemption would be Soft: the
// repo has integration tests named plainly with only the build tag, e.g.
// cells/accesscore/slices/identitymanage/service_test.go, so filename is not a
// reliable proxy and renaming would dodge the rule). To escape this rule an
// author must add //go:build integration, which removes the test from the normal
// unit run — a visible behavior change, not a silent bypass.
//
// # AI-robust grading
//
// downstream (the import ban): archtest Medium — "a test file must not import a
// package" has no type-system Hard form (Go permits any import); archtest AST +
// build-constraint eval (fail-closed in CI) is the ceiling for this rule shape.
// The exemption boundary is build-tag-based (not renameable), which is strictly
// more robust than a depguard filename exemption. depguard is deliberately NOT
// used here: it matches paths only and cannot evaluate //go:build, so it cannot
// express the correct (build-tag) exemption. Routed to archtest per ai-robust
// "纯 AST 模式 → archtest.Run".
//
// # Blind spots (out of this rule's declared range) + reverse self-checks
//
//   - aliased import (adapterpg "...adapters/postgres"): COVERED — we match the
//     import path literal, not the local name (fixture: aliased_import).
//   - dot import (. "...adapters/s3"): COVERED — path literal still matches
//     (fixture: dot_import).
//   - cell-internal adapters (cells/<c>/internal/adapters/...): NOT a platform
//     adapter; the prefix github.com/ghbvf/gocell/adapters/ does not match the
//     cells/.../internal/adapters/ path (fixture: cell_internal_adapter).
//   - transitive import (cell test imports a helper that imports adapters): OUT
//     OF SCOPE — this rule bans direct imports only. A transitively-importing
//     helper living under cells/ would itself be caught if it is a test file.
//   - production cell .go importing adapters: OUT OF SCOPE here — governed by the
//     cells-isolation depguard rule (fixture: production_file).
//   - non-cell test files (runtime/, kernel/): OUT OF SCOPE — #803 is cells-only
//     (fixture: non_cell_test).
package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const platformAdapterImportPrefix = "github.com/ghbvf/gocell/adapters"

// TestCellTestNoAdapterImport asserts no cell (or example-cell) unit test file
// imports the platform adapters/ layer. Integration/e2e tests (build-tag gated)
// are exempt.
func TestCellTestNoAdapterImport(t *testing.T) {
	root := findModuleRoot(t)
	findings, err := cellTestAdapterImportFindings(root)
	require.NoError(t, err)
	assert.Empty(t, findings,
		"cell unit tests must not import platform adapters/; use canonical in-mem "+
			"fakes (session.MemStore / refresh memstore / eventbus.InMemoryEventBus / "+
			"outboxtest.Recorder / distlock locktest / crypto.LocalAESKeyProvider / "+
			"outbox.DemoTxRunner / cells/*/internal/mem). If this is a real integration "+
			"test that needs a live adapter, gate it behind //go:build integration. "+
			"See CELL-TEST-NO-ADAPTER-IMPORT-01 and issue #803.\nViolations:\n%s",
		strings.Join(findings, "\n"))
}

// cellTestAdapterImportFindings walks every test file under cells/** and
// examples/*/cells/** that is visible in the default build context (i.e. a real
// unit test, not gated behind a build tag) and reports any direct import of the
// platform adapters/ layer.
func cellTestAdapterImportFindings(root string) ([]string, error) {
	files, err := ModuleScope(root, IncludeTests()).Files()
	if err != nil {
		return nil, err
	}

	var findings []string
	for _, path := range files {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		relSlash := filepath.ToSlash(rel)
		if !isCellOrExampleCellPath(relSlash) {
			continue
		}

		visible, vErr := fileDefaultVisible(path)
		if vErr != nil {
			return nil, vErr
		}
		if !visible {
			continue // build-tag gated (integration/e2e/...) — legitimately uses real adapters
		}

		imports, iErr := fileImportPaths(path)
		if iErr != nil {
			return nil, iErr
		}
		for _, p := range imports {
			if isPlatformAdapterImport(p) {
				findings = append(findings, relSlash+": imports "+p)
			}
		}
	}
	return findings, nil
}

// isCellOrExampleCellPath reports whether a module-relative slash path is under
// cells/ or examples/<x>/cells/.
func isCellOrExampleCellPath(relSlash string) bool {
	if strings.HasPrefix(relSlash, "cells/") {
		return true
	}
	if !strings.HasPrefix(relSlash, "examples/") {
		return false
	}
	// examples/<example>/cells/...
	rest := strings.TrimPrefix(relSlash, "examples/")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return false
	}
	return strings.HasPrefix(rest[slash+1:], "cells/")
}

// fileDefaultVisible reports whether the file is compiled under the default
// (no-extra-tags) build context. A file with no //go:build directive is visible;
// a file gated behind a tag (integration/e2e/...) that is false under defaults
// is NOT visible and is exempt from this rule.
func fileDefaultVisible(path string) (bool, error) {
	expr, err := ParseBuildConstraint(path)
	if err != nil {
		return false, err
	}
	if expr == nil {
		return true, nil
	}
	return expr.Eval(BuildContextPredicate()), nil
}

// fileImportPaths returns the import path literals of a Go file.
func fileImportPaths(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, imp := range file.Imports {
		if p := archStringLiteralValue(imp.Path); p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// isPlatformAdapterImport reports whether an import path is the platform
// adapters/ layer (github.com/ghbvf/gocell/adapters[/...]). It deliberately does
// NOT match cell-internal adapters (github.com/ghbvf/gocell/cells/.../internal/adapters/...).
func isPlatformAdapterImport(p string) bool {
	return p == platformAdapterImportPrefix ||
		strings.HasPrefix(p, platformAdapterImportPrefix+"/")
}

// TestCellTestNoAdapterImport_FixtureMetaTest verifies the walker classifies
// synthetic files correctly across the blind spots documented in the package
// godoc: aliased import, dot import, cell-internal adapter, build-tag exemption,
// example-cell scope, non-cell out-of-scope, production-file out-of-scope.
func TestCellTestNoAdapterImport_FixtureMetaTest(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name    string // also the subdir; reused in the relpath
		rel     string // module-relative path of the file to write
		content string
		wantHit bool
	}{
		{
			name:    "plain_cell_unit_import",
			rel:     "cells/a/x_test.go",
			content: "package a\nimport _ \"github.com/ghbvf/gocell/adapters/postgres\"\n",
			wantHit: true,
		},
		{
			name:    "integration_gated_exempt",
			rel:     "cells/b/y_test.go",
			content: "//go:build integration\n\npackage b\nimport _ \"github.com/ghbvf/gocell/adapters/postgres\"\n",
			wantHit: false,
		},
		{
			name:    "e2e_gated_exempt",
			rel:     "cells/h/h_test.go",
			content: "//go:build e2e\n\npackage h\nimport _ \"github.com/ghbvf/gocell/adapters/redis\"\n",
			wantHit: false,
		},
		{
			name:    "aliased_import",
			rel:     "cells/c/z_test.go",
			content: "package c\nimport pg \"github.com/ghbvf/gocell/adapters/redis\"\nvar _ = pg.Nil\n",
			wantHit: true,
		},
		{
			name:    "dot_import",
			rel:     "cells/d/w_test.go",
			content: "package d\nimport . \"github.com/ghbvf/gocell/adapters/s3\"\n",
			wantHit: true,
		},
		{
			name:    "cell_internal_adapter",
			rel:     "cells/e/e_test.go",
			content: "package e\nimport _ \"github.com/ghbvf/gocell/cells/e/internal/adapters/postgres\"\n",
			wantHit: false,
		},
		{
			name:    "example_cell_in_scope",
			rel:     "examples/demo/cells/f/u_test.go",
			content: "package f\nimport _ \"github.com/ghbvf/gocell/adapters/postgres\"\n",
			wantHit: true,
		},
		{
			name:    "non_cell_test_out_of_scope",
			rel:     "runtime/g/r_test.go",
			content: "package g\nimport _ \"github.com/ghbvf/gocell/adapters/postgres\"\n",
			wantHit: false,
		},
		{
			name:    "production_file_out_of_scope",
			rel:     "cells/i/prod.go",
			content: "package i\nimport _ \"github.com/ghbvf/gocell/adapters/postgres\"\n",
			wantHit: false,
		},
	}

	root := t.TempDir()
	for _, fx := range fixtures {
		full := filepath.Join(root, filepath.FromSlash(fx.rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(fx.content), 0o644))
	}

	findings, err := cellTestAdapterImportFindings(root)
	require.NoError(t, err)

	for _, fx := range fixtures {
		relSlash := fx.rel
		hit := false
		for _, f := range findings {
			if strings.HasPrefix(f, relSlash+":") {
				hit = true
				break
			}
		}
		assert.Equal(t, fx.wantHit, hit, "fixture %s: import-detection result", fx.name)
	}
}
