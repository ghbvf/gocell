package errcode

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateGolden controls whether the golden file is regenerated.
// Set via environment variable ERRCODE_PREFIX_GOLDEN_UPDATE=1.
var updateGolden = os.Getenv("ERRCODE_PREFIX_GOLDEN_UPDATE") == "1"

func TestRegisterPrefix_DuplicateConflictPanics(t *testing.T) {
	// Use a unique prefix that the platform init never registers.
	const prefix = "ERR_TESTCASEDUP_"
	const ownerA = "ownerA"
	const ownerB = "ownerB"

	// First registration must succeed.
	RegisterPrefix(prefix, ownerA)

	// Second registration with a different owner must panic with an *Error
	// whose message names both owners.
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		RegisterPrefix(prefix, ownerB)
	}()

	require.NotNil(t, recovered, "expected panic on duplicate prefix with different owner")

	err, ok := recovered.(*Error)
	require.True(t, ok, "recovered value must be *Error, got %T: %v", recovered, recovered)
	assert.Contains(t, err.Message, ownerA, "panic message must mention first owner")
	assert.Contains(t, err.Message, ownerB, "panic message must mention second owner")
	assert.Contains(t, err.Message, prefix, "panic message must mention the conflicting prefix")
}

func TestRegisterPrefix_IdempotentSameOwner(t *testing.T) {
	const prefix = "ERR_TESTCASEIDEMPOTENT_"
	const owner = "idempotentOwner"

	// Two calls with identical (prefix, owner) must not panic.
	RegisterPrefix(prefix, owner)
	RegisterPrefix(prefix, owner)

	// The prefix must appear exactly once in the snapshot.
	entries := RegisteredPrefixes()
	count := 0
	for _, e := range entries {
		if e.Prefix == prefix {
			count++
		}
	}
	assert.Equal(t, 1, count, "same prefix+owner registered twice must appear exactly once")
}

func TestRegisterPrefix_EmptyArgPanics(t *testing.T) {
	t.Run("emptyPrefix", func(t *testing.T) {
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			RegisterPrefix("", "someOwner")
		}()
		require.NotNil(t, recovered, "expected panic on empty prefix")
		_, ok := recovered.(*Error)
		assert.True(t, ok, "recovered value must be *Error on empty prefix, got %T", recovered)
	})

	t.Run("emptyOwner", func(t *testing.T) {
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			RegisterPrefix("ERR_TESTEMPTYOWNER_", "")
		}()
		require.NotNil(t, recovered, "expected panic on empty owner")
		_, ok := recovered.(*Error)
		assert.True(t, ok, "recovered value must be *Error on empty owner, got %T", recovered)
	})
}

func TestRegisterPrefix_MalformedPrefixPanics(t *testing.T) {
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		RegisterPrefix("WRONG_PREFIX_", "someOwner")
	}()
	require.NotNil(t, recovered, "expected panic on malformed prefix (not starting with ERR_)")
	_, ok := recovered.(*Error)
	assert.True(t, ok, "recovered value must be *Error on malformed prefix, got %T", recovered)
}

func TestOwnerOfCode_LongestMatch(t *testing.T) {
	// These prefixes are unique to this test and don't conflict with platform init.
	const nsPrefix = "ERR_LMTESTAUTH_"
	const wholeCode = "ERR_LMTESTINTERNAL"
	const owner = "lmTestOwner"

	RegisterPrefix(nsPrefix, owner)
	RegisterPrefix(wholeCode, owner)

	t.Run("namespace match", func(t *testing.T) {
		got, ok := OwnerOfCode("ERR_LMTESTAUTH_FORBIDDEN")
		require.True(t, ok, "ERR_LMTESTAUTH_FORBIDDEN must match namespace ERR_LMTESTAUTH_")
		assert.Equal(t, owner, got)
	})

	t.Run("whole-code match", func(t *testing.T) {
		got, ok := OwnerOfCode("ERR_LMTESTINTERNAL")
		require.True(t, ok, "ERR_LMTESTINTERNAL must match whole-code entry")
		assert.Equal(t, owner, got)
	})

	t.Run("whole-code is NOT a prefix match (F1 false-green closed)", func(t *testing.T) {
		// A whole-code entry must claim ONLY its exact string. An extended code
		// that merely starts with the whole-code string must NOT resolve — else
		// the closed-set guard (which reuses OwnerOfCode) is false-green.
		_, ok := OwnerOfCode("ERR_LMTESTINTERNAL_DETAIL")
		assert.False(t, ok,
			"whole-code entry ERR_LMTESTINTERNAL must not claim the extended code "+
				"ERR_LMTESTINTERNAL_DETAIL by prefix")
	})

	t.Run("no match", func(t *testing.T) {
		_, ok := OwnerOfCode("ERR_UNKNOWN_ZZZZZ")
		assert.False(t, ok, "unregistered code must return !ok")
	})
}

