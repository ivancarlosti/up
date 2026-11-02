package services

import (
	"context"
	"errors"
	"time"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/models"
)

// runAgent is the life of one agent connection: take the lease, poll the machine
// once a minute, keep the lease alive, and give everything back when the socket
// or the ownership goes away.
func (s *SystemService) runAgent(ctx context.Context, session *agentSession) {
	defer close(session.done)
	defer s.agents.remove(session)
	defer func() {
		// A clean release is what makes the handover immediate: the agent is about
		// to reconnect somewhere, and the next node must be allowed to take it
		// without waiting for the lease to expire.
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.ReleaseSystem(releaseCtx, session.systemID); err != nil {
			s.log.Debug("could not release the system lease", "system_id", session.systemID, "error", err)
		}
		s.publish("system.disconnected", map[string]any{"id": session.systemID})
		s.log.Info("agent disconnected", "system_id", session.systemID, "reason", session.conn.CloseReason())
	}()

	owned, err := s.ClaimSystem(ctx, session.systemID)
	if err != nil {
		s.log.Warn("could not take the lease of a system", "system_id", session.systemID, "error", err)
		session.conn.Close("could not take the system lease")
		return
	}
	if !owned {
		// The agent reached a node that does not own it. Refusing is the correct
		// behaviour: the owner is talking to the same machine, and the agent will
		// reconnect (to the owner or elsewhere) on its own retry schedule.
		session.conn.Close("another node owns this system")
		return
	}

	renew := time.NewTicker(models.LeaseRenewEvery())
	defer renew.Stop()
	poll := time.NewTicker(models.SystemSampleInterval * time.Second)
	defer poll.Stop()

	// The first sample must not wait a whole interval: the dashboard was opened
	// precisely because the operator wants to see the machine.
	s.pollAgent(ctx, session)

	for {
		select {
		case <-ctx.Done():
			return
		case <-session.conn.Done():
			return
		case <-renew.C:
			ok, err := s.RenewSystem(ctx, session.systemID)
			if err != nil {
				s.log.Warn("could not renew a system lease", "system_id", session.systemID, "error", err)
				continue
			}
			if !ok {
				session.conn.Close("another node took over this system")
				return
			}
		case <-poll.C:
			s.pollAgent(ctx, session)
		}
	}
}

// pollAgent takes one sample of the machine and stores it.
//
// A failed sample is not a dead connection: an agent that is busy collecting (or
// a machine under memory pressure) answers late, so the failure is recorded for
// the operator and the loop keeps its schedule. Only an agent that is silent for
// several cycles in a row is disconnected, and by then the read deadline has
// usually done it already.
func (s *SystemService) pollAgent(ctx context.Context, session *agentSession) {
	system, err := s.find(ctx, session.systemID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		session.conn.Close("the system disappeared")
		return
	}
	if !system.Enabled {
		session.conn.Close("the system was disabled")
		return
	}

	options := beszel.DataRequestOptions{
		CacheTimeMs:    uint16(models.SystemSampleInterval * 1000),
		IncludeDetails: session.detailsAt.IsZero() || time.Since(session.detailsAt) > models.SystemDetailsRefresh,
	}
	pollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	data, err := session.conn.Sample(pollCtx, options)
	if err != nil {
		s.recordAgentError(ctx, system.ID, err)
		// Keep a slow connection alive until the next cycle (upstream does the
		// same): the ping extends the read deadline without needing an answer.
		session.conn.Ping()
		return
	}
	if options.IncludeDetails && data.Details != nil {
		session.detailsAt = time.Now().UTC()
	}
	if err := s.IngestSample(ctx, system, data); err != nil {
		s.log.Warn("could not store a hardware sample", "system_id", system.ID, "error", err)
		return
	}
	s.maybeRefreshSmart(ctx, session)
}

// maybeRefreshSmart asks for the SMART report on its own (slow) cadence.
//
// SMART is not part of the sample: reading it spins up disks, and the agent keeps
// its own cache anyway. Asking every SystemSmartRefresh is what makes a disk that
// started failing visible without a manual refresh.
func (s *SystemService) maybeRefreshSmart(ctx context.Context, session *agentSession) {
	if !session.smartAt.IsZero() && time.Since(session.smartAt) < models.SystemSmartRefresh {
		return
	}
	session.smartAt = time.Now().UTC()
	s.RefreshSmart(ctx, session.systemID, func() (beszel.SmartReport, error) {
		smartCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		return session.conn.Smart(smartCtx)
	})
}

// recordAgentError stores why a sample failed, so the UI can explain it.
func (s *SystemService) recordAgentError(ctx context.Context, systemID uint, cause error) {
	message := cause.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&models.System{}).Where("id = ?", systemID).
		Updates(map[string]any{"last_error": message, "updated_at": now}).Error; err != nil {
		s.log.Debug("could not record the agent error", "system_id", systemID, "error", err)
		return
	}
	s.publish("system.error", map[string]any{"id": systemID, "error": message})
}
