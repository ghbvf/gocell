// Package websocket provides a WebSocket adapter for the GoCell framework.
// It implements a Hub-based connection manager with signal-first broadcasting,
// ping/pong health checks, and origin validation.
//
// Uses nhooyr.io/websocket for the underlying WebSocket protocol handling.
//
// Key features:
//   - Hub manages active connections with unique IDs
//   - Signal-first mode: messages are broadcast to all connected clients
//   - Ping/pong for connection health monitoring
//   - Origin-based upgrade validation
//   - Graceful shutdown with connection draining
package websocket
