package eventtransport

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
)

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
//
// Kind is the sealed bootstrap.EventTransportKind fact stating whether the
// resolved transport is a real cross-process broker (postgres → RabbitMQ) or an
// in-process bus (demo). This package is the SINGLE sanctioned minter of the
// real-broker variant (EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01); the composition
// root threads Kind into bootstrap.WithEventTransportKind so the phase0
// broker-mandatory gate trusts a type-system fact instead of a StorageBackend
// proxy (#2211). Kind is intentionally NOT promoted to composition.SharedDeps
// (unlike Publisher/Subscriber): it is a wiring-time fact consumed only by the
// bootstrap startup gate — cell modules and the shared-dep layer never inspect
// it — so it stays a composition-root-local value (cmdLocals / the ssobff infra
// struct), threaded directly into the bootstrap option.
type Transport struct {
	Publisher  outbox.Publisher
	Subscriber outbox.Subscriber
	Resources  []lifecycle.ManagedResource
	Kind       bootstrap.EventTransportKind
}

// Config carries the composition-root-read configuration for the transport. The
// caller (cmd/corebundle) reads the environment and passes the values in, so
// this package stays pure with respect to os.Getenv and is exhaustively
// unit-testable.
type Config struct {
	// Cells maps each broker-requiring cell ID to its resolved per-cell AMQP URL.
	// The composition root reads each cell's GOCELL_<CELLID>_AMQP_URL (falling back
	// to the assembly-wide GOCELL_AMQP_URL) and passes the resolved URLs here. It is
	// the broker-side twin of percellpg.Config.Cells (per-cell DSN): dedupBrokerURL
	// collapses an identical set to one connection (colocated, behavior-preserving)
	// and fail-closes on distinct URLs (egress-only — see dedupBrokerURL). Required
	// (non-empty, every URL non-empty) in postgres topology; ignored in demo.
	Cells map[string]string

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
// in-memory bus (Cells ignored); postgres topology dedups the per-cell broker
// URLs and selects RabbitMQ — a missing/empty/distinct set is a fail-closed
// startup error, never a silent in-memory fallback (#1940's core invariant).
func resolveBrokerSpec(topo bootstrap.Topology, cfg Config) (brokerSpec, error) {
	if topo.StorageBackend() != bootstrap.StorageBackendPostgres {
		return brokerSpec{kind: brokerInMemory}, nil
	}
	url, err := dedupBrokerURL(cfg.Cells)
	if err != nil {
		return brokerSpec{}, err
	}
	return brokerSpec{kind: brokerRabbitMQ, url: url}, nil
}

// dedupBrokerURL is the PURE per-cell broker-URL gate + dedup. It decides which
// single broker URL the assembly connection is opened from; it performs NO I/O.
// It is the broker-side twin of cellmodules/percellpg.Resolve (per-cell DSN dedup):
// both feed the keyed relay fan-out seam (#2152 PR-1, bootstrap.WithRelay) and
// must lift their ">1 distinct" fail-closed together once N consumers are wired
// (#2341 for the relay/pool source, plus the ingress phase6 N-router follow-up).
//
// Returns the alphabetically-first cell's URL when there is exactly one distinct
// URL (colocated). Fail-closed for:
//   - empty Cells (postgres topology with no broker cells),
//   - any empty per-cell URL (names the missing GOCELL_<CELLID>_AMQP_URL),
//   - >1 distinct URL. This is the egress-only boundary (#2152 PR-2): with only
//     the relay (publisher) fanned out and a single subscriber, distinct per-cell
//     brokers would orphan events (cell A publishes to broker A; the lone
//     subscriber consumes only the agreed broker). Colocated assemblies must set
//     an identical GOCELL_<CELLID>_AMQP_URL for every broker cell.
func dedupBrokerURL(cells map[string]string) (string, error) {
	// Sort cell IDs for deterministic error messages + a stable agreed-URL pick.
	cellIDs := make([]string, 0, len(cells))
	for id := range cells {
		cellIDs = append(cellIDs, id)
	}
	sort.Strings(cellIDs)

	// Empty-cell-set gate: postgres topology with no broker cells is a fail-closed
	// misconfiguration, not a silent no-op (also guards the cellIDs[0] index below).
	if len(cellIDs) == 0 {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"eventtransport: postgres topology requires at least one broker cell "+
				"with a broker URL; refusing to start with an empty cell set")
	}

	// Dedup URLs; fail-closed on empty URL.
	seen := make(map[string]struct{}, len(cellIDs))
	for _, id := range cellIDs {
		url := strings.TrimSpace(cells[id])
		if url == "" {
			envVar := "GOCELL_" + strings.ToUpper(id) + "_AMQP_URL"
			return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"eventtransport: postgres topology requires a per-cell broker URL for "+
					"every broker cell; set GOCELL_<CELLID>_AMQP_URL (no silent in-memory "+
					"fallback — the relay must publish durable outbox entries to a broker, "+
					"not an in-process bus)",
				errcode.WithInternal(errcode.InternalAttr("cell_id", id)),
				errcode.WithInternal(errcode.InternalAttr("env_var", envVar)),
			)
		}
		seen[url] = struct{}{}
	}

	if len(seen) > 1 {
		return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"eventtransport: per-cell distinct broker URLs require split-topology ingress "+
				"fan-out (phase6 N-router, #2366) plus per-cell relay fan-out (#2341), not yet "+
				"wired (egress-only, #2152 PR-2); colocated assemblies must configure an "+
				"identical GOCELL_<CELLID>_AMQP_URL for every broker cell. "+
				"Per-cell credential/vhost isolation (distinct vhost/user per cell) is "+
				"the intended security model; this egress-only gate lifts together with "+
				"#2366/#2341",
			errcode.WithInternal(errcode.InternalAttr("distinct_url_count", len(seen))),
			errcode.WithInternal(errcode.InternalAttr("cell_ids", strings.Join(cellIDs, ","))),
		)
	}

	return strings.TrimSpace(cells[cellIDs[0]]), nil
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
	return dispatchTransport(clk, spec, cfg)
}

// dispatchTransport constructs the Transport for an already-resolved brokerSpec.
// Split out of Resolve so the fail-closed default arm is reachable by a white-box
// test (resolveBrokerSpec only ever emits the two valid kinds, so the default is
// otherwise unreachable through the public API).
func dispatchTransport(clk clock.Clock, spec brokerSpec, cfg Config) (Transport, error) {
	switch spec.kind {
	case brokerInMemory:
		eb := eventbus.New(clk)
		return Transport{Publisher: eb, Subscriber: eb, Kind: bootstrap.InMemoryEventTransport()}, nil
	case brokerRabbitMQ:
		return resolveRabbitMQ(clk, spec, cfg)
	default:
		// Fail-closed defensive guard: resolveBrokerSpec only ever emits
		// brokerInMemory or brokerRabbitMQ today, so this is unreachable. If a
		// future broker kind is added to resolveBrokerSpec but its dispatch arm is
		// forgotten here, refuse to start rather than silently fall back to a wrong
		// transport.
		return Transport{}, errcode.New(errcode.KindInternal, errcode.ErrInternal,
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
		// Sole sanctioned mint of the real-broker kind (EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01):
		// only reached on the postgres → RabbitMQ branch.
		Kind: bootstrap.RealBrokerEventTransport(),
	}, nil
}
