// Package vault provides a HashiCorp Vault Transit adapter that implements the
// kernel/crypto.KeyProvider interface.
//
// # Layer
//
// adapters/ layer — depends only on kernel/ and pkg/. Must NOT import runtime/.
//
// # Envelope Encryption
//
// This package uses envelope encryption (对标 k8s KMS v2):
//
//  1. Vault Transit `/datakey/plaintext` returns a server-generated 32-byte DEK
//     (HSM-backed in HCP) plus its wrapped EDK in a single round-trip.
//  2. The DEK encrypts the plaintext via AES-GCM with the caller-supplied AAD.
//  3. The wrapped EDK ("vault:vN:...") is returned alongside the ciphertext for
//     storage; the keyID is parsed from the EDK prefix at encrypt time.
//  4. On decrypt the EDK is unwrapped by Vault `/transit/decrypt` and used to
//     decrypt locally. The decrypt endpoint accepts EDKs in the canonical
//     `vault:vN:...` format regardless of whether they were produced by
//     `/datakey/plaintext` (current) or `/transit/encrypt` (legacy), so storage
//     written before the encrypt-path switchover stays decryptable.
//
// This pattern eliminates server-side exposure of plaintext and ensures AAD
// binding is enforced at the local AES-GCM layer (not just as a Vault context
// hint), fixing the AAD binding bug present in the pre-R1c direct-encrypt path.
//
// # References
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go@master
// ref: hashicorp/vault api-docs/secret/transit POST /transit/datakey/plaintext/:name
package vault
