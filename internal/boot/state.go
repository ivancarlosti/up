// Package boot carries the boot progress of the process.
//
// The HTTP listener opens before the database is reachable. That is deliberate:
// the embedded SPA has to be served immediately (a closed port is what made a
// slow or broken database look like a dead application for as long as the
// connection retried), and something has to tell the browser - and the
// container health check - what the process is waiting for. State is that
// single source of truth: the boot sequence in cmd/server writes it, the
// bootstrap HTTP engine and GET /api/health read it.
package boot

import (
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
)

// Phase is the boot step the process is currently in. Every value has a
// sentence in the "boot" namespace of web/src/locales/*.json.
type Phase string

// Boot phases, in the order they run.
const (
	PhaseStarting     Phase = "starting"
	PhaseConnecting   Phase = "connecting"
	PhaseMigrating    Phase = "migrating"
	PhaseBackfilling  Phase = "backfilling"
	PhaseTemplateAuth Phase = "template_auth"
	PhaseRollups      Phase = "rollups"
	PhaseSeeding      Phase = "seeding"
	PhaseWiring       Phase = "wiring"
	PhaseScheduler    Phase = "scheduler"
	PhaseReady        Phase = "ready"
)

// Values of the "status" field. The HTTP layer answers 503 for everything but
// StatusOK, which is what the container health check reacts to.
const (
	// StatusOK means the database is wired and every service is serving.
	StatusOK = "ok"
	// StatusStarting means the boot sequence is still working and nothing has
	// failed yet.
	StatusStarting = "starting"
	// StatusDegraded means something failed: the connection was refused, the
	// credentials were rejected or a boot step could not complete.
	StatusDegraded = "degraded"
)

// State is the concurrency safe boot progress of the process.
//
// Every method tolerates a nil receiver: a caller that does not care about the
// progress (a test, a future command) can pass nil instead of building one.
type State struct {
	mu sync.RWMutex

	nodeID string

	// started is when the process began booting (the UI shows the elapsed time).
	started time.Time
	// phaseSince is when the current phase began.
	phaseSince time.Time

	phase Phase
	// code is the stable error code of the last failure ("" while all is well).
	code   string
	detail string

	// attempts counts the failed connection attempts of database.Connect.
	attempts  int
	nextRetry time.Duration

	ready bool
	// Maintenance names a background job that still has to finish before the
	// reported data is complete ("rollups" after the first boot of an upgrade).
	maintenance string
}

// Background jobs reported by the "maintenance" field of the boot snapshot.
const (
	// MaintenanceRollups is the full horizon rollup rebuild of the first boot
	// after an upgrade (see cmd/server/main.go, startDeferredRollups).
	MaintenanceRollups = "rollups"
)

// New creates the boot state of a node.
func New(nodeID string) *State {
	now := time.Now().UTC()
	return &State{
		nodeID:     nodeID,
		started:    now,
		phaseSince: now,
		phase:      PhaseStarting,
	}
}

// SetPhase records the step that is running now and clears the failure of the
// previous one: a step that failed and is being retried stops being degraded as
// soon as it makes progress again.
func (s *State) SetPhase(phase Phase) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
	s.phaseSince = time.Now().UTC()
	s.code = ""
	s.detail = ""
	s.nextRetry = 0
}

// Retry records a failed attempt of a step that is going to be retried
// (database.Connect). The phase is left alone so the UI keeps saying what the
// process is working on.
func (s *State) Retry(attempts int, code, detail string, next time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = attempts
	s.nextRetry = next
	s.code = code
	s.detail = detail
}

// Fail records a failure the process cannot recover from on its own. The
// process stays up (the SPA reports it instead of the browser showing a
// connection error), so the failure is what /api/health answers with.
func (s *State) Fail(code, detail string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
	s.detail = detail
	// The step gave up: the attempt counter of the connection belongs to the
	// phase that failed, not to the message that reports it.
	s.attempts = 0
	s.nextRetry = 0
}

