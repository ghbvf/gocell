// Package projection_prefix_bypass_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/A6.
//
// It bare-concatenates the projection readiness-probe infix "_projection_"
// instead of routing through healthz.ProjectionReadyProbeName. The archtest
// scanner (scanA6ProjectionPrefixBypass) must detect this BinaryExpr as an A6
// violation — the magic infix must be single-sourced through the constructor so
// a dashboard/metric/log value cannot drift from the canonical probe name.
//
// DO NOT use this package in production code.
package projection_prefix_bypass_red

// dashboardKey demonstrates the A6 violation: hardcoding the "<cell>_projection_
// <name>_ready" probe-name shape via bare string concat instead of calling
// healthz.ProjectionReadyProbeName(cellID, projectionID).
func dashboardKey(cellID, projectionID string) string {
	// VIOLATION A6: bare concat of the "_projection_" probe infix.
	return cellID + "_projection_" + projectionID + "_ready"
}
