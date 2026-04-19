// Package crypto provides the KeyProvider abstraction and implementations for
// encrypting sensitive values at the repository boundary.
//
// Design:
//   - KeyProvider abstracts any KMS backend (LocalAES, VaultTransit, AWS-KMS, ...).
//   - KeyHandle represents a specific key version, provides Encrypt/Decrypt.
//   - ValueTransformer is a thin caller-facing wrapper over KeyProvider.
//
// Interfaces (KeyProvider, KeyHandle, ValueTransformer, CurrentKeyIDProvider)
// are defined in kernel/crypto and re-exported here via type aliases so that
// existing import paths continue to work without modification.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/transformer.go@master
// ref: hashicorp/vault vault/barrier_aes_gcm.go@main:L1199-L1233
// ref: PR#200 R1a kernel/lifecycle type-alias bridge pattern
package crypto

import kcrypto "github.com/ghbvf/gocell/kernel/crypto"

// KeyProvider is a type alias for kernel/crypto.KeyProvider.
// Abstracts a KMS backend; implementations must be safe for concurrent use.
type KeyProvider = kcrypto.KeyProvider

// KeyHandle is a type alias for kernel/crypto.KeyHandle.
// A thin handle for a specific key version providing Encrypt/Decrypt.
type KeyHandle = kcrypto.KeyHandle
