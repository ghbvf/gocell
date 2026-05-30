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
//   - Plaintext (dev or mesh-sidecar): set Config.TLS.AllowInsecure = true.
//     The bind address is not restricted to loopback — a service-mesh sidecar
//     terminating mTLS at the pod boundary and forwarding cleartext is a valid
//     production posture. Omitting both AllowInsecure and PEM material is
//     rejected fail-closed (the explicit opt-in is the only gate).
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
// The serving flag is set after grpcServer.Serve starts its accept loop, so
// there is a sub-millisecond window between flag flip and the first Accept.
// At typical readiness-probe polling intervals (≥100 ms) this window is
// invisible. Wire it via lifecycle.ManagedResource.Probes() drain in bootstrap:
//
//	srv, _ := grpc.New(cfg)
//	grpc_health_v1.RegisterHealthServer(srv.ServiceRegistrar(), healthSrv)
//	bootstrap.WithManagedResource(srv)
//
// # Scope boundary
//
// This adapter provides server-side lifecycle only. Client-side gRPC adapters,
// ServiceRegistrar wiring (PR-7), and codegen/proto plumbing (PR-2/6) are
// delivered in subsequent PRs.
package grpc
