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
