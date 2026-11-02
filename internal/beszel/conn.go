package beszel

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Timings of the conversation with one agent.
//
// requestTimeout outlasts the agent's own 30 s give-up, so a request that fails
// because the agent is busy collecting is reported as a busy agent instead of a
// dead connection. readDeadline is longer than a whole failed cycle (poll plus
// request timeout) for the reason upstream documents: a shorter one expires in
// the middle of a collection and closes a healthy connection.
const (
	requestTimeout = 35 * time.Second
	readDeadline   = 100 * time.Second
)

// ErrClosed is returned by every call on a connection that is gone.
var ErrClosed = errors.New("the agent connection is closed")

// Conn is one agent connection, seen from the hub side.
//
// The agent is the client: it dials the hub, proves it holds the token we gave
// it and then answers requests. Requests may be pipelined, so every one carries
// an id and a single reader routes the answers back to the caller waiting for it.
type Conn struct {
	ws  *websocket.Conn
	log *slog.Logger

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[uint32]chan AgentResponse
	nextID  atomic.Uint32

	lastMessage atomic.Int64

	closed    chan struct{}
	closeOnce sync.Once
	// reason is why the connection went away, for the operator.
	reason atomic.Value
}

// NewConn wraps an upgraded WebSocket and starts its reader.
//
// ws is owned by this connection from now on: the reader is the only one allowed
// to read from it, and closing the Conn closes the socket.
func NewConn(ws *websocket.Conn, log *slog.Logger) *Conn {
	conn := &Conn{
		ws:      ws,
		log:     log,
		pending: map[uint32]chan AgentResponse{},
		closed:  make(chan struct{}),
	}
	conn.lastMessage.Store(time.Now().UnixNano())
	conn.reason.Store("")
	go conn.readLoop()
	return conn
}

// Done is closed when the connection is gone.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// CloseReason is the human readable reason the connection ended (empty while it
// is alive).
func (c *Conn) CloseReason() string {
	reason, _ := c.reason.Load().(string)
	return reason
}

// TimeSinceLastMessage reports how long the agent has been silent.
func (c *Conn) TimeSinceLastMessage() time.Duration {
	return time.Since(time.Unix(0, c.lastMessage.Load()))
}

// Close closes the connection once, with a reason other callers can read.
func (c *Conn) Close(reason string) {
	c.closeOnce.Do(func() {
		if reason != "" {
			c.reason.Store(reason)
		}
		_ = c.ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason),
			time.Now().Add(5*time.Second))
		_ = c.ws.Close()
		close(c.closed)
		c.failPending()
	})
}

// Ping keeps a quiet connection alive without waiting for an answer.
func (c *Conn) Ping() {
	if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
		c.Close("ping failed")
	}
}
