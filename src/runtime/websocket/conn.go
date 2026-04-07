package websocket

import "context"

// Conn abstracts a WebSocket connection. Implementations live in
// adapters/ (e.g., adapters/websocket for nhooyr.io/websocket).
type Conn interface {
	// ID returns the unique connection identifier.
	ID() string
	// Ping checks liveness. Returns an error if the peer is unresponsive.
	Ping(ctx context.Context) error
	// Read blocks until a message arrives or the context is cancelled.
	Read(ctx context.Context) ([]byte, error)
	// Write sends a text message.
	Write(ctx context.Context, data []byte) error
	// Close performs a graceful close handshake.
	Close() error
}
