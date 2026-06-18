// Package eventtransport resolves the topology-gated outbox event transport
// (outbox.Publisher + outbox.Subscriber) for a composition root: an in-process
// in-memory bus in demo topology, and a real message broker (RabbitMQ) in
// postgres topology.
//
// It is the publisher-side analog of kernel/outbox.ResolveEmitter: a single
// funnel that maps a [runtime/bootstrap.Topology] to the right transport, so the
// composition root never hand-picks an implementation. It lives in cellmodules/
// (the Composition Root helper layer, alongside cellsecrets) because resolving a
// broker requires importing adapters/rabbitmq — which runtime/composition, by
// its layering contract, must not do.
//
// # INVARIANT: COREBUNDLE-EVENTBUS-FUNNEL-01
//
// The in-memory eventbus (runtime/eventbus) is reachable ONLY through this
// resolver's demo branch. The production composition root cmd/corebundle must
// not import runtime/eventbus directly; the depguard rule
// "corebundle-no-direct-eventbus" (.golangci.yml) enforces that path-level
// import ban (ref: .claude/rules/gocell/ai-robust.md §"路径级 import ban →
// depguard"). A direct eventbus.New in the composition root would re-open the
// #1940 gap where postgres (durable) topology publishes already-persisted outbox
// entries to an in-process bus — lost across processes / restarts.
//
// In postgres topology [resolveBrokerSpec] fail-closes when no broker URL is
// configured (via [dedupBrokerURL]): an empty cell set, a missing per-cell
// GOCELL_<CELLID>_AMQP_URL, or distinct per-cell URLs are startup errors, never a
// silent degrade back to in-memory.
//
// # Composition-root usage
//
// A composition root collects each broker cell's per-cell URL (falling back to
// GOCELL_AMQP_URL), resolves the transport once, and threads its outputs into
// bootstrap:
//
//	cells := map[string]string{ // cellID → its broker URL (cmd/corebundle.LoadBrokerURL
//	    "configcore": brokerURLFor("CONFIGCORE"), //   reads GOCELL_<CELLID>_AMQP_URL,
//	    "accesscore": brokerURLFor("ACCESSCORE"), //   falling back to GOCELL_AMQP_URL)
//	}
//	transport, err := eventtransport.Resolve(clk, topo, eventtransport.Config{Cells: cells})
//	// ... handle err ...
//	opts := []bootstrap.Option{
//	    bootstrap.WithPublisher(transport.Publisher),
//	    bootstrap.WithSubscriber(transport.Subscriber),
//	    bootstrap.WithEventTransportKind(transport.Kind), // sealed broker-kind for the phase0 split gate (#2211)
//	}
//	for _, mr := range transport.Resources { // broker connection (postgres mode) for LIFO teardown
//	    opts = append(opts, bootstrap.WithManagedResource(mr))
//	}
//
// See cmd/corebundle and examples/ssobff for the two production wirings.
//
// # INVARIANT: EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01
//
// Resolve stamps each [Transport] with a sealed [runtime/bootstrap.EventTransportKind]
// (Transport.Kind): RealBrokerEventTransport() on the postgres → RabbitMQ branch,
// InMemoryEventTransport() on the demo branch. This package is the SINGLE
// sanctioned minter of the real-broker variant — the composition root threads
// Transport.Kind into bootstrap.WithEventTransportKind so the phase0
// broker-mandatory gate (validateSplitTopologyBroker) trusts a type-system fact
// instead of the older StorageBackend()=="postgres" proxy ("non-nil ≠ real
// broker" closed, #2211). bootstrap (framework module) must export the
// constructor for this package (root module) to call it, and a
// framework/.../internal/ package cannot bridge that cross-module import, so the
// caller restriction is a structural Medium ceiling enforced by archtest
// EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01 (a call-level AST scan, same family as the
// RowScopeAll minter and COMMAND-ASYNC-EMIT-CALLER-01). The production end-to-end
// guarantee remains Hard via COREBUNDLE-EVENTBUS-FUNNEL-01 above: an in-memory bus
// is import-unexpressible in the production roots, so a forged real-broker kind
// cannot be paired with one there.
//
// # Per-cell broker URL dedup (egress-only, #2152 PR-2)
//
// The broker URL is read per cell as GOCELL_<CELLID>_AMQP_URL (falling back to
// the assembly-wide GOCELL_AMQP_URL), mirroring the per-cell DB DSN seam
// (cellmodules/percellpg). [dedupBrokerURL] collapses the per-cell URLs:
//
//   - one distinct URL (colocated) → a single broker connection from that URL
//     (behavior-preserving — the previous single-GOCELL_AMQP_URL wiring is the
//     case where every cell falls back to the same value);
//   - distinct URLs → fail-closed.
//
// The distinct-URL fail-closed is the egress-only boundary. #2152 PR-1
// (bootstrap.WithRelay keyed-by-instance) fanned out only the relay (publisher)
// side; the subscriber stays single (one phase6 event router). With a single
// subscriber, distinct per-cell brokers would orphan events — cell A publishes to
// broker A, but the lone subscriber consumes only the agreed broker. So this
// resolver refuses distinct broker URLs rather than silently severing the
// publish/subscribe chain. Lifting it (true N-broker fan-out) requires:
//
//   - ingress fan-out: subscriber single → N + a phase6 N-router (#2366);
//   - the relay/pool source: #2341 (per-cell PGProvider → N pools → N relays),
//     which lifts percellpg's symmetric >1-distinct-DSN fail-closed in lockstep.
//
// dedupBrokerURL is therefore the broker-side twin of percellpg.Resolve: a pure
// per-cell dedup that today admits only the colocated (one-distinct) case and
// hands the keyed relay seam (#2152 PR-1) its single agreed connection.
//
// ref: kernel/outbox.ResolveEmitter — the symmetric durability-gated funnel.
// ref: cellmodules/percellpg.Resolve — the per-cell DSN dedup twin.
// ref: github.com/ThreeDotsLabs/watermill message/router.go — disabledPublisher pattern.
//
// # INVARIANT: AMQP-URL-REDACTION-FUNNEL-01
//
// AMQP connection URLs carry embedded credentials in the DSN form
// amqp://user:pass@host/vhost. The adapter layer (adapters/rabbitmq) provides a
// sanitize funnel — sanitizeURL / sanitizeErrorURL / sanitizeDialError — that
// redacts user:pass before any URL string reaches a slog / fmt / errcode sink.
// This invariant is enforced by archtest AMQP-URL-REDACTION-FUNNEL-01 (Medium:
// typed field-selection AST scan with go/types identity for rabbitmq.Config.URL
// and brokerSpec.url). Blind spot: local-variable laundering and map-value reads
// (cells[id]) are not field selections and fall outside the scan; these paths are
// covered by TestDedupBrokerURL_CredentialNonLeak (behavior test). Second blind
// spot: resolveRabbitMQ wraps the rabbitmq.NewConnection error with
// fmt.Errorf("...: %w", err); that err is not a .URL/.url selection, so the scan
// does not inspect it. Non-leak there rests on an adapter precondition — the dial
// path pre-sanitizes via sanitizeDialError before the error escapes NewConnection
// (a Soft caller-side contract in adapters/rabbitmq, not a Medium guard here);
// tightening it would require NewConnection to return an already-redacted sealed
// error type.
//
// # Per-cell AMQP credential/vhost isolation (#2152 PR-3)
//
// Each cell's AMQP URL (GOCELL_<CELLID>_AMQP_URL, falling back to
// GOCELL_AMQP_URL) is the SEAM for per-cell credential/vhost isolation: the URL
// carries per-cell credentials and a vhost. The TARGET model (split topology) is
// for the operator to provision a distinct user + vhost per cell so each process
// holds only the credentials it needs and cannot publish to or consume from a
// broker it has no access to.
//
// This is NOT a runtime reality today. Distinct per-cell URLs are fail-closed
// (see "current boundary" below and dedupBrokerURL), so the only runnable
// configuration is a shared/identical URL across cells — under which all cells
// share one AMQP credential. True per-cell runtime isolation is blocked-by
// #2366/#2341; until they land, the enforced controls are the distinct-URL
// fail-closed gate plus URL credential non-leak (below), NOT live per-cell
// credential separation.
//
// This credential/vhost isolation is operator-provisioned via the AMQP DSN
// itself. Framework does NOT derive per-cell keys (no HKDF / key derivation
// layer): AMQP broker users are external to the framework and are managed by the
// broker operator, unlike the #2153 HMAC keyring which has a framework-level
// master key to derive from.
//
// The current boundary is egress-only: distinct per-cell broker URLs are
// fail-closed (see dedupBrokerURL). Running per-cell isolation end-to-end
// requires:
//   - ingress fan-out (#2366, subscriber single → N + phase6 N-router);
//   - per-cell relay fan-out (#2341, per-cell PGProvider → N pools → N relays).
//
// Ops note: until #2366/#2341 land, an operator MUST configure an identical
// GOCELL_<CELLID>_AMQP_URL for every broker cell in an assembly (or rely on the
// shared GOCELL_AMQP_URL fallback). A distinct value fails closed at Resolve with
// distinct_url_count in the internal attrs — that is the expected egress-only
// boundary, not a misconfiguration to "fix" by other means.
//
// Credentials are never written to error strings or logs; the sanitize funnel in
// adapters/rabbitmq (sanitizeURL / sanitizeErrorURL / sanitizeDialError) is the
// canonical redaction path. Archtest AMQP-URL-REDACTION-FUNNEL-01 (above) guards
// non-leak.
//
// ref: adapters/rabbitmq/connection.go — sanitizeURL / sanitizeErrorURL.
// ref: ADR docs/architecture/202606131500-1940-adr-topology-gated-event-transport.md.
package eventtransport
