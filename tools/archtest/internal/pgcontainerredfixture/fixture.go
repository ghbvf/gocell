//go:build archtest_fixture

// Package pgcontainerredfixture is the RED fixture for PG-TESTCONTAINER-FUNNEL-01.
// It deliberately starts a per-test container via tcpostgres.Run OUTSIDE the
// pgclone funnel so the funnel detector has a known-positive to fire on.
//
// Built only under the archtest_fixture tag. The funnel's RED self-check reaches
// it by direct path (TestPG_TESTCONTAINER_FUNNEL_01_RedFixture, via
// parser.ParseFile which ignores build constraints); repo-rooted production
// scans never report it because the scanner excludes all of
// tools/archtest/internal/ (see archtestInternalRel in internal/scanner/scope.go).
package pgcontainerredfixture

import (
	"context"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Boot is the forbidden shape: a direct tcpostgres.Run outside pgclone.
func Boot(ctx context.Context) (*tcpostgres.PostgresContainer, error) {
	return tcpostgres.Run(ctx, "postgres:15-alpine")
}
