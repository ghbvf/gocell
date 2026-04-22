// Package crypto defines the kernel-level cryptographic interfaces:
// KeyProvider, KeyHandle, ValueTransformer, and CurrentKeyIDProvider.
//
// This package is the authoritative contract layer. Implementations
// (LocalAES, VaultTransit) live in runtime/crypto/ or adapters/*/.
//
// runtime/crypto exposes interface type aliases to kernel/crypto so that
// runtime/crypto implementations (LocalAESKeyProvider, VaultTransitKeyProvider,
// keyProviderTransformer, NoopTransformer) type-check against the kernel
// contract. This is NOT a backwards-compatibility shim — kernel/crypto is the
// authoritative contract and external consumers should import it directly.
//
// Breaking changes from pre-kernel split: AADForConfig helper moved to
// cells/configcore/internal/crypto; consumers must update imports from
// kcrypto.AADForConfig to configcrypto.AADForConfig. AAD formatting is
// configcore business logic (cell:{cellID}/key:{configKey} uses cellID and
// configKey which are configcore domain concepts), not a generic crypto
// contract.
//
// kernel/crypto/ must not import runtime/, adapters/, or cells/ — it is the
// interface-only package for the adapters→kernel-only dependency rule.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/transformer.go
// ref: PR#200 R1a kernel/lifecycle layering pattern
package crypto
