// Package websocket provides a Hub-based WebSocket connection manager with
// signal-first broadcasting, ping/pong health checks, and graceful shutdown.
//
// This package is protocol-agnostic: it operates on the [Conn] interface.
// Use adapters/websocket for the nhooyr.io/websocket binding.
package websocket
