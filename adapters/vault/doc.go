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
//  1. A fresh 32-byte DEK is generated locally per plaintext.
//  2. The DEK encrypts the plaintext via AES-GCM with the caller-supplied AAD.
//  3. Vault Transit wraps (encrypts) the DEK; the wrapped DEK (edk) is stored
//     alongside the ciphertext.
//  4. On decrypt the edk is unwrapped by Vault and used to decrypt locally.
//
// This pattern eliminates server-side exposure of plaintext and ensures AAD
// binding is enforced at the local AES-GCM layer (not just as a Vault context
// hint), fixing the AAD binding bug present in the pre-R1c direct-encrypt path.
//
// # References
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go@master
// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main
package vault