// Ready marks the boot as finished: every service is wired and serving.
func (s *State) Ready() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = true
	s.phase = PhaseReady
	s.phaseSince = time.Now().UTC()
	s.code = ""
	s.detail = ""
	s.nextRetry = 0
	s.attempts = 0
}

// SetMaintenance names a background job that is still running after the boot.
func (s *State) SetMaintenance(job string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maintenance = job
}

// ClearMaintenance removes the background job marker.
func (s *State) ClearMaintenance() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maintenance = ""
}

// Snapshot is the JSON body shared by GET /api/health and GET /api/boot.
type Snapshot struct {
	NodeID string `json:"node_id"`
	// Status is "ok", "starting" or "degraded" (see the constants above).
	Status string `json:"status"`
	// Ready is true once the boot sequence finished and every route is served.
	Ready bool `json:"ready"`
	// Phase is the running boot step.
	Phase Phase `json:"phase"`
	// Database is "pending" (still booting), "ok", "unreachable",
	// "unauthorized", "missing" or "error". It is the historical field of
	// GET /api/health, kept under the same name.
	Database string `json:"database"`
	// Code is the stable error code of the last failure ("" while all is well).
	Code string `json:"code,omitempty"`
	// Detail is the developer oriented explanation of Code.
	Detail string `json:"detail,omitempty"`
	// Attempts counts the failed attempts of the step that is being retried.
	Attempts int `json:"attempts,omitempty"`
	// Since is when the process began booting.
	Since time.Time `json:"since"`
	// ElapsedMS is how long the process has been booting.
	ElapsedMS int64 `json:"elapsed_ms"`
	// PhaseMS is how long the current phase has been running.
	PhaseMS int64 `json:"phase_ms"`
	// NextRetryMS is the backoff that separates the next attempt.
	NextRetryMS int64 `json:"next_retry_ms,omitempty"`
	// Maintenance names a background job that is still running ("" when none).
	Maintenance string `json:"maintenance,omitempty"`
}

// Snapshot reads the current state.
func (s *State) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{Status: StatusOK, Database: "unknown", Phase: PhaseReady}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().UTC()
	status := StatusOK
	database := "ok"
	switch {
	case s.code != "":
		// A failure is what the operator needs to see, whether the process is
		// still booting (credentials rejected) or already serving (a background
		// job that could not finish).
		status = StatusDegraded
		// Only a failure of the connection itself names the state of the
		// database. A step that failed on top of a working connection (a
		// migration, the background rollup rebuild) is reported as a database
		// error while the process is still booting; once it serves, the
		// database answers and saying otherwise would be a lie.
		if connectivityCode(s.code) || !s.ready {
			database = DatabaseState(s.code)
		}
	case !s.ready:
		status = StatusStarting
		database = "pending"
	}

	return Snapshot{
		NodeID:      s.nodeID,
		Status:      status,
		Ready:       s.ready,
		Phase:       s.phase,
		Database:    database,
		Code:        s.code,
		Detail:      s.detail,
		Attempts:    s.attempts,
		Since:       s.started,
		ElapsedMS:   now.Sub(s.started).Milliseconds(),
		PhaseMS:     now.Sub(s.phaseSince).Milliseconds(),
		NextRetryMS: s.nextRetry.Milliseconds(),
		Maintenance: s.maintenance,
	}
}

// connectivityCode reports whether a failure was produced by the connection
// itself (the server is unreachable, the credentials were refused, the schema
// does not exist) rather than by a statement that ran on a working connection.
func connectivityCode(code string) bool {
	switch code {
	case i18n.CodeDatabaseUnreachable, i18n.CodeDatabaseCredentials, i18n.CodeDatabaseMissing:
		return true
	}
	return false
}

// DatabaseState names the historical "database" field after the failure code.
func DatabaseState(code string) string {
	switch code {
	case i18n.CodeDatabaseUnreachable:
		return "unreachable"
	case i18n.CodeDatabaseCredentials:
		return "unauthorized"
	case i18n.CodeDatabaseMissing:
		return "missing"
	}
	return "error"
}
