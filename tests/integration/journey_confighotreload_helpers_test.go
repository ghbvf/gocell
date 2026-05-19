//go:build integration

// Package-internal helpers for J-confighotreload integration tests
// (config-publish + access-apply). Both criteria share common wiring
// utilities; this file keeps the per-criterion files focused on assertions.
//
// Scope: J-confighotreload only. Other journeys keep their inline setup
// per the existing journey_*_test.go convention.
//
// Coverage boundary (informational): the helpers in this file support tests
// that exercise the configsubscribe handler (HandleEntryUpserted /
// HandleEntryDeleted) and the resulting in-memory cache state
// (configsubscribe.Cache.GetVersion). The emitter is outbox.NewNoopEmitter()
// — outbox-to-broker publish atomicity is NOT verified here; that is owned
// by tests/integration/l2atomicity/ + adapters/rabbitmq integration suites.
// PG-backed store behavior is also NOT covered; configpublish/configwrite
// use in-memory defaults (WithInMemoryDefaults) for all helper-wired tests.
//
// Context lifetime (informational): ctx is context.Background() with no
// deadline. The in-memory configsubscribe.Service and in-memory configcore
// store are in-process and never block on I/O, so a deadline is unnecessary.
// If these helpers are ever upgraded to PG-backed testcontainers, callers
// must wrap ctx with a timeout before invoking handler or store methods.
//
// ref: tests/integration/journey_auditlogintrail_helpers_test.go:40-54
// (buildAuditcoreChain — same Coverage boundary + Context lifetime pattern).
package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// configEntryUpsertedPayload is the wire shape for event.config.entry-upserted.v1.
// Defined here to avoid importing cells/configcore/internal/events (internal-package
// barrier). The canonical schema lives in
// contracts/event/config/entry-upserted/v1/payload.schema.json.
// Per cell-patterns.md "跨 cell decode 重复属于预期成本".
type configEntryUpsertedPayload struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
	ActorID string `json:"actorId"`
}

// configEntryDeletedPayload is the wire shape for event.config.entry-deleted.v1.
type configEntryDeletedPayload struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
	ActorID string `json:"actorId"`
}

// makeUpsertEntry builds an outbox.Entry for event.config.entry-upserted.v1.
func makeUpsertEntry(t *testing.T, key string, version int) outbox.Entry {
	t.Helper()
	payload, err := json.Marshal(configEntryUpsertedPayload{
		Key:     key,
		Version: version,
		ActorID: "actor-j-confighotreload",
	})
	require.NoError(t, err, "marshal upserted payload")
	return outbox.Entry{
		ID:        "evt-j-confighotreload-upsert-" + key,
		EventType: "event.config.entry-upserted.v1",
		Topic:     "event.config.entry-upserted.v1",
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
}

// makeDeleteEntry builds an outbox.Entry for event.config.entry-deleted.v1.
func makeDeleteEntry(t *testing.T, key string, version int) outbox.Entry {
	t.Helper()
	payload, err := json.Marshal(configEntryDeletedPayload{
		Key:     key,
		Version: version,
		ActorID: "actor-j-confighotreload",
	})
	require.NoError(t, err, "marshal deleted payload")
	return outbox.Entry{
		ID:        "evt-j-confighotreload-delete-" + key,
		EventType: "event.config.entry-deleted.v1",
		Topic:     "event.config.entry-deleted.v1",
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
}

// assertConfighotreloadSubscription verifies that a non-nil handler is registered
// for the named topic in the RegistryRecorder snapshot. Fails the test if no
// matching subscription exists or its handler is nil.
func assertConfighotreloadSubscription(t *testing.T, subs []cell.SubscriptionRequest, topic string) {
	t.Helper()
	for _, sub := range subs {
		if sub.Spec.Topic == topic {
			require.NotNilf(t, sub.Handler,
				"subscription handler for %q must be non-nil", topic)
			return
		}
	}
	t.Fatalf("no subscription found for topic %q (configcore wiring drift)", topic)
}