// TestRegisterPrefix_CrossOwnerOverlapPanics proves F2: a new prefix whose
// claimed code-set overlaps a DIFFERENT owner's existing entry fail-fast panics,
// even though the prefix strings are not identical.
//
// NOTE: registry-mutating tests must NOT call t.Parallel() — the global registry
// is shared across all tests in the process.
func TestRegisterPrefix_CrossOwnerOverlapPanics(t *testing.T) {
	// Use test-only owners (never the platform owner) so successful
	// registrations here do not leak into the platform-filtered golden set.
	const seedOwner = "github.com/seedtest/mod"
	const otherOwner = "github.com/other/mod"

	// Seed a namespace owned by one module.
	RegisterPrefix("ERR_OVERLAPCONTAINER_", seedOwner)

	mustPanic := func(t *testing.T, prefix, owner, wantExisting string) {
		t.Helper()
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			RegisterPrefix(prefix, owner)
		}()
		require.NotNil(t, recovered, "expected cross-owner overlap panic for %q", prefix)
		err, ok := recovered.(*Error)
		require.True(t, ok, "recovered value must be *Error, got %T", recovered)
		assert.Contains(t, err.Message, prefix, "panic message must name the new prefix")
		assert.Contains(t, err.Message, wantExisting, "panic message must name the existing overlapping prefix")
	}

	t.Run("namespace under namespace", func(t *testing.T) {
		mustPanic(t, "ERR_OVERLAPCONTAINER_SUB_", otherOwner, "ERR_OVERLAPCONTAINER_")
	})
	t.Run("whole-code under namespace", func(t *testing.T) {
		mustPanic(t, "ERR_OVERLAPCONTAINER_LEAF", otherOwner, "ERR_OVERLAPCONTAINER_")
	})
	t.Run("namespace containing existing namespace (reverse direction)", func(t *testing.T) {
		// Seed a narrow namespace owned by other, then a broader one owned by
		// platform must panic (existing is under the new prefix).
		RegisterPrefix("ERR_REVERSEOVERLAP_INNER_", otherOwner)
		mustPanic(t, "ERR_REVERSEOVERLAP_", seedOwner, "ERR_REVERSEOVERLAP_INNER_")
	})
}

// TestRegisterPrefix_SameOwnerOverlapAllowed proves overlap WITHIN one owner is
// intentional (e.g. gocell owns both "ERR_AUTH_" and "ERR_AUTH_FORBIDDEN") and
// must NOT panic.
func TestRegisterPrefix_SameOwnerOverlapAllowed(t *testing.T) {
	// Test-only owner (not the platform owner) — what matters is that both
	// registrations share the SAME owner, not which owner it is. Keeps the
	// platform-filtered golden set clean.
	const owner = "github.com/sameowner/mod"

	RegisterPrefix("ERR_SAMEOWNEROVERLAP_", owner)
	// Same owner: a whole-code under the namespace and a sub-namespace are both
	// legal refinements and must not panic.
	assert.NotPanics(t, func() { RegisterPrefix("ERR_SAMEOWNEROVERLAP_LEAF", owner) })
	assert.NotPanics(t, func() { RegisterPrefix("ERR_SAMEOWNEROVERLAP_SUB_", owner) })
}

