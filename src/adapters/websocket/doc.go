// Package websocket provides a WebSocket adapter for the GoCell framework.
//
// The adapter implements a signal-first push model: instead of streaming full
// payloads over the socket, the Hub broadcasts lightweight JSON signals such as
//
//	{"type":"refresh","resource":"config"}
//
// Clients react to these signals by fetching the latest state via the
// corresponding REST API, keeping the WebSocket channel lean and stateless.
//
// # Architecture
//
//   - Hub manages the set of active connections (register / unregister /
//     broadcast / unicast).
//   - UpgradeHandler returns an http.Handler that upgrades HTTP requests to
//     WebSocket connections and registers them with the Hub.
//   - Heartbeat: the server sends ping frames at a configurable interval
//     (default 30 s); if a pong is not received within the pong timeout
//     (default 10 s) the connection is closed.
//
// # Reference
//
// The Hub pattern is adapted from nhooyr.io/websocket internal/examples/chat,
// with additions for connection identity (connectionID + userID), unicast
// delivery, and configurable heartbeat.
//
// ref: nhooyr.io/websocket internal/examples/chat — Hub pattern adopted with
// connectionID + userID identity and unicast; heartbeat interval/pong timeout
// made configurable instead of relying solely on CloseRead.
package websocket
