package eventtransport

import (
	"fmt"

	"github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// storageBackendPostgres is the bootstrap.Topology.StorageBackend() value that
// selects the durable (multi-process) transport. Mirrors the same literal used
// by cmd/corebundle.durabilityModeForTopology — both gate on the postgres
// backend.
const storageBackendPostgres = "postgres"

// dlxExchange is the dead-letter exchange the RabbitMQ subscriber declares for
// every subscription. The adapter REQUIRES a non-empty DLXExchange at Setup time
// (rejected/poison messages — outbox.Reject after the retry budget — are routed
// here instead of being silently dropped by the broker; see eventbus.md §"DLX 与
// 幂等"). A single bundle-wide DLX keeps the broker topology simple; dead-lettered
// messages retain their original routing key (topic), so a DLX consumer can route
// by source topic. Stable name = an operations contract (renaming requires broker
// migration of in-flight dead letters), so it is a const, not env-tunable.
const dlxExchange = "gocell.events.dlx"

// Transport bundles the resolved publish/subscribe sinks and any infrastructure
// resources whose lifecycle the composition root must manage.
//
// In demo topology Publisher and Subscriber are the SAME in-memory bus instance
// (in-process pub/sub) and Resources is empty. In postgres topology they are a
// real broker's publisher/subscriber backed by one shared connection, and
// Resources holds that connection (a lifecycle.ManagedResource) so bootstrap can
// close it during shutdown.
type Transport struct {
	Publisher  outbox.Publisher
	Subscriber outbox.Subscriber
	Resources  []lifecycle.ManagedResource
}

// Config carries the composition-root-read configuration for the transport. The
// caller (cmd/corebundle) reads the environment and passes the values in, so
// this package stays pure with respect to os.Getenv and is exhaustively
// unit-testable.
type Config struct {
	// AMQPURL is the RabbitMQ connection URL. Required (non-empty) in postgres
	// topology; ignored in demo topology. cmd/corebundle convention:
	// GOCELL_AMQP_URL.
	AMQPURL string

	// connOpts are optional rabbitmq.ConnectionOption values. Production passes
	// none; the package's own tests inject rabbitmq.WithDialFunc(fakeDial) so the
	// postgres branch is unit-testable without a live broker. Unexported so the
	// adapter type never leaks into the public Config surface — only same-package
	// (white-box) tests can set it.
	connOpts []rabbitmq.ConnectionOption
}

// brokerKind is the internal transport-kind enum. It is the sanctioned extension
// point for additional brokers (#1940 decision 4): a new broker (e.g. MQTT) adds
// a const here, a branch in resolveBrokerSpec, and a case in Resolve — plus a
// selector env in cmd. No GOCELL_EVENT_BROKER env is exposed while there is a
// single real broker, so an operator cannot request an unwired one.
type brokerKind int

const (
	brokerInMemory brokerKind = iota + 1
	brokerRabbitMQ
)

// brokerSpec is the resolved transport selection: which broker, and (for a real
// broker) its connection URL.
type brokerSpec struct {
	kind brokerKind
	url  string
}

// resolveBrokerSpec is the pure topology gate. demo/memory topology selects the
// in-memory bus; postgres topology selects RabbitMQ and REQUIRES a non-empty
// broker URL — a missing URL is a fail-closed startup error, never a silent
// in-memory fallback (#1940's core invariant).
func resolveBrokerSpec(topo bootstrap.Topology, cfg Config) (brokerSpec, error) {
	if topo.StorageBackend() != storageBackendPostgres {
		return brokerSpec{kind: brokerInMemory}, nil
	}
	if cfg.AMQPURL == "" {
		return brokerSpec{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres topology requires a real event broker; set GOCELL_AMQP_URL "+
				"(no silent in-memory fallback — the relay must publish durable outbox "+
				"entries to a broker, not an in-process bus)")
	}
	return brokerSpec{kind: brokerRabbitMQ, url: cfg.AMQPURL}, nil
}

// Resolve selects the event transport for topo. clk is the mandatory positional
// clock (ADR clock-positional-injection-funnel), threaded into the in-memory bus
// or the broker connection/publisher/subscriber.
//
// Demo topology returns an in-memory bus (Publisher == Subscriber, no resources).
// Postgres topology constructs a RabbitMQ connection (which dials eagerly, so an
// unreachable broker fail-fasts here) plus its publisher and subscriber, and
// returns the connection as a managed resource.
func Resolve(clk clock.Clock, topo bootstrap.Topology, cfg Config) (Transport, error) {
	clock.MustHaveClock(clk, "eventtransport.Resolve")

	spec, err := resolveBrokerSpec(topo, cfg)
	if err != nil {
		return Transport{}, err
	}

	switch spec.kind {
	case brokerInMemory:
		eb := eventbus.New(clk)
		return Transport{Publisher: eb, Subscriber: eb}, nil
	case brokerRabbitMQ:
		return resolveRabbitMQ(clk, spec, cfg)
	default:
		// Fail-closed defensive guard: resolveBrokerSpec only ever emits
		// brokerInMemory or brokerRabbitMQ today, so this is unreachable. If a
		// future broker kind is added to resolveBrokerSpec but its dispatch arm is
		// forgotten here, refuse to start rather than silently fall back to a wrong
		// transport.
		return Transport{}, errcode.New(errcode.KindInternal, errcode.ErrValidationFailed,
			"eventtransport: unhandled broker kind (programmer error: a new brokerKind "+
				"was added to resolveBrokerSpec without a Resolve dispatch arm)")
	}
}

// resolveRabbitMQ builds the RabbitMQ transport from one shared connection. The
// connection dials in its constructor, so a missing/unreachable broker surfaces
// as a startup error here (fail-closed), and it is returned as the lone managed
// resource so bootstrap closes it last (LIFO) after the relay and consumers.
func resolveRabbitMQ(clk clock.Clock, spec brokerSpec, cfg Config) (Transport, error) {
	conn, err := rabbitmq.NewConnection(clk, rabbitmq.Config{URL: spec.url}, cfg.connOpts...)
	if err != nil {
		return Transport{}, fmt.Errorf("eventtransport: rabbitmq connection: %w", err)
	}
	return Transport{
		Publisher: rabbitmq.NewPublisher(clk, conn),
		// DLXExchange is mandatory: the adapter's Setup fail-fasts without it, and
		// outbox.Reject (poison messages past the retry budget) must dead-letter
		// rather than vanish. Other SubscriberConfig fields (PrefetchCount, drain
		// timeouts) keep their adapter defaults.
		Subscriber: rabbitmq.NewSubscriber(clk, conn, rabbitmq.SubscriberConfig{DLXExchange: dlxExchange}),
		Resources:  []lifecycle.ManagedResource{conn},
	}, nil
}
