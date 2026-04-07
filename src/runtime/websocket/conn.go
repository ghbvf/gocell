package websocket

import "context"

// Conn abstracts a WebSocket connection. Implementations live in
// adapters/ (e.g., adapters/websocket for nhooyr.io/websocket).
//
// Implementations must be safe for concurrent use:
//   - Read is called from a single goroutine per connection.
//   - Write, Ping, and Close may be called concurrently with Read.
//   - Close must cause any in-progress Read to return an error.
type Conn interface {
	// ID returns the unique connection identifier.
	ID() string
	// Ping checks liveness. Returns an error if the peer is unresponsive.
	Ping(ctx context.Context) error
	// Read blocks until a message arrives or the context is cancelled.
	Read(ctx context.Context) ([]byte, error)
	// Write sends a text message.
	Write(ctx context.Context, data []byte) error
	// Close closes the connection. The contract:
	//   - Idempotent: multiple calls return nil.
	//   - Does NOT require a graceful WebSocket close handshake.
	//   - Must cause any in-progress Read to return promptly.
	//   - Must not block longer than necessary (no long mutex waits).
	// If a graceful close is needed in the future, add CloseGracefully(ctx).
	Close() error
}
