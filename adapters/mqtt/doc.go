// Package mqtt is the GoCell MQTT v5 adapter. It is structurally parallel to
// adapters/rabbitmq (AMQP 0-9-1): both implement kernel/outbox.Publisher and
// kernel/outbox.Subscriber, both use kernel/idempotency.Claimer for deduplication,
// and both wire kernel/healthz typed readiness probes.
//
// # What ships in each PR
//
// PR-1 (this PR): connection infrastructure, sealed-struct identity funnels
// (ClientID, TopicNamespace), Config, metrics skeleton, pkg/redaction wiring,
// and the archtest funnels that lock the sealed-struct shapes.
//
// PR-2: Publisher (outbox.Publisher).
// PR-3: Subscriber (outbox.Subscriber) + ConsumerBase integration.
// PR-4: DLT routing ($dead/<topic>), outbox conformance suite.
// PR-5: examples/iotdevice MQTT demo path.
//
// Parent epic: gh issue #1138.
//
// # Sealed construction funnels
//
// Two types carry the "illegal state unrepresentable" guarantee:
//
// ClientID is a sealed struct with a single unexported field `value string`.
// The only way to obtain a non-zero ClientID is ParseClientID, which validates
// the "{cellID}-{role}-{uuid}" format before constructing. Callers who receive
// a ClientID are guaranteed it passed validation. Go's visibility rules make
// outside-package struct-literal construction a compile error.
//
//	// INVARIANT: MQTT-CLIENT-ID-NAMESPACE-01
//	// tools/archtest/mqtt_funnel_test.go locks the field shape (A1), the
//	// construction allowlist inside adapters/mqtt (A2), and the absence of
//	// type aliases (A3).
//
// TopicNamespace is a sealed struct with a single unexported field `value string`.
// The only way to obtain a non-zero TopicNamespace is ParseTopicNamespace, which
// validates the topic prefix format before constructing. Methods PublishOK and
// SubscribeOK gate publish/subscribe operations to the declared prefix.
//
//	// INVARIANT: MQTT-TOPIC-NAMESPACE-01
//	// tools/archtest/mqtt_funnel_test.go locks the field shape (A1), the
//	// construction allowlist inside adapters/mqtt (A2), and the absence of
//	// type aliases (A3). Downstream callsite enforcement (publish/subscribe
//	// args must route through PublishOK/SubscribeOK) is deferred to PR-2/3
//	// and tracked by gh issue #1225.
//
// # Readiness probe
//
//	// INVARIANT: PROBENAME-SEALED-FUNNEL-01
//	// ProbeReady healthz.ProbeName = "mqtt_ready" is declared in healthz.go
//	// and listed in the golden inventory in
//	// tools/archtest/probename_sealed_funnel_test.go.
//
// # AI-robust grading summary (per .claude/rules/gocell/ai-robust.md)
//
//   - ClientID upstream Hard (package-external): Go compile error.
//   - ClientID upstream Medium (package-internal): archtest A2 AST scan.
//   - TopicNamespace: same grading as ClientID.
//   - Downstream callsite funnel: deferred to PR-2/3 (#1225).
//
// # Reference
//
//   - autopaho ConnectionManager: eclipse/paho.golang/autopaho/auto.go
//   - reconnect wrap pattern: adapters/rabbitmq/connection.go
//   - backoff shared helper: adapters/adapterutil/backoff.go
//
// ref: eclipse/paho.golang autopaho/auto.go — ConnectionManager, ClientConfig
// ref: adapters/rabbitmq/connection.go — reconnect wrap pattern
// ref: ADR docs/architecture/202605281200-048-adr-mqtt-adapter.md
package mqtt
