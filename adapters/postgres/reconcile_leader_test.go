package postgres

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// reconcilePGTestLeaseTTL is the lease duration shared by the PG reconcile elector
// unit + integration tests (TEST-TIME-LITERAL-01: literal lives in a package-level
// const). Declared in this untagged file so the integration-tagged file can share it
// without a duplicate declaration (mirrors the redis adapter's reconcileTestLeaseTTL).
const reconcilePGTestLeaseTTL = 30 * time.Second

// mustPGLease builds the shared test LeaseTTL from the package-level literal const
// (the elector now takes a validated reconcile.LeaseTTL, not a raw time.Duration —
// sub-ms truncation is foreclosed at NewLeaseTTL, see kernel/reconcile/leasettl.go).
func mustPGLease(t *testing.T) reconcile.LeaseTTL {
	t.Helper()
	l, err := reconcile.NewLeaseTTL(reconcilePGTestLeaseTTL)
	require.NoError(t, err)
	return l
}

// TestEpochToUint64 covers the BIGINT→uint64 epoch conversion's defensive guard:
// reconcile_leases.epoch is monotonic and seeded at 1, so a negative value is DB
// corruption and is clamped to 0 (a fail-safe lowest epoch the FencedWriter rejects)
// rather than wrapping to a huge uint64 that would falsely outrank live tokens.
func TestEpochToUint64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   int64
		want uint64
	}{
		{"negative_clamps_to_zero", -1, 0},
		{"min_int64_clamps_to_zero", math.MinInt64, 0},
		{"zero", 0, 0},
		{"one", 1, 1},
		{"large_positive", math.MaxInt64, uint64(math.MaxInt64)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, epochToUint64(tt.in))
		})
	}
}

// TestNewReconcileElector_ConstructorValidation covers the constructor guard
// branches: a nil pool and the unconstructed zero-value lease each fail-fast, while a
// non-nil pool + a validated LeaseTTL builds an elector (no DB I/O — the guards
// return before the pool is touched). Sub-ms lease rejection lives upstream in
// NewLeaseTTL (TestNewLeaseTTL); the zero-value LeaseTTL{} (ms==0) is the only invalid
// lease a caller can still pass to the elector.
func TestNewReconcileElector_ConstructorValidation(t *testing.T) {
	t.Run("nil_pool", func(t *testing.T) {
		_, err := NewReconcileElector(nil, mustPGLease(t))
		require.Error(t, err)
	})
	t.Run("zero_value_lease", func(t *testing.T) {
		// the unconstructed LeaseTTL{} (ms==0) must fail-fast, not yield a 0ms lease
		_, err := NewReconcileElector(&Pool{}, reconcile.LeaseTTL{})
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		require.Equal(t, errcode.ErrCellInvalidConfig, ec.Code,
			"a zero-value lease is a config mistake, routable separately from a postgres connect failure")
	})
	t.Run("valid", func(t *testing.T) {
		// &Pool{} (inner nil) suffices: the guards return before any DB access, so a
		// non-nil pool + a validated lease yields an elector.
		e, err := NewReconcileElector(&Pool{}, mustPGLease(t))
		require.NoError(t, err)
		require.NotNil(t, e)
	})
}
