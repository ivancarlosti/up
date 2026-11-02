package services

import (
	"context"
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/models"
)

// agentRegistry keeps the live agent connections of this node.
//
// It is in memory on purpose: a connection is a socket, so it exists exactly as
// long as the process holding it. What is persisted is the lease (who owns the
// system), which is what a restarted process and the other nodes read.
type agentRegistry struct {
	mu       sync.Mutex
	sessions map[uint]*agentSession
}

// agentSession is one agent being polled.
type agentSession struct {
	systemID uint
	conn     *beszel.Conn
	cancel   context.CancelFunc
	done     chan struct{}
	// detailsAt is when the machine identity was last asked for, and smartAt
	// when the SMART report was. Both are per session: a new connection re-asks
	// them, because the agent may have been restarted with new hardware.
	detailsAt time.Time
	smartAt   time.Time
}

// newAgentRegistry builds an empty registry.
func newAgentRegistry() *agentRegistry {
	return &agentRegistry{sessions: map[uint]*agentSession{}}
}

// connected reports whether this node is talking to an agent of the system.
func (r *agentRegistry) connected(systemID uint) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sessions[systemID]
	return ok
}

// replace installs a session and returns the one it displaced.
//
// An agent that reconnects always arrives with a new socket, and the old one is
// dead (or about to be): keeping both would poll the machine twice. The caller
// closes the displaced session outside the lock.
func (r *agentRegistry) replace(session *agentSession) *agentSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.sessions[session.systemID]
	r.sessions[session.systemID] = session
	return previous
}

// remove drops a session if it is still the current one.
func (r *agentRegistry) remove(session *agentSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.sessions[session.systemID]; ok && current == session {
		delete(r.sessions, session.systemID)
	}
}

// take removes and returns every session (the shutdown path).
func (r *agentRegistry) take() []*agentSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	sessions := make([]*agentSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.sessions = map[uint]*agentSession{}
	return sessions
}

// ids lists the systems this node is connected to right now.
func (r *agentRegistry) ids() []uint {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]uint, 0, len(r.sessions))
	for id := range r.sessions {
		out = append(out, id)
	}
	return out
}

// Attach starts polling a system over a freshly authenticated connection.
//
// The caller has already authenticated the handshake (the token and the
// fingerprint), so this only takes the lease and runs the loop. A node that
// cannot take the lease closes the socket: the agent will reconnect, and it must
// not be polled by two nodes at once.
func (s *SystemService) Attach(ctx context.Context, system *models.System, conn *beszel.Conn) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &agentSession{
		systemID: system.ID,
		conn:     conn,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	if previous := s.agents.replace(session); previous != nil {
		previous.cancel()
		previous.conn.Close("a new connection for the same system arrived")
	}
	s.log.Info("agent connected",
		"system_id", system.ID, "system", system.Name, "agent_version", system.AgentVersion, "node_id", s.nodeID())
	s.publish("system.connected", map[string]any{"id": system.ID, "uuid": system.UUID})
	go s.runAgent(sessionCtx, session)
}

// Detach closes the connection of a system (disabled, deleted, token rotated).
func (s *SystemService) Detach(systemID uint, reason string) {
	s.agents.mu.Lock()
	session := s.agents.sessions[systemID]
	delete(s.agents.sessions, systemID)
	s.agents.mu.Unlock()
	if session == nil {
		return
	}
	session.cancel()
	session.conn.Close(reason)
	go s.ReleaseSystem(context.Background(), systemID)
}

// CloseAgents closes every connection of this node (the shutdown path).
//
// The leases are released as well, so another node picks the agents up on their
// next reconnect instead of waiting for the expiry to pass.
func (s *SystemService) CloseAgents(reason string) {
	for _, session := range s.agents.take() {
		session.cancel()
		session.conn.Close(reason)
		s.ReleaseSystem(context.Background(), session.systemID)
	}
}

// ConnectedSystems lists the systems this node is talking to right now.
func (s *SystemService) ConnectedSystems() []uint { return s.agents.ids() }

// session returns the live session of a system, or nil.
func (r *agentRegistry) session(systemID uint) *agentSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[systemID]
}
