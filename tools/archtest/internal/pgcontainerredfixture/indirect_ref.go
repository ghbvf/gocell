//go:build archtest_fixture

package pgcontainerredfixture

import (
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// indirectRun is the forbidden function-value form of the funnel violation:
// assigning tcpostgres.Run to a variable evades a CallExpr-only scan (the call
// happens later through `indirectRun`, whose Fun is a bare *ast.Ident).
// PG-TESTCONTAINER-FUNNEL-01's SelectorExpr scan must still catch the
// tcpostgres.Run reference on the assignment RHS here.
var indirectRun = tcpostgres.Run
