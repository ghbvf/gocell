package websocket

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth"
)

// Conn abstracts a WebSocket connection. Implementations live in
// adapters/ (e.g., adapters/websocket for github.com/coder/websocket).
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
	// Read blocks until a message arrives or the context is canceled.
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

	// RemoteAddr returns the remote network address as a "host:port" string.
	// Used by Hub for diagnostic logging (eviction reasons, shutdown drain).
	// Implementations capture the address at handshake time; once captured the
	// value is immutable.
	RemoteAddr() string

	// Principal returns the authenticated principal bound at handshake time.
	// It may return nil if the adapter did not bind a principal (e.g. test
	// fakes); the Hub treats nil as "no subject indexing, no expiry tracked"
	// and behaves as if the connection has Kind=PrincipalUnknown.
	//
	// The returned pointer is owned by the Conn implementation; consumers
	// must not mutate the Principal struct.
	Principal() *auth.Principal
}
