//go:build archtest_fixture

// fixture_service.go is intentionally NOT a _repo.go / _store.go file. It
// proves R3 (call-bound ExecDirect approval) is GLOBAL — R3 scans every
// production file regardless of filename, unlike R1/R2 which remain
// file-extension scoped (#1206). The bad-form ExecDirect below MUST be flagged
// by R3 even though this filename would exempt it from R1/R2.
package pgrepoambienttxfixture

import (
	"context"

	"github.com/ghbvf/gocell/pkg/pgrepoapproved"
	"github.com/ghbvf/gocell/tools/archtest/internal/pgrepoambienttxfixture/internal/pgexec"
)

// serviceLayerBadExecDirect passes a reused (non-inline) approval — R3 must
// flag it here in a non-_repo.go file (global-scope proof).
func serviceLayerBadExecDirect(db pgexec.PGExecutor, ctx context.Context) { //nolint:unused // RED fixture
	a := pgrepoapproved.Approve(pgrepoapproved.RevokeSessionCascade)
	_, _ = pgexec.ExecDirect(a, db, ctx, "SELECT 1")
}
