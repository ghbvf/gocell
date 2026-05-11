// INVARIANT: TYPESEVAL-BUILDCONTEXT-PREDICATE-01

package typeseval_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

func TestBuildContextPredicate_ImplicitDefaults(t *testing.T) {
	t.Parallel()
	pred := typeseval.BuildContextPredicate()
	// Implicit toolchain defaults must be true.
	for _, tag := range []string{
		"linux", "darwin", "windows", "freebsd",
		"amd64", "arm64", "386", "wasm",
		"cgo", "unix", "gc",
		"go1.1", "go1.18", "go1.25",
	} {
		require.True(t, pred(tag), "expected %q to be implicit default", tag)
	}
}

func TestBuildContextPredicate_NotImplicitDefaults(t *testing.T) {
	t.Parallel()
	pred := typeseval.BuildContextPredicate()
	// Project-specific tags MUST NOT be implicit defaults.
	for _, tag := range []string{
		"integration", "e2e", "examples_smoke",
		"catalog_gen", "never", "archtest_fixture",
		"boringcrypto", "msan", "asan", "race", // experiment/build-flag, non-default
		"gccgo", // alternate compiler — not the default
	} {
		require.False(t, pred(tag), "expected %q NOT to be implicit default", tag)
	}
}

func TestBuildContextPredicate_ExtraTags(t *testing.T) {
	t.Parallel()
	pred := typeseval.BuildContextPredicate("integration", "e2e")
	require.True(t, pred("integration"))
	require.True(t, pred("e2e"))
	require.True(t, pred("linux"))           // still implicit-true
	require.False(t, pred("examples_smoke")) // not in extra, not implicit
}

func TestBuildContextPredicate_NoExtraTags(t *testing.T) {
	t.Parallel()
	pred := typeseval.BuildContextPredicate()
	require.True(t, pred("linux"))
	require.False(t, pred("integration"))
}
