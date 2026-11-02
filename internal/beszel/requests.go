package beszel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/gorilla/websocket"
)

// readLoop is the only reader of the socket: it routes every answer to the
// request waiting for that id, and closes the connection when the socket dies.
func (c *Conn) readLoop() {
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(readDeadline))
		_, payload, err := c.ws.ReadMessage()
		if err != nil {
			if !errors.Is(err, websocket.ErrCloseSent) {
				c.log.Debug("agent connection read failed", "error", err)
			}
			c.Close("the agent stopped answering")
			return
		}
		c.lastMessage.Store(time.Now().UnixNano())

		var response AgentResponse
		if err := cbor.Unmarshal(payload, &response); err != nil {
			// A frame we cannot decode is not a reason to drop a working
			// connection: log it and keep reading.
			c.log.Warn("undecodable frame from the agent", "error", err, "bytes", len(payload))
			continue
		}
		if response.ID == nil {
			c.log.Debug("agent sent an answer without a request id")
			continue
		}
		id := *response.ID
		c.mu.Lock()
		waiter, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if !ok {
			c.log.Debug("agent answered an unknown request", "request_id", id)
			continue
		}
		// The buffer is one, so this never blocks the reader.
		waiter <- response
	}
}

// failPending unblocks everyone waiting on the connection.
func (c *Conn) failPending() {
	c.mu.Lock()
	pending := c.pending
	c.pending = map[uint32]chan AgentResponse{}
	c.mu.Unlock()
	for _, waiter := range pending {
		close(waiter)
	}
}

// forget drops the waiter of a request that will never be answered.
func (c *Conn) forget(id uint32) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// request sends one action and waits for the answer carrying its id.
func (c *Conn) request(ctx context.Context, action WebSocketAction, data any, timeout time.Duration) (AgentResponse, error) {
	select {
	case <-c.closed:
		return AgentResponse{}, ErrClosed
	default:
	}

	id := c.nextID.Add(1)
	waiter := make(chan AgentResponse, 1)

	c.mu.Lock()
	c.pending[id] = waiter
	c.mu.Unlock()

	payload, err := cbor.Marshal(HubRequest[any]{Action: action, Data: data, ID: &id})
	if err != nil {
		c.forget(id)
		return AgentResponse{}, fmt.Errorf("encoding request %d: %w", action, err)
	}
	c.writeMu.Lock()
	err = c.ws.WriteMessage(websocket.BinaryMessage, payload)
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		return AgentResponse{}, fmt.Errorf("sending request %d: %w", action, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response, ok := <-waiter:
		if !ok {
			return AgentResponse{}, ErrClosed
		}
		if response.Error != "" {
			return response, fmt.Errorf("the agent refused the request: %s", response.Error)
		}
		return response, nil
	case <-ctx.Done():
		c.forget(id)
		return AgentResponse{}, ctx.Err()
	case <-timer.C:
		c.forget(id)
		return AgentResponse{}, fmt.Errorf("the agent did not answer within %s", timeout)
	case <-c.closed:
		return AgentResponse{}, ErrClosed
	}
}
