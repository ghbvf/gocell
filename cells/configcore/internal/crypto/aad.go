// Package crypto provides configcore-specific crypto helpers.
//
// kernel/crypto defines pure crypto contracts (KeyProvider, ValueTransformer,
// etc). AAD formatting is configcore business logic and lives here.
//
// ref: kubernetes/apiserver pkg/storage/value — AAD (etcd key path) computed
// by the storage layer, not the generic transformer contract.
package crypto

import "fmt"

// AADForConfig computes the Additional Authenticated Data for a config entry.
// Format: "cell:{cellID}/key:{configKey}"
//
// Using a composite key prevents a ciphertext encrypted for one config entry
// from being transplanted into a different entry (cross-row replay attack).
func AADForConfig(cellID, configKey string) []byte {
	return fmt.Appendf(nil, "cell:%s/key:%s", cellID, configKey)
}

// AADForVersion computes the Additional Authenticated Data for a config version.
// Format: "cell:{cellID}/version:{configID}"
//
// Deliberately uses the "/version:" segment (not "/key:") so that a ciphertext
// encrypted as a config version cannot be replayed into a config entry AAD domain
// and vice versa — even if configID happened to equal a configKey string.
// configID is the UUID primary key of config_entries; using it (rather than the
// human-readable configKey) also prevents cross-field AAD collisions of the form
// configKey == "version:<someUUID>".
func AADForVersion(cellID, configID string) []byte {
	return fmt.Appendf(nil, "cell:%s/version:%s", cellID, configID)
}
