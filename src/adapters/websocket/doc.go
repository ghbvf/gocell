// Package websocket provides a WebSocket adapter for the GoCell framework.
//
// It implements the real-time push interfaces defined in runtime/,
// providing WebSocket connection management, message broadcasting,
// subscription-based routing, and automatic reconnection.
//
// # Configuration
//
//	ReadBufferSize:  1024
//	WriteBufferSize: 1024
//	PingInterval:    30s
//	PongTimeout:     60s
//	MaxMessageSize:  512KB
//
// # Broadcasting
//
// The adapter supports topic-based broadcasting where clients subscribe to
// channels (e.g. config changes, audit events) and receive real-time updates.
//
// # Close
//
// Always call Close to gracefully disconnect all clients and release resources.
package websocket
