// Package crypto defines the kernel-level cryptographic interfaces:
// KeyProvider, KeyHandle, ValueTransformer, CurrentKeyIDProvider, and the
// AADForConfig helper.
//
// Implementations (LocalAES, VaultTransit) live in runtime/crypto/ and are
// exposed via type aliases so that callers importing kernel/crypto receive the
// same concrete types without creating a dependency on runtime/.
//
// This package may only depend on the standard library, preserving the
// kernel→no-runtime layering rule.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/transformer.go
// ref: PR#200 R1a kernel/lifecycle layering pattern
package crypto
