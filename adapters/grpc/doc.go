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
// This package registers zero interceptors. Recovery, metrics, tracing, and auth
// interceptors are composed at the composition-root layer via
// interceptor.NewServerInterceptors(deps) (the sole public funnel, which mints the
// shared registrar/drain, #1752) and plumbed in via Config.Interceptors; they are
// not a concern of this adapter.
//
// # Service registration (PR-7: Form B callback, cell-facing)
//
// Services are registered via the cell-facing drain model introduced in PR-7:
//  1. Cells declare GRPCServiceSpec in Cell.Init via reg.GRPCService(spec).
//  2. bootstrap drains RegistrySnapshot.GRPCServices in phase7b and calls
//     srv.Registrar().Register(spec) for each spec — AFTER binding the gRPC
//     listener but BEFORE grpcServeAll.
//  3. The Registrar intercepts the spec.Register callback's RegisterService
//     call to record /{service}/{method}→cellID attribution.
//
// Composition-root wiring (cmd/ or examples/). interceptor.NewServerInterceptors
// mints the ONE shared registrar + drain internally (#1752), builds both the unary
// and streaming chains from them, and the adapter binds the bundle's registrar +
// triggers its drain — so the adapter and chains cannot observe different
// registrar/drain instances and a streaming server cannot forget the stream chain
// (#1752/#1153). The composition root supplies neither registrar nor drain:
//
//	deps := interceptor.Deps{..., CellIDClosedSet: asm.CellIDs()}
//	srv, _ := adaptersgrpc.New(adaptersgrpc.Config{
//	    ..., Interceptors: interceptor.NewServerInterceptors(deps),
//	})
//	bootstrap.New(clk,
//	    bootstrap.WithGRPCListener(cell.PrimaryListener, srv, ":9000"),
//	)
//	// Services are registered automatically from cell declarations — no manual
//	// srv.Registrar().Register(...) needed in the composition root.
//
// # Readiness probe
//
// ProbeReady ("grpc_ready") is healthy while grpcServer.Serve is active.
// The serving flag is set after grpcServer.Serve starts its accept loop, so
// there is a sub-millisecond window between flag flip and the first Accept.
// At typical readiness-probe polling intervals (≥100 ms) this window is
// invisible.
//
// # Scope boundary
//
// This adapter provides server-side lifecycle only. Client-side gRPC adapters
// and codegen/proto plumbing (PR-2/6) are delivered in other PRs.
package grpc
