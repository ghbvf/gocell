package query

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// LogCursorError returns a CursorErrorFunc that emits a structured slog.Info
// record when a cursor validation fails. Returns nil if logger is nil.
//
// The raw cursor string is intentionally omitted — opaque base64 that may
// encode internal offsets; aligned with k8s apiserver / etcd / MinIO practice.
//
// Level is Info (not Warn) because cursor errors are client input failures,
// not server-side degradation.
func LogCursorError(logger *slog.Logger, sliceName string) CursorErrorFunc {
	if logger == nil {
		return nil
	}
	return func(ctx context.Context, phase string, err error) {
		attrs := []any{
			"slice", sliceName,
			"reason", phase,
			"error", err.Error(),
		}
		if rid, ok := ctxkeys.RequestIDFrom(ctx); ok && rid != "" {
			attrs = append(attrs, "request_id", rid)
		}
		logger.Info("invalid cursor", attrs...)
	}
}
