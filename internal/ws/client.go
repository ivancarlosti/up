package ws

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// writeWait is the time allowed to write a message to the client.
	writeWait = 10 * time.Second
	// pongWait is how long the hub waits for a pong before dropping a client.
	pongWait = 60 * time.Second
	// pingPeriod must be smaller than pongWait.
	pingPeriod = 25 * time.Second
	// sendBufferSize is the per client queue size.
	sendBufferSize = 64
)

// defaultTopics are the events delivered to a client that did not subscribe
// explicitly.
var defaultTopics = []string{"heartbeat", "monitor.", "notification.log", "cluster."}

// Client is a single WebSocket connection.
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	mu     sync.RWMutex
	topics []string
	closed bool
}

// Handle upgrades an HTTP request into a WebSocket connection. The caller is
// responsible for authenticating the request first.
func (h *Hub) Handle(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("websocket upgrade failed", "error", err)
		return
	}
	client := &Client{hub: h, conn: conn, send: make(chan []byte, sendBufferSize), topics: defaultTopics}
	h.register <- client

	h.log.Debug("websocket connection upgraded", "remote", r.RemoteAddr)
	go client.writeLoop()
	go client.readLoop()
}

// wants tells whether the client subscribed to an event type. Topic entries may
// end with "." to act as a prefix match (e.g. "monitor.").
func (c *Client) wants(eventType string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.topics) == 0 {
		return true
	}
	for _, topic := range c.topics {
		if topic == "*" {
			return true
		}
		if strings.HasSuffix(topic, ".") {
			if strings.HasPrefix(eventType, topic) {
				return true
			}
			continue
		}
		if topic == eventType {
			return true
		}
	}
	return false
}

// enqueue queues a message, dropping it when the client cannot keep up.
func (c *Client) enqueue(payload []byte) {
	select {
	case c.send <- payload:
	default:
		c.hub.log.Warn("dropping a websocket message: the client is too slow")
	}
}

// readLoop reads client commands (subscribe / ping) and detects disconnects.
func (c *Client) readLoop() {
	defer func() {
		c.hub.unregister <- c
	}()
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		message := serverMessage{}
		if err := json.Unmarshal(raw, &message); err != nil {
			continue
		}
		switch message.Action {
		case "subscribe":
			c.mu.Lock()
			c.topics = message.Topics
			c.mu.Unlock()
		case "ping":
			c.enqueue([]byte(`{"type":"pong"}`))
		}
	}
}

// writeLoop flushes the queued messages and keeps the connection alive.
func (c *Client) writeLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case payload, ok := <-c.send:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// close releases the client resources.
func (c *Client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
}
