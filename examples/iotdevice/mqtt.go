// mqtt.go wires the optional MQTT publish demo channel for iotdevice. When
// GOCELL_IOTDEVICE_MQTT_BROKERS is set, the device-register event channel is
// swapped from the in-memory event bus to a real MQTT v5 broker (adapters/mqtt)
// so the demo can be observed end-to-end with `mosquitto_sub`. The HTTP/WS main
// path (register API, command polling) is unchanged. See examples/iotdevice/docs/mqtt.md.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ghbvf/gocell/adapters/mqtt"
	"github.com/ghbvf/gocell/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// Environment variables controlling the MQTT publish demo channel.
const (
	// envMQTTBrokers is a comma-separated list of broker URLs (e.g.
	// "tcp://127.0.0.1:1883"). Unset = the channel is disabled and iotdevice
	// keeps its in-memory event bus (today's default behavior, byte-for-byte).
	envMQTTBrokers = "GOCELL_IOTDEVICE_MQTT_BROKERS"
	// envMQTTTopicNS overrides the topic namespace prefix; defaults to "iotdevice".
	envMQTTTopicNS     = "GOCELL_IOTDEVICE_MQTT_TOPIC_NS"
	defaultMQTTTopicNS = "iotdevice"
	// envMQTTConnectDeadline overrides the bootstrap connect deadline (a Go
	// duration string, e.g. "10s"). Defaults to defaultMQTTConnectDeadline. This
	// bounds how long startup waits for the first broker connection before
	// failing fast — without it an unreachable broker would hang startup before
	// /readyz is listening (#1388).
	envMQTTConnectDeadline = "GOCELL_IOTDEVICE_MQTT_CONNECT_DEADLINE"
	// defaultMQTTConnectDeadline allows ~2-3 ConnectTimeout(10s)+backoff retries
	// before fail-fast, leaving headroom for transient network jitter without
	// hanging startup indefinitely.
	defaultMQTTConnectDeadline = 30 * time.Second
)

// mqttConnectDeadline resolves the bootstrap connect deadline from
// envMQTTConnectDeadline (a Go duration string, e.g. "10s"), falling back to
// defaultMQTTConnectDeadline when unset. A malformed or non-positive override
// is a hard configuration error (fail-fast, no silent default) so an operator
// typo surfaces at startup rather than silently reverting to the default.
func mqttConnectDeadline() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(envMQTTConnectDeadline))
	if raw == "" {
		return defaultMQTTConnectDeadline, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", envMQTTConnectDeadline, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s=%q: must be > 0", envMQTTConnectDeadline, raw)
	}
	return d, nil
}

// mqttTopicPublisher adapts an mqtt.Publisher to iotdevice's event-type naming.
// GoCell event types are dotted ("event.device-registered.v1"); MQTT topics are
// slash-separated and namespace-scoped. It maps the dotted event type to a
// namespaced slash topic before delegating, leaving the payload (the serialized
// outbox envelope) untouched.
//
// Single-backend transform decorator — ref: ThreeDotsLabs/watermill
// message/decorator.go messageTransformPublisherDecorator (embed Publisher +
// transform, delegate). No fan-out: the demo swaps the cell's direct publisher
// to MQTT rather than mirroring to a second sink (device-registered has no
// in-process subscriber, so there is no second sink to mirror).
type mqttTopicPublisher struct {
	inner outbox.Publisher
	ns    mqtt.TopicNamespace
}

var _ outbox.Publisher = (*mqttTopicPublisher)(nil)

// mqttEventTopic maps a dotted GoCell event type to a namespaced MQTT topic:
// dots become slashes and the namespace prefix is prepended, e.g.
// ("iotdevice", "event.device-registered.v1") -> "iotdevice/event/device-registered/v1".
// The result is always under ns, so mqtt.Publisher.Publish → ns.Mint accepts it
// (asserted by TestMQTTEventTopic).
func mqttEventTopic(ns mqtt.TopicNamespace, eventType string) string {
	return ns.String() + "/" + strings.ReplaceAll(eventType, ".", "/")
}

// Publish maps the event-type topic to the namespaced MQTT topic and delegates.
func (p *mqttTopicPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	return p.inner.Publish(ctx, mqttEventTopic(p.ns, topic), payload)
}

// Close forwards to the inner MQTT publisher (drains in-flight publishes). The
// underlying Connection is closed separately via bootstrap.WithManagedCloser.
func (p *mqttTopicPublisher) Close(ctx context.Context) error {
	return p.inner.Close(ctx)
}

