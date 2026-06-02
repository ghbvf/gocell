//go:build archtest_fixture

// Package redsagaslogbareinstanceid is a RED fixture for
// SAGA-SLOG-INSTANCE-FIELDS-CALLER-01: a production-shaped log site emits a bare
// slog.String("instance_id", …) outside the sagalog.InstanceFields carrier,
// bypassing the per-instance lease_id funnel. The A1 detector must flag the
// slog.String call. Expect one diagnostic.
package redsagaslogbareinstanceid