func TestRegisteredPrefixes_SortedSnapshot(t *testing.T) {
	// RegisteredPrefixes must return a sorted slice.
	entries := RegisteredPrefixes()
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Prefix > entries[i].Prefix {
			t.Errorf("RegisteredPrefixes not sorted: entries[%d].Prefix %q > entries[%d].Prefix %q",
				i-1, entries[i-1].Prefix, i, entries[i].Prefix)
		}
	}
}

// TestPlatformOwnerFilter verifies that RegisteredPrefixes filtered by the
// platform owner constant excludes prefixes registered under a different owner.
// This proves the golden test's platform-owner filter logic works correctly and
// does not accidentally absorb external module prefixes into the platform set.
//
// NOTE: registry-mutating tests must NOT call t.Parallel() — the global registry
// is shared across all tests in the process.
func TestPlatformOwnerFilter(t *testing.T) {
	const nonPlatformPrefix = "ERR_NONPLATFORMTEST_"
	const nonPlatformOwner = "github.com/other/mod"

	RegisterPrefix(nonPlatformPrefix, nonPlatformOwner)

	const platformOwner = "github.com/ghbvf/gocell"
	entries := RegisteredPrefixes()

	// The non-platform prefix must NOT appear in the platform-filtered set.
	for _, e := range entries {
		if e.Prefix == nonPlatformPrefix && e.Owner == platformOwner {
			t.Errorf("non-platform prefix %q must not appear in platform-owner filtered set", nonPlatformPrefix)
		}
	}

	// The prefix IS registered (different owner) — OwnerOfCode must resolve it.
	owner, ok := OwnerOfCode(nonPlatformPrefix + "SOMETHING")
	if !ok {
		t.Errorf("OwnerOfCode(%q) must find the non-platform registration; got !ok", nonPlatformPrefix+"SOMETHING")
	}
	if ok && owner != nonPlatformOwner {
		t.Errorf("OwnerOfCode returned owner %q, want %q", owner, nonPlatformOwner)
	}
}

func TestGocellPrefixSetMatchesGolden(t *testing.T) {
	entries := RegisteredPrefixes()

	// Only include platform-owned entries in the golden file. Test-only
	// prefixes (registered by other tests in the same process using unique
	// test-only owners) are excluded so the golden stays stable.
	const platformOwner = "github.com/ghbvf/gocell"
	var platformEntries []PrefixOwner
	for _, e := range entries {
		if e.Owner == platformOwner {
			platformEntries = append(platformEntries, e)
		}
	}

	// Build the canonical multiline representation: one "prefix\towner" per line.
	lines := make([]string, len(platformEntries))
	for i, e := range platformEntries {
		lines[i] = fmt.Sprintf("%s\t%s", e.Prefix, e.Owner)
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"

	// Find the testdata directory relative to this test file.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	goldenPath := filepath.Join(filepath.Dir(thisFile), "testdata", "prefix_set.golden")

	if updateGolden {
		err := os.MkdirAll(filepath.Dir(goldenPath), 0o755)
		require.NoError(t, err)
		err = os.WriteFile(goldenPath, []byte(got), 0o644)
		require.NoError(t, err)
		t.Logf("golden file updated: %s", goldenPath)
		return
	}

	goldenBytes, err := os.ReadFile(goldenPath) //nolint:gosec // path is constructed from runtime.Caller, not user input
	if os.IsNotExist(err) {
		t.Fatalf("golden file not found at %s — run: ERRCODE_PREFIX_GOLDEN_UPDATE=1 go test ./pkg/errcode/...", goldenPath)
	}
	require.NoError(t, err)
	want := string(goldenBytes)
	const goldenMismatchMsg = "prefix registry golden mismatch — " +
		"if intentional, run: ERRCODE_PREFIX_GOLDEN_UPDATE=1 go test ./pkg/errcode/..."
	assert.Equal(t, want, got, goldenMismatchMsg)
}
