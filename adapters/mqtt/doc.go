// Package mqtt is the GoCell MQTT v5 adapter. It is structurally parallel to
// adapters/rabbitmq (AMQP 0-9-1): both implement kernel/outbox.Publisher and
// kernel/outbox.Subscriber, both use kernel/idempotency.Claimer for deduplication,
// and both wire kernel/healthz typed readiness probes.
//
// # What ships in each PR
//
// PR-1 (merged): connection infrastructure, sealed-struct identity funnels
// (ClientID, TopicNamespace), Config, metrics skeleton, pkg/redaction wiring,
// and the archtest funnels that lock the sealed-struct shapes.
//
// PR-2 (merged): Publisher (outbox.Publisher), QoS-1 PUBACK error classification
// (classifyPubackReason), publisher metrics (mqtt_publish_failed_total closed-set
// reason labels), and the MQTT-PUBLISH-CALLSITE-FUNNEL-01 callsite archtest that
// locks all cm.Publish calls inside (*Connection).Publish.
//
// PR-3 (merged): Subscriber (outbox.Subscriber) + ConsumerBase integration,
// SUBSCRIBE/ACK callsite funnels.
// PR-4 (this PR): DLT routing ($dead/<topic> via TopicNamespace.MintDeadLetter),
// mqtt_dlx_total metric, Option C Requeue semantics (ADR-050 §6), outbox
// conformance suite, mTLS nightly.
// PR-5: examples/iotdevice MQTT demo path.
//
// Parent epic: gh issue #1138.
//
// # Sealed construction funnels
//
// Two types carry the "illegal state unrepresentable" guarantee:
//
// ClientID is a sealed struct with a single unexported field `value string`.
// The only way to obtain a non-zero ClientID is ParseEphemeralClientID or
// ParseStableClientID, which validate the "{cellID}-{role}-{uuid}" format before
// constructing. ParseEphemeralClientID generates the uuid internally (clean-session
// use); ParseStableClientID accepts a caller-supplied instanceID (persistent-session
// use). Callers who receive a ClientID are guaranteed it passed validation. Go's
// visibility rules make outside-package struct-literal construction a compile error.
//
//	// INVARIANT: MQTT-CLIENT-ID-NAMESPACE-01
//	// tools/archtest/mqtt_funnel_test.go locks the field shape (A1), the
//	// construction allowlist inside adapters/mqtt (A2), and the absence of
//	// type aliases (A3).
//
// TopicNamespace is a type alias for the sealed internal type
// adapters/mqtt/internal/topicns.Namespace (a struct with a single unexported
// field `value string`). The namespace, the publish/subscribe token types
// (PublishableTopic / SubscribableFilter) and their validated constructors
// (Mint / MintFilter / MintDeadLetter) all live in that internal package. Because
// the token fields are unexported there, a token carrying an arbitrary topic
// cannot be constructed in adapters/mqtt at all — it is a compile error, not just
// an archtest finding (#1247, sealed construction upstream Hard). The only way to
// obtain a non-zero namespace is ParseTopicNamespace (→ topicns.Parse), which
// validates the prefix; methods PublishOK and SubscribeOK gate publish/subscribe
// operations to the declared prefix.
//
//	// INVARIANT: MQTT-TOPIC-NAMESPACE-01
//	// tools/archtest/mqtt_funnel_test.go locks the field shape (A1, via the
//	// alias), the Namespace construction allowlist inside internal/topicns (A2),
//	// and the absence of further type aliases / reshapes (A3). Downstream
//	// callsite enforcement (all cm.Publish calls must route through
//	// (*Connection).Publish, which gates on PublishOK) is locked by
//	// MQTT-PUBLISH-CALLSITE-FUNNEL-01 (tools/archtest/mqtt_callsite_funnel_test.go).
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
//   - ClientID upstream Medium (package-internal): archtest A2 AST scan. ClientID
//     stays in adapters/mqtt, so its construction is in-package and the
//     package-internal axis is the permanent Go ceiling (same family as #851 / #893).
//   - TopicNamespace + tokens (PublishableTopic / SubscribableFilter): the seal
//     moved to adapters/mqtt/internal/topicns (#1247), so the construction axis is
//     upstream Hard for ALL of adapters/mqtt — a cross-package unexported field is
//     a compile error. The irreducible residual (Mint must co-locate with the type
//     it constructs) is a Medium archtest confined to the ~1-file internal package,
//     the maximal Hard reachable in Go.
//   - Downstream callsite funnels: MQTT-PUBLISH-CALLSITE-FUNNEL-01 (gh #1225
//     closed), MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01 + MQTT-ACK-CALLSITE-FUNNEL-01.
//     The PUBLISH funnel's A2 construction allowlist is {Mint, MintDeadLetter} for
//     the $dead sink. The authoritative sub-rule inventory + Hard/Medium grading
//     lives in the package godoc of tools/archtest/mqtt_callsite_funnel_test.go
//     (single source, not duplicated here).
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
