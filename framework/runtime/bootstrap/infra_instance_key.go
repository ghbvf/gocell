package bootstrap

// infra_instance_key.go — sealed identity for a deduplicated infrastructure
// instance, the keying dimension of bootstrap's runtime fan-out (#2152 PR-1).
//
// A colocated assembly has exactly ONE infra instance (the shared pool/broker),
// so every relay registers under DefaultInstanceKey and behavior is unchanged.
// A split assembly mints one key per distinct deduplicated instance (e.g. a
// per-cell DSN-derived id) so each instance's relay fans out independently.
//
// PR-1 keys the relay channel only (each relay already holds its own publisher,
// so the relay collection is independent of the publisher/subscriber channels).
// PR-2 (broker instance dedup) reuses THIS SAME key type to fan out the
// publisher/subscriber channels — the keying dimension is frozen here and not
// rewritten downstream.
//
// ref: runtime/transport.TransportMode — sealed value via an unexported field +
// constructor accessors; external packages cannot mint a non-default value.

import (
	"regexp"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
)

// infraInstanceIDPattern constrains a non-default instance id to a lowercase
// snake_case identifier (no leading digit, no consecutive/trailing underscore,
// no hyphen/uppercase). The constraint exists so the id composes directly into a
// valid healthz probe name ("<base>_<id>"): a fanned-out relay exposes
// per-instance health probes via healthz.RelayInstanceProbeName, which fails on a
// non-snake_case id. Keeping the contract at the key mint surfaces a bad id at
// the source rather than at relay registration.
var infraInstanceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

// infraInstanceIDMaxLen bounds the id so the longest composed relay probe name
// ("outbox_relay_cleanup_" = 21 + id) stays within healthz probeNameMaxLen (64).
const infraInstanceIDMaxLen = 32

// InfraInstanceKey identifies a single deduplicated infrastructure instance
// (one DB pool / broker connection that one or more colocated cells share).
//
// It is a sealed value type: the id field is unexported, so an external package
// cannot forge a key for a specific instance via a struct literal — the only
// way to obtain a non-default key is NewInfraInstanceKey. The zero value is the
// colocated default (DefaultInstanceKey), which IS openly constructable because
// the single colocated instance is not a guarded identity. The type is
// comparable, so it is used directly as a map key in the fan-out collection.
type InfraInstanceKey struct {
	// id is the stable identifier of the deduplicated instance. Empty id is the
	// colocated default sentinel (DefaultInstanceKey); a non-default key always
	// carries a validated non-empty snake_case id.
	id string
}

// NewInfraInstanceKey mints the key for a deduplicated infrastructure instance.
// Composition roots call it once per distinct instance with a stable, probe-safe
// id (e.g. a DSN-derived lowercase snake_case identifier). It panics through the
// panic-taxonomy funnel on an empty or malformed id: minting a key with an
// unusable instance id is a composition-time programmer error, not a runtime
// condition. Use DefaultInstanceKey for the single colocated instance.
func NewInfraInstanceKey(id string) InfraInstanceKey {
	if id == "" || len(id) > infraInstanceIDMaxLen || !infraInstanceIDPattern.MatchString(id) {
		panic(panicregister.Approved("bootstrap-infra-instance-key-invalid",
			errcode.Assertion("bootstrap: NewInfraInstanceKey id must be a non-empty lowercase snake_case identifier (<=32 chars)")))
	}
	return InfraInstanceKey{id: id}
}

// DefaultInstanceKey is the colocated single-instance sentinel: the zero value.
// Colocated composition roots register their one relay under this key, so the
// fan-out collection holds exactly one entry and behavior is identical to the
// pre-fan-out single-relay world. It never collides with a NewInfraInstanceKey
// value (which always carries a non-empty id).
func DefaultInstanceKey() InfraInstanceKey {
	return InfraInstanceKey{}
}
