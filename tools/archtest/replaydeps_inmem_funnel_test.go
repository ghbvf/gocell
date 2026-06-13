//go:build archtest

// INVARIANT: REPLAYDEPS-INMEM-FUNNEL-01
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestREPLAYDEPS_INMEM_FUNNEL_01 dogfoods CheckReplaydepsInmemFunnel01: the two
// hardened composition roots (cmd/corebundle, examples/ssobff) must contain no
// direct idempotency.NewInMemClaimer / auth.NewInMemoryNonceStore call — both
// route through cellmodules/replaydeps.Resolve, whose demo branch is the only
// sanctioned home of the in-memory primitives (#825 / #2017).
func TestREPLAYDEPS_INMEM_FUNNEL_01(t *testing.T) {
	t.Parallel()
	Report(t, "REPLAYDEPS-INMEM-FUNNEL-01", CheckReplaydepsInmemFunnel01(t, ConfigForExternalCell{}))
}

// TestREPLAYDEPS_INMEM_FUNNEL_01_ClaimerRedFixture asserts the detector fires on
// a known-positive idempotency.NewInMemClaimer call. The source is written to a
// temp file and parsed (never compiled) — parser.ParseFile reads it, the
// toolchain never does — so the fixture cannot pollute the production scan.
// Without this, a broken detector would silently pass (anti-vacuity).
func TestREPLAYDEPS_INMEM_FUNNEL_01_ClaimerRedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + idempotencyInMemModule + "\"\n\n" +
		"var _ = idempotency.NewInMemClaimer(nil)\n"
	path := filepath.Join(t.TempDir(), "claimer_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, idempotencyInMemModule, "idempotency", "NewInMemClaimer")
	require.NoError(t, err, "parse claimer RED fixture")
	if !ok {
		t.Error("REPLAYDEPS-INMEM-FUNNEL-01 claimer RED fixture: detector found no " +
			"idempotency.NewInMemClaimer; firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("claimer RED fixture hit at line %d", line)
}

// TestREPLAYDEPS_INMEM_FUNNEL_01_NonceRedFixture asserts the detector fires on a
// known-positive auth.NewInMemoryNonceStore call.
func TestREPLAYDEPS_INMEM_FUNNEL_01_NonceRedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + runtimeAuthModule + "\"\n\n" +
		"var _ = auth.NewInMemoryNonceStore\n"
	path := filepath.Join(t.TempDir(), "nonce_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, runtimeAuthModule, "auth", "NewInMemoryNonceStore")
	require.NoError(t, err, "parse nonce RED fixture")
	if !ok {
		t.Error("REPLAYDEPS-INMEM-FUNNEL-01 nonce RED fixture: detector found no " +
			"auth.NewInMemoryNonceStore; firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("nonce RED fixture hit at line %d", line)
}

// TestREPLAYDEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot closes the dot-import blind
// spot: a dot-import of kernel/idempotency or runtime/auth would make the funnel
// symbols bare idents the SelectorExpr scan misses. Asserts no production file in
// the hardened roots dot-imports either module.
func TestREPLAYDEPS_INMEM_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, replaydepsHardenedRoots).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		for _, mod := range []string{idempotencyInMemModule, runtimeAuthModule} {
			dot, perr := fileDotImportsModule(path, mod)
			require.NoError(t, perr)
			if dot {
				findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades REPLAYDEPS-INMEM-FUNNEL-01", rel, mod))
			}
		}
	}
	assert.Empty(t, findings,
		"dot-importing kernel/idempotency or runtime/auth in a hardened root would make the "+
			"in-mem constructors bare idents and evade the funnel scan; forbidden")
}
