package websocket

import (
	"context"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/pkg/errcode"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
)

const defaultWriteTimeout = 10 * time.Second

// Compile-time check: Conn implements runtime/websocket.Conn.
var _ rtws.Conn = (*Conn)(nil)

// Conn wraps an nhooyr.io/websocket.Conn and implements runtime/websocket.Conn.
type Conn struct {
	id   string
	conn *websocket.Conn

	mu     sync.Mutex
	closed bool
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
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return errcode.New(ErrAdapterWSClosed, "websocket: connection is closed")
	}

	writeCtx, cancel := context.WithTimeout(ctx, defaultWriteTimeout)
	defer cancel()

	if err := c.conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		return errcode.Wrap(ErrAdapterWSWrite, "websocket: write failed", err)
	}
	return nil
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.CloseNow()
}
