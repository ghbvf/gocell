// Package crypto provides config-core-specific crypto helpers.
//
// kernel/crypto defines pure crypto contracts (KeyProvider, ValueTransformer,
// etc). AAD formatting is config-core business logic and lives here.
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
	return []byte(fmt.Sprintf("cell:%s/key:%s", cellID, configKey))
}
