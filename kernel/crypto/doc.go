// Package crypto defines the kernel-level cryptographic interfaces:
// KeyProvider, KeyHandle, ValueTransformer, CurrentKeyIDProvider, and the
// AADForConfig helper.
//
// Implementations (LocalAES, VaultTransit) live in runtime/crypto/ or
// adapters/*/. runtime/crypto re-exports these interfaces via type aliases
// so that existing callers using "runtime/crypto" continue to compile
// without change; kernel/crypto itself has no dependency on runtime or
// adapters.
//
// kernel/crypto/ must not import runtime/ or adapters/ — it is the
// interface-only package for the adapters→kernel-only dependency rule.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/transformer.go
// ref: PR#200 R1a kernel/lifecycle layering pattern
package crypto
