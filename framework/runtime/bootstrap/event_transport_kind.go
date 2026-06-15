package bootstrap

// event_transport_kind.go — sealed broker-kind fact for the phase0
// broker-mandatory gate (#2211).
//
// ref: uber-go/fx app.go — sealed value constructed at wiring time, safe zero
// value (here: unset → fail-closed at the gate).

// EventTransportKind is the sealed fact "is the resolved event transport a real
// cross-process broker, or an in-process bus?" It upgrades the phase0
// broker-mandatory gate (validateSplitTopologyBroker) from the weaker
// StorageBackend()=="postgres" ∧ non-nil proxy to a type-system fact: a split
// deployment topology requires IsRealBroker()==true.
//
// Sealed construction (Hard): all fields are unexported, so a package-external
// struct literal cannot mint (let alone forge, e.g. EventTransportKind{realBroker:true})
// a value — the only entry points are the two constructors below. The zero
// value is "unset" → IsRealBroker() reports false, so a split topology whose
// composition root forgot to declare the kind is rejected (fail-closed), the
// same posture as a forgotten publisher/subscriber.
//
// Single sanctioned minter of the real-broker variant (Medium, Go/module
// ceiling): RealBrokerEventTransport is intended to be called ONLY by
// cellmodules/eventtransport.Resolve (the topology-gated transport funnel),
// which mints it in the same branch that constructs the RabbitMQ
// publisher/subscriber. bootstrap (framework module) must export the
// constructor for eventtransport (root module, cellmodules/) to call it, and a
// framework/.../internal/ package cannot bridge that cross-module import, so a
// struct-level Hard funnel is unreachable; the caller restriction is enforced
// by archtest EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01 (same family as the
// RowScopeAll minter and COMMAND-ASYNC-EMIT-CALLER-01). The production
// end-to-end guarantee is nonetheless Hard: the COREBUNDLE-EVENTBUS-FUNNEL-01
// depguard makes an in-memory bus import-unexpressible in the production
// composition roots, so a forged real-broker kind cannot be paired with an
// in-memory bus there.
//
// Field set frozen by TestEventTransportKindZeroExportedFields
// (EVENT-TRANSPORT-KIND-SEALED-FIELD-FROZEN-01).
type EventTransportKind struct {
	set        bool // false = unset (fail-closed): no kind declared
	realBroker bool // true = real cross-process broker; false = in-process bus
}

// InMemoryEventTransport mints the kind for an in-process eventbus (demo /
// memory topology). IsRealBroker()==false. This is the safe direction (a split
// topology carrying this kind is rejected by the broker gate), so it has no
// caller funnel.
func InMemoryEventTransport() EventTransportKind {
	return EventTransportKind{set: true, realBroker: false}
}

// RealBrokerEventTransport mints the kind for a real cross-process broker
// (postgres topology → RabbitMQ). IsRealBroker()==true. Sanctioned caller:
// cellmodules/eventtransport.Resolve only (EVENT-TRANSPORT-KIND-MINTER-FUNNEL-01).
func RealBrokerEventTransport() EventTransportKind {
	return EventTransportKind{set: true, realBroker: true}
}

// IsRealBroker reports whether the resolved transport is a real cross-process
// broker. The zero value (unset) reports false so the broker gate fails closed.
func (k EventTransportKind) IsRealBroker() bool {
	return k.set && k.realBroker
}

// String renders the kind for diagnostics (errcode internal attrs / logs).
func (k EventTransportKind) String() string {
	switch {
	case !k.set:
		return "unset"
	case k.realBroker:
		return "real-broker"
	default:
		return "in-memory"
	}
}
