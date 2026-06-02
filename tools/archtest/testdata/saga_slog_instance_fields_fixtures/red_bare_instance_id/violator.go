//go:build archtest_fixture

package redsagaslogbareinstanceid

import (
	"context"
	"log/slog"
)

// emitBareInstanceID logs a per-instance saga record with a hand-written
// slog.String("instance_id", …) instead of routing through
// sagalog.InstanceFields. SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 A1 must flag the
// slog.String("instance_id", …) call.
func emitBareInstanceID(ctx context.Context, logger *slog.Logger) {
	logger.LogAttrs(ctx, slog.LevelWarn, "saga: bare per-instance log",
		slog.String("instance_id", "inst-x"))
}

// emitBareLeaseIDViaAny uses slog.Any("lease_id", …) — a DIFFERENT log/slog Attr
// constructor than slog.String. A1 must still flag it (the detector matches any
// log/slog Attr constructor by signature, not just slog.String): #1266 review
// C1/F1.
func emitBareLeaseIDViaAny(ctx context.Context, logger *slog.Logger) {
	logger.LogAttrs(ctx, slog.LevelWarn, "saga: bare per-instance log via Any",
		slog.Any("lease_id", "lease-x"))
}
