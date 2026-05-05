// Package websocket provides a Hub-based WebSocket connection manager with
// signal-first broadcasting, ping/pong health checks, and graceful shutdown.
//
// Hub lifecycle: NewHub → Start (blocks) → Stop (terminal, single-use).
//
// This package is protocol-agnostic: it operates on the [Conn] interface.
// Use adapters/websocket for the github.com/coder/websocket binding.
//
// Example wiring at composition root:
//
//	hub := websocket.NewHub(websocket.DefaultHubConfig(clock.Real()), msgHandler)
//	go func() { _ = hub.Start(ctx) }()
//	bootstrap.WithManagedResource(hub)  // Close on phase10 LIFO shutdown
package websocket
