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

// KeyProvider is a type alias for the kernel KeyProvider interface. The
// authoritative definition lives in kernel/crypto.
//
// This is not a migration shim — the alias exists so runtime/crypto
// implementations (LocalAES, VaultTransit, keyProviderTransformer)
// type-check against the kernel contract without importing kernel/crypto
// from every local impl file.
//
// Guidance for new consumers: code in cells/ or cmd/ referencing only
// interfaces SHOULD import kernel/crypto directly and reference
// kcrypto.KeyProvider (the kernel contract).
type KeyProvider = kcrypto.KeyProvider

// KeyHandle is a type alias for the kernel KeyHandle interface. The
// authoritative definition lives in kernel/crypto.
//
// See KeyProvider alias comment for guidance on import choices.
type KeyHandle = kcrypto.KeyHandle

// EncryptResult is a type alias for the kernel encryption result contract.
// The authoritative definition lives in kernel/crypto.
type EncryptResult = kcrypto.EncryptResult