// buildMQTTDirectPublisher constructs the MQTT publish channel from the
// environment. When GOCELL_IOTDEVICE_MQTT_BROKERS is unset it returns
// (nil, nil, false, nil) — the demo keeps its in-memory event bus unchanged.
//
// When set, it opens an MQTT connection (Open blocks until the broker is
// reachable — start the broker before running the demo) and returns the
// topic-mapping publisher plus the connection (for the mqtt_ready readiness
// probe and shutdown close). Any config/parse/connect error is returned
// (fail-fast); there is intentionally no silent noop fallback
// (go-standards.md "生产配置使用真实 adapter，非 noop 回退"). Only the unset
// env var disables the channel.
//
// ctx is the app lifecycle context — autopaho binds the ConnectionManager to it,
// so the connection lives for the app's lifetime and is torn down on shutdown
// (in addition to the explicit bootstrap.WithManagedCloser close).
//
// ref: ThreeDotsLabs/watermill message/decorator.go (transform decorator).
func buildMQTTDirectPublisher(
	ctx context.Context, clk clock.Clock, logger *slog.Logger,
) (outbox.Publisher, *mqtt.Connection, bool, error) {
	raw := strings.TrimSpace(os.Getenv(envMQTTBrokers))
	if raw == "" {
		// Channel disabled: the bool (ok=false) is the discriminant, mirroring
		// buildDevicePersistence's demo-mode nil pool signal.
		//nolint:nilnil // ok=false is the disabled-channel discriminant; see godoc
		return nil, nil, false, nil
	}
	brokers := splitBrokers(raw)
	if len(brokers) == 0 {
		return nil, nil, false, fmt.Errorf("%s is set but contains no broker URLs", envMQTTBrokers)
	}

	nsStr := strings.TrimSpace(os.Getenv(envMQTTTopicNS))
	if nsStr == "" {
		nsStr = defaultMQTTTopicNS
	}
	ns, err := mqtt.ParseTopicNamespace(nsStr)
	if err != nil {
		return nil, nil, false, fmt.Errorf("mqtt topic namespace %q: %w", nsStr, err)
	}

	// The clientID's cellID segment is the cell identity ("iotdevice"), NOT the
	// configurable topic namespace: the two have different validation rules
	// (clientID cellID forbids "/" and is ≤32 chars; a topic namespace allows
	// "/" and is ≤128), and clientID only needs to be globally unique (the uuid
	// suffix guarantees that). It is deliberately independent of nsStr.
	clientID, err := mqtt.ParseEphemeralClientID(defaultMQTTTopicNS, "publisher")
	if err != nil {
		return nil, nil, false, fmt.Errorf("mqtt client id: %w", err)
	}

	connectDeadline, err := mqttConnectDeadline()
	if err != nil {
		return nil, nil, false, err
	}

	cfg := mqtt.Config{
		ClientID:        clientID,
		Brokers:         brokers,
		ConnectTimeout:  10 * time.Second,
		ConnectDeadline: connectDeadline,
		KeepAlive:       30 * time.Second,
		PublishTimeout:  5 * time.Second,
		Backoff: mqtt.BackoffConfig{
			BaseDelay: 500 * time.Millisecond,
			MaxDelay:  30 * time.Second,
		},
	}

	conn, err := mqtt.Open(ctx, clk, cfg)
	if err != nil {
		return nil, nil, false, fmt.Errorf("mqtt connect: %w", err)
	}
	// Redact any userinfo (e.g. tcp://user:pass@host) before logging broker URLs.
	logger.Info("iotdevice: MQTT publish channel connected",
		slog.Any("brokers", redactedBrokers(brokers)), slog.String("topicNamespace", ns.String()))

	pub, err := mqtt.NewPublisher(clk, conn, ns)
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if cerr := conn.Close(closeCtx); cerr != nil {
			logger.Warn("iotdevice: mqtt connection close after publisher-init failure",
				slog.Any("error", cerr))
		}
		return nil, nil, false, fmt.Errorf("mqtt publisher: %w", err)
	}
	return &mqttTopicPublisher{inner: pub, ns: ns}, conn, true, nil
}

// mqttChannelWiring is the bootstrap lifecycle wiring derived from an enabled
// MQTT publish channel. It carries the two resources that MUST BOTH participate
// in graceful shutdown:
//
//   - Resource: the *mqtt.Connection (lifecycle.ManagedResource). Registered via
//     WithManagedResource — it wires the mqtt_ready readiness probe + LIFO
//     disconnect, the standard adapter path (postgres/rabbitmq/redis).
//   - Closer: the publisher (lifecycle.ContextCloser). Publisher.Close drains
//     in-flight publishes; Connection.Close only disconnects (no drain), so the
//     publisher closer is mandatory, not redundant — omitting it silently drops
//     in-flight publishes on shutdown (PR #1364 review F1).
//
// Ordering is correctness, not cosmetic: bootstrap appends WithManagedResource
// teardowns first (they run LAST under LIFO) and WithManagedCloser teardowns
// later (they run earlier), so the publisher drains BEFORE the connection
// disconnects. See runtime/bootstrap LIFO teardown.
type mqttChannelWiring struct {
	Resource kernellifecycle.ManagedResource
	Closer   kernellifecycle.ContextCloser
}

// mqttChannelWiringFor pairs the enabled channel's publisher and connection into
// the lifecycle wiring. outbox.Publisher already requires Close(ctx) error, so
// the publisher always satisfies lifecycle.ContextCloser by interface.
func mqttChannelWiringFor(pub outbox.Publisher, conn *mqtt.Connection) mqttChannelWiring {
	return mqttChannelWiring{Resource: conn, Closer: pub}
}

// bootstrapOptions renders the wiring into bootstrap options. It always yields
// exactly two — the managed resource (connection) and the managed closer
// (publisher drain). Dropping either regresses shutdown drain/teardown; the
// count is locked by TestMQTTChannelWiring_RegistersPublisherDrainAndConnection.
func (w mqttChannelWiring) bootstrapOptions() []bootstrap.Option {
	return []bootstrap.Option{
		bootstrap.WithManagedResource(w.Resource),
		bootstrap.WithManagedCloser(w.Closer),
	}
}

// redactedBrokers returns broker URLs with any userinfo password masked, safe
// for structured logging. Unparseable entries fall back to a placeholder rather
// than risk leaking embedded credentials. ref: net/url.URL.Redacted().
func redactedBrokers(brokers []string) []string {
	out := make([]string, 0, len(brokers))
	for _, b := range brokers {
		u, err := url.Parse(b)
		if err != nil {
			out = append(out, "<unparseable-broker-url>")
			continue
		}
		out = append(out, u.Redacted())
	}
	return out
}

// splitBrokers parses a comma-separated broker list, trimming whitespace and
// dropping empty entries.
func splitBrokers(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
