//go:build archtest

// INVARIANT: GENERATE-RSA-KEYPAIR-FUNNEL-01
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

// TestGENERATE_RSA_KEYPAIR_FUNNEL_01 dogfoods CheckGenerateRSAKeypairFunnel01: the
// two hardened composition roots (cmd/corebundle, examples/ssobff) must contain no
// direct auth.GenerateRSAKeyPair call — both route JWT signing keys through
// cellmodules/cellsecrets.LoadKeySet, whose demo branch is the only sanctioned home
// of ephemeral key generation (#2052; #825 / #2017 family).
func TestGENERATE_RSA_KEYPAIR_FUNNEL_01(t *testing.T) {
	t.Parallel()
	Report(t, "GENERATE-RSA-KEYPAIR-FUNNEL-01", CheckGenerateRSAKeypairFunnel01(t, ConfigForExternalCell{}))
}

// TestGENERATE_RSA_KEYPAIR_FUNNEL_01_RedFixture asserts the detector fires on a
// known-positive auth.GenerateRSAKeyPair call. The source is written to a temp file
// and parsed (never compiled) — parser.ParseFile reads it, the toolchain never does
// — so the fixture cannot pollute the production scan. Without this, a broken
// detector would silently pass (anti-vacuity).
func TestGENERATE_RSA_KEYPAIR_FUNNEL_01_RedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + runtimeAuthModule + "\"\n\n" +
		"var _, _, _ = auth.GenerateRSAKeyPair()\n"
	path := filepath.Join(t.TempDir(), "genrsa_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, runtimeAuthModule, "auth", "GenerateRSAKeyPair")
	require.NoError(t, err, "parse GenerateRSAKeyPair RED fixture")
	if !ok {
		t.Error("GENERATE-RSA-KEYPAIR-FUNNEL-01 RED fixture: detector found no " +
			"auth.GenerateRSAKeyPair; firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("GenerateRSAKeyPair RED fixture hit at line %d", line)
}

// TestGENERATE_RSA_KEYPAIR_FUNNEL_01_NoDotImportBlindSpot closes the dot-import
// blind spot: a dot-import of runtime/auth would make GenerateRSAKeyPair a bare
// ident the SelectorExpr scan misses. Asserts no production file in the hardened
// roots dot-imports runtime/auth.
func TestGENERATE_RSA_KEYPAIR_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.DirsScope(root, generateRSAKeypairHardenedRoots).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := funnelRelSlash(root, path)
		dot, perr := fileDotImportsModule(path, runtimeAuthModule)
		require.NoError(t, perr)
		if dot {
			findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades GENERATE-RSA-KEYPAIR-FUNNEL-01", rel, runtimeAuthModule))
		}
	}
	assert.Empty(t, findings,
		"dot-importing runtime/auth in a hardened root would make GenerateRSAKeyPair a bare "+
			"ident and evade the funnel scan; forbidden")
}
