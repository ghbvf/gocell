package metadata

// NormalizeRuntimeContract canonicalizes a standalone, runtime-submitted contract
// (303-US3 #2234) to the parser-equivalent field state that governance
// declaration rules expect. A runtime candidate is a bare ContractMeta that never
// went through parseContract + the project-wide derive passes, so the fields those
// passes populate are absent — and a gate that validates the raw candidate would
// both miss declaration checks and mis-fire derived-field checks. This helper
// applies the per-contract subset of that normalization so the runtime
// registration gate (and the US4 submit decoder) validate the SAME canonical shape
// as in-tree `gocell validate`, instead of a hand-canonicalized one-off:
//
//   - Transports: default per-kind when unset, reusing defaultTransportsForKind —
//     the SAME single source parseContract uses (so FMT-39 transport↔kind validates
//     identically for runtime and in-tree contracts).
//   - event Subscribers: derived from ActorSubscribers (the only subscriber source
//     a standalone contract has — there are no sibling slices to merge), mirroring
//     deriveEventSubscribers' actor-only path. Without this an actorSubscribers-only
//     runtime event is falsely rejected as having no consumer.
//
// It mutates c in place (the canonical form of the passed candidate); callers that
// must not mutate their input (e.g. a dry-run gate Check) normalize a copy. It is a
// no-op for nil c.
//
// Scope boundary: webhook Receivers/Dispatchers are also yaml:"-" derived fields,
// but unlike event subscribers they have NO user-authored source on ContractMeta
// (in-tree they come solely from slice webhook contractUsages). How a runtime
// webhook declares its receivers is a webhook-runtime path question owned by the
// US4 submit decoder, not this per-contract normalize — so it is deliberately not
// synthesized here.
func NormalizeRuntimeContract(c *ContractMeta) {
	if c == nil {
		return
	}
	if c.Transports == nil {
		c.Transports = defaultTransportsForKind(c.Kind)
	}
	if c.Kind == "event" {
		c.Endpoints.Subscribers = dedupSorted(append([]string(nil), c.Endpoints.ActorSubscribers...))
	}
}
