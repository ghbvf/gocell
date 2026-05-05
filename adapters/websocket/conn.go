package websocket

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
)

const defaultWriteTimeout = 10 * time.Second

// Compile-time check: Conn implements runtime/websocket.Conn.
var _ rtws.Conn = (*Conn)(nil)

// Conn wraps a github.com/coder/websocket.Conn and implements runtime/websocket.Conn.
//
// Close is lock-free (coder/websocket.Conn.CloseNow is internally synchronized)
// so it can interrupt an in-flight Write immediately by closing the underlying
// TCP connection. Write uses mu only to serialize concurrent writes.
type Conn struct {
	id         string
	remoteAddr string
	principal  *auth.Principal
	conn       *websocket.Conn

	closed atomic.Bool // set once by Close; checked by Write
	mu     sync.Mutex  // serializes Write calls only
}

// newConn creates a Conn wrapping a github.com/coder/websocket connection.
// remoteAddr is captured from r.RemoteAddr at handshake time (already
// sanitized via logutil.SafeAddr by the caller). The id and principal are
// bound at construction; both are immutable for the lifetime of the Conn.
func newConn(id, remoteAddr string, principal *auth.Principal, conn *websocket.Conn) *Conn {
	return &Conn{id: id, remoteAddr: remoteAddr, principal: principal, conn: conn}
}

// Principal returns the authenticated principal bound at handshake time.
// May be nil only when the adapter is misconstructed; production wiring
// always supplies a non-nil principal (anonymous endpoints use
// auth.NewAnonymousAuthenticator which returns PrincipalAnonymous).
func (c *Conn) Principal() *auth.Principal {
	return c.principal
}

func (c *Conn) ID() string         { return c.id }
func (c *Conn) RemoteAddr() string { return c.remoteAddr }

func (c *Conn) Ping(ctx context.Context) error {
	return c.conn.Ping(ctx)
}

func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := c.conn.Read(ctx)
	return data, err
}

func (c *Conn) Write(ctx context.Context, data []byte) error {
	if c.closed.Load() {
		return errcode.New(errcode.KindInternal, ErrAdapterWSClosed, "websocket: connection is closed")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring mu (Close may have fired between the
	// fast-path check and the lock acquisition).
	if c.closed.Load() {
		return errcode.New(errcode.KindInternal, ErrAdapterWSClosed, "websocket: connection is closed")
	}

	writeCtx, cancel := context.WithTimeout(ctx, defaultWriteTimeout)
	defer cancel()

	if err := c.conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterWSWrite, "websocket: write failed", err)
	}
	return nil
}

// Close performs an immediate transport close (CloseNow). It does NOT acquire
// mu, so it never blocks behind an in-flight Write. The underlying TCP close
// causes any concurrent Write to fail immediately.
func (c *Conn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	return c.conn.CloseNow()
}
