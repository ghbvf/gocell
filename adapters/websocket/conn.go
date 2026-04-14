package websocket

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
)

const defaultWriteTimeout = 10 * time.Second

// Compile-time check: Conn implements runtime/websocket.Conn.
var _ rtws.Conn = (*Conn)(nil)

// Conn wraps an nhooyr.io/websocket.Conn and implements runtime/websocket.Conn.
//
// Close is lock-free (nhooyr.CloseNow is internally synchronized) so it can
// interrupt an in-flight Write immediately by closing the underlying TCP
// connection. Write uses mu only to serialize concurrent writes.
type Conn struct {
	id   string
	conn *websocket.Conn

	closed atomic.Bool // set once by Close; checked by Write
	mu     sync.Mutex  // serializes Write calls only
}

// NewConn creates a Conn wrapping an nhooyr.io/websocket connection.
func NewConn(id string, conn *websocket.Conn) *Conn {
	return &Conn{id: id, conn: conn}
}

func (c *Conn) ID() string { return c.id }

func (c *Conn) Ping(ctx context.Context) error {
	return c.conn.Ping(ctx)
}

func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	_, data, err := c.conn.Read(ctx)
	return data, err
}

func (c *Conn) Write(ctx context.Context, data []byte) error {
	if c.closed.Load() {
		return errcode.New(ErrAdapterWSClosed, "websocket: connection is closed")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring mu (Close may have fired between the
	// fast-path check and the lock acquisition).
	if c.closed.Load() {
		return errcode.New(ErrAdapterWSClosed, "websocket: connection is closed")
	}

	writeCtx, cancel := context.WithTimeout(ctx, defaultWriteTimeout)
	defer cancel()

	if err := c.conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return errcode.Wrap(ErrAdapterWSWrite, "websocket: write failed", err)
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
