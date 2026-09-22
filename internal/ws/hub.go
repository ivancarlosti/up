// Package ws implements the WebSocket hub used by the dashboard to receive
// live updates (heartbeats, status changes, cluster events).
//
// The hub only knows the standard library plus gorilla/websocket, so it can be
// injected into the service layer through the services.EventPublisher
// interface without creating an import cycle.
package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Event is the envelope sent to every connected client.
type Event struct {
	Type    string    `json:"type"`
	Payload any       `json:"payload,omitempty"`
	At      time.Time `json:"at"`
}

// serverMessage is what a client sends to the hub (subscribe/unsubscribe/ping).
type serverMessage struct {
	Action string   `json:"action"`
	Topics []string `json:"topics"`
}

// Hub keeps the connected clients and fans out the events.
type Hub struct {
	log *slog.Logger

	mu      sync.RWMutex
	clients map[*Client]struct{}

	broadcast  chan Event
	register   chan *Client
	unregister chan *Client

	upgrader websocket.Upgrader
}

// NewHub builds the hub.
func NewHub(log *slog.Logger, allowedOrigins []string) *Hub {
	return &Hub{
		log:        log,
		clients:    map[*Client]struct{}{},
		broadcast:  make(chan Event, 256),
		register:   make(chan *Client, 16),
		unregister: make(chan *Client, 16),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// The dashboard is served from the same origin; when a reverse
			// proxy is used APP_URL is the canonical origin.
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" || len(allowedOrigins) == 0 {
					return true
				}
				for _, allowed := range allowedOrigins {
					if allowed == origin {
						return true
					}
				}
				log.Warn("websocket connection rejected", "origin", origin)
				return false
			},
		},
	}
}

// Publish implements services.EventPublisher: it queues an event for every
// connected client. It never blocks the caller.
func (h *Hub) Publish(event string, payload any) {
	select {
	case h.broadcast <- Event{Type: event, Payload: payload, At: time.Now().UTC()}:
	default:
		h.log.Warn("dropping a realtime event: the broadcast buffer is full", "type", event)
	}
}

// Run processes the registration and broadcast channels until ctx is done.
func (h *Hub) Run(ctx <-chan struct{}) {
	for {
		select {
		case <-ctx:
			h.mu.Lock()
			for client := range h.clients {
				client.close()
			}
			h.clients = map[*Client]struct{}{}
			h.mu.Unlock()
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = struct{}{}
			h.mu.Unlock()
			h.log.Debug("websocket client connected", "total", h.ClientCount())
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.close()
			}
			h.mu.Unlock()
			h.log.Debug("websocket client disconnected", "total", h.ClientCount())
		case event := <-h.broadcast:
			encoded, err := json.Marshal(event)
			if err != nil {
				h.log.Error("could not encode the realtime event", "error", err)
				continue
			}
			h.mu.RLock()
			for client := range h.clients {
				if !client.wants(event.Type) {
					continue
				}
				client.enqueue(encoded)
			}
			h.mu.RUnlock()
		}
	}
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
