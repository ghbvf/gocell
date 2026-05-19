//go:build integration

package integration

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestJConfighotreloadAccessApply implements journeys/J-confighotreload.yaml
// passCriteria "accesscore 接收并应用配置 upsert/delete 状态"
// — checkRef journey.J-confighotreload.access-apply.
//
// J-confighotreload is lifecycle: active (promoted from experimental in this PR),
// so governance VERIFY-06 runs this test inside `gocell validate --strict`.
// The test MUST be Docker-free.
//
// The criterion asserts that accesscore's configreceive slice correctly handles
// event.config.entry-upserted.v1 and event.config.entry-deleted.v1:
//   - HandleEntryUpserted Acks a valid payload
//   - HandleEntryDeleted Acks a valid payload
//   - Both Reject on invalid payload (permanent unmarshal error → dead letter)
//
// configreceive.Service is a public type with no internal-package dependency;
// it is constructable directly from tests/integration/ without Docker wiring.
// The service is currently observability-only (log-only, no side effects), so
// Ack is the load-bearing assertion for "applied" in the current implementation.
// Limitation: Ack alone cannot prove cache write, TTL refresh, or any real
// side-effect; when configreceive introduces actual cache-apply side effects,
// this test MUST be extended with direct state assertions (e.g. cache.GetVersion).
// Tracked in docs/backlog/cap-14-tooling.md JOURNEY-CONFIGHOTRELOAD-CRITERIA-EXPANSION-01.
//
// Layer compromise: tests/integration/ cannot import
// cells/accesscore/internal/... (Go internal-package barrier). configreceive
// has no such barrier — its package path is
// cells/accesscore/slices/configreceive (public slice directory).
//
// ref: cells/accesscore/slices/configreceive/service_test.go (in-package mirror).
func TestJConfighotreloadAccessApply(t *testing.T) {
	t.Parallel()

	svc := configreceive.NewService(slog.New(slog.DiscardHandler))
	ctx := context.Background()

	// --- sub-test: upsert Ack ---
	t.Run("upsert_acks", func(t *testing.T) {
		entry := makeUpsertEntry(t, "jwt.ttl", 3)
		result := svc.HandleEntryUpserted(ctx, entry)
		require.Equalf(t, outbox.DispositionAck, result.Disposition,
			"accesscore configreceive must Ack entry-upserted; got disposition=%v error=%v",
			result.Disposition, result.Err)
	})

	// --- sub-test: delete Ack ---
	t.Run("delete_acks", func(t *testing.T) {
		entry := makeDeleteEntry(t, "jwt.ttl", 3)
		result := svc.HandleEntryDeleted(ctx, entry)
		require.Equalf(t, outbox.DispositionAck, result.Disposition,
			"accesscore configreceive must Ack entry-deleted; got disposition=%v error=%v",
			result.Disposition, result.Err)
	})

	// --- sub-test: invalid upsert payload Rejects ---
	t.Run("invalid_upsert_rejects", func(t *testing.T) {
		bad := outbox.Entry{
			ID:        "evt-bad-upsert",
			EventType: "event.config.entry-upserted.v1",
			Payload:   []byte(`{not-json`),
		}
		result := svc.HandleEntryUpserted(ctx, bad)
		require.Equalf(t, outbox.DispositionReject, result.Disposition,
			"invalid upsert payload must Reject; got disposition=%v", result.Disposition)
	})

	// --- sub-test: invalid delete payload Rejects ---
	t.Run("invalid_delete_rejects", func(t *testing.T) {
		bad := outbox.Entry{
			ID:        "evt-bad-delete",
			EventType: "event.config.entry-deleted.v1",
			Payload:   []byte(`{not-json`),
		}
		result := svc.HandleEntryDeleted(ctx, bad)
		require.Equalf(t, outbox.DispositionReject, result.Disposition,
			"invalid delete payload must Reject; got disposition=%v", result.Disposition)
	})
}
