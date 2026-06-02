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

	t.Run("no match", func(t *testing.T) {
		_, ok := OwnerOfCode("ERR_UNKNOWN_ZZZZZ")
		assert.False(t, ok, "unregistered code must return !ok")
	})
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
		t.Fatalf("golden file not found at %s — run with ERRCODE_PREFIX_GOLDEN_UPDATE=1 to generate", goldenPath)
	}
	require.NoError(t, err)
	want := string(goldenBytes)
	assert.Equal(t, want, got, "prefix registry golden mismatch — if intentional, run with ERRCODE_PREFIX_GOLDEN_UPDATE=1")
}
