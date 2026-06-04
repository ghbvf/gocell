// Package crypto provides configcore-specific crypto helpers.
//
// kernel/crypto defines pure crypto contracts (KeyProvider, ValueTransformer,
// etc). AAD formatting is configcore business logic and lives here.
//
// ref: kubernetes/apiserver pkg/storage/value — AAD (etcd key path) computed
// by the storage layer, not the generic transformer contract.
package crypto

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/tenant"
)

// AADForConfig computes the Additional Authenticated Data for a config entry.
// Format: "cell:{cellID}/tenant:{tenant}/key:{configKey}"
//
// Using a composite key prevents a ciphertext encrypted for one config entry
// from being transplanted into a different entry (cross-row replay attack).
// The tenant segment additionally binds the ciphertext to its owning tenant:
// without it, a ciphertext from tenant A's row could be transplanted into
// tenant B's row under the same configKey and still decrypt (cross-tenant
// secret leak). AES-GCM authenticates the AAD, so any tenant/key/cell mismatch
// surfaces as a decryption (tag) failure — fail-closed.
func AADForConfig(cellID string, t tenant.TenantID, configKey string) []byte {
	return fmt.Appendf(nil, "cell:%s/tenant:%s/key:%s", cellID, string(t), configKey)
}

// AADForVersion computes the Additional Authenticated Data for a config version.
// Format: "cell:{cellID}/tenant:{tenant}/version:{configID}"
//
// Deliberately uses the "/version:" segment (not "/key:") so that a ciphertext
// encrypted as a config version cannot be replayed into a config entry AAD domain
// and vice versa — even if configID happened to equal a configKey string.
// configID is the UUID primary key of config_entries; using it (rather than the
// human-readable configKey) also prevents cross-field AAD collisions of the form
// configKey == "version:<someUUID>". The tenant segment binds the ciphertext to
// its owning tenant (see AADForConfig).
func AADForVersion(cellID string, t tenant.TenantID, configID string) []byte {
	return fmt.Appendf(nil, "cell:%s/tenant:%s/version:%s", cellID, string(t), configID)
}
