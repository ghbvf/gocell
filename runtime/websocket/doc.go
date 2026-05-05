// Package websocket provides a Hub-based WebSocket connection manager with
// signal-first broadcasting, ping/pong health checks, and graceful shutdown.
//
// Hub lifecycle: NewHub → Start (blocks) → Stop (terminal, single-use).
//
// This package is protocol-agnostic: it operates on the [Conn] interface.
// Use adapters/websocket for the github.com/coder/websocket binding.
//
// Example wiring at composition root (managed mode — recommended):
//
//	hub := websocket.NewHub(websocket.DefaultHubConfig(clock.Real()), msgHandler)
//	bootstrap.New(..., bootstrap.WithManagedResource(hub))
//	// bootstrap auto-starts the hub via Hub.Worker() (kernel/worker.Worker)
//	// and tears it down via Hub.Close() during phase10 LIFO shutdown.
//	// Do NOT also run `go hub.Start(ctx)` — duplicate Start returns
//	// ErrWSAlreadyStarted and breaks the bootstrap worker pipeline.
//
// Manual mode (legacy / unit tests outside bootstrap):
//
//	hub := websocket.NewHub(websocket.DefaultHubConfig(clock.Real()), msgHandler)
//	go func() { _ = hub.Start(ctx) }()
//	defer hub.Stop(ctx)
package websocket
