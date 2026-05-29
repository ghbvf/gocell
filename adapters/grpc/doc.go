// Package grpc provides the GoCell gRPC server adapter.
//
// # Overview
//
// The adapter wraps a *grpc.Server and exposes the lifecycle.ManagedResource
// interface so bootstrap.WithManagedResource can manage its lifecycle
// (health probes, background worker, LIFO teardown).
//
// # Transport security
//
// Three modes are supported:
//
//   - mTLS (production path): supply CertPEM + KeyPEM + ClientCAPEM. The
//     server requires and verifies client certificates via TLS 1.3
//     (tlsutil.NewServerMTLSConfig + tlsutil.NewClientCAPool).
//   - Server-side TLS (single direction): supply CertPEM + KeyPEM only.
//     Clients authenticate the server; the server does not verify clients.
//   - Plaintext (dev-only): set Config.TLS.AllowInsecure = true. Omitting
//     both AllowInsecure and PEM material is rejected fail-closed.
//
// # Interceptors
//
// This package registers zero interceptors. Recovery, metrics, tracing,
// and auth interceptors are composed at the bootstrap layer (PR-4) and
// plumbed in via grpc.ServerOption; they are not a concern of this adapter.
//
// # Readiness probe
//
// ProbeReady ("grpc_ready") is healthy while grpcServer.Serve is active.
// Register it via lifecycle.ManagedResource.Probes() drain in bootstrap.
//
// # Scope boundary
//
// This adapter provides server-side lifecycle only. Client-side gRPC adapters,
// ServiceRegistrar wiring (PR-7), and codegen/proto plumbing (PR-2/6) are
// delivered in subsequent PRs.
package grpc
