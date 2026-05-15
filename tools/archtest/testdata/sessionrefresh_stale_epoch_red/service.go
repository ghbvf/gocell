// Package fixture is the RED fixture for SESSIONREFRESH-STALE-EPOCH-REJECT-01.
// It declares a refreshInTx-like function WITHOUT the S4d stale-epoch check
// (no row != user inequality, no "stale-epoch" stage marker). archtest must
// detect both absences.
//
// This file is NOT compiled by production; it is parsed only as AST data by
// TestSessionrefreshStaleEpochReject_RedFixtureDetected.
package fixture

// Service is a minimal stand-in for sessionrefresh.Service in the production
// code; we only care about refreshInTx existing for AST scanning.
type Service struct{}

// User mirrors the shape that the production refreshInTx reads.
type User struct {
	AuthzEpoch int64
}

// Token mirrors the shape that the production refreshInTx reads from the
// refresh store.
type Token struct {
	AuthzEpochAtIssue int64
}

// refreshInTx — the broken form. It reads both fields but does NOT compare
// them with `!=` and does NOT route into the unified cascade with the
// "stale-epoch" stage marker. The S4d guard must catch this regression.
func (s *Service) refreshInTx(presented *Token, user *User) error {
	// Touch the fields so the SelectorExpr scan still passes prongs 1+2 —
	// the only thing missing is the inequality + marker. This isolates the
	// detector to the truly load-bearing prongs.
	_ = presented.AuthzEpochAtIssue
	_ = user.AuthzEpoch
	return nil
}
