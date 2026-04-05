// Package websocket provides a WebSocket adapter for GoCell.
//
// This adapter implements real-time bidirectional communication between GoCell
// HTTP handlers and browser or native clients. It wraps the gorilla/websocket
// library and integrates with the Cell authentication middleware
// (runtime/auth) for connection-level access control.
//
// Configuration is done via WebSocketConfig, which can be populated from
// environment variables using ConfigFromEnv().
//
// # Usage
//
//	cfg := websocket.ConfigFromEnv()
//	upgrader := websocket.NewUpgrader(cfg)
//
//	// In an http.HandlerFunc:
//	conn, err := upgrader.Upgrade(w, r, nil)
//	if err != nil { ... }
//	defer conn.Close()
//
// # Environment Variables
//
// See docs/guides/adapter-config-reference.md for the full variable listing.
// Key variables: WS_READ_BUFFER_SIZE, WS_WRITE_BUFFER_SIZE,
// WS_HANDSHAKE_TIMEOUT, WS_ALLOWED_ORIGINS.
//
// # Error Codes
//
// All errors use the ERR_ADAPTER_WEBSOCKET_* code family from pkg/errcode.
package websocket
