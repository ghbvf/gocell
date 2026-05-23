//go:build archtest_fixture

// Package pgcontainerredfixture is the RED fixture for PG-TESTCONTAINER-FUNNEL-01.
// It deliberately starts a per-test container via tcpostgres.Run OUTSIDE the
// pgclone funnel so the funnel detector has a known-positive to fire on.
//
// Built only under the archtest_fixture tag. The funnel scan reaches it via
// parser.ParseFile (which ignores build constraints); the production scan
// skips tools/archtest/internal/ so this fixture is never itself reported.
package pgcontainerredfixture

import (
	"context"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Boot is the forbidden shape: a direct tcpostgres.Run outside pgclone.
func Boot(ctx context.Context) (*tcpostgres.PostgresContainer, error) {
	return tcpostgres.Run(ctx, "postgres:15-alpine")
}
