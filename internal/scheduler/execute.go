package scheduler

import (
	"context"
	"time"

	"github.com/ivancarlosti/up/internal/checkers"
	"github.com/ivancarlosti/up/internal/models"
)

// execute runs the probe of a monitor applying the retry policy, stores the
// heartbeat and asks the cluster to re-evaluate the aggregated status (which is
// what ultimately triggers notifications).
//
// Concurrency is capped by the scheduler semaphore (SCHEDULER_MAX_CONCURRENT),
// so hundreds of monitors do not open hundreds of sockets at the same instant.
func (s *Scheduler) execute(ctx context.Context, monitor *models.Monitor) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return
	}

	result := checkers.Check(ctx, monitor)
	attempts := 0
	for result.Status == models.StatusDown && attempts < monitor.Retries {
		attempts++
		interval := time.Duration(monitor.RetriesIntervalSeconds) * time.Second
		if interval <= 0 {
			interval = time.Duration(monitor.IntervalSeconds) * time.Second
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return
		}
		result = checkers.Check(ctx, monitor)
	}

	heartbeat := &models.Heartbeat{
		MonitorID:  monitor.ID,
		NodeID:     s.cfg.NodeID,
		Status:     result.Status,
		LatencyMS:  result.LatencyMS,
		StatusCode: result.StatusCode,
		Message:    result.Message,
		// The heartbeat is "important" when it represents a definitive verdict:
		// either no retry was configured, or the retries ended up with a
		// successful check (a recovery).
		Important: attempts == 0 || result.Status == models.StatusUp,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.heartbeat.Record(ctx, heartbeat); err != nil {
		s.log.Error("could not store the heartbeat", "monitor_id", monitor.ID, "error", err)
		return
	}

	if err := s.cluster.EvaluateAndNotify(ctx, monitor.ID); err != nil {
		s.log.Error("could not evaluate the monitor status", "monitor_id", monitor.ID, "error", err)
	}
}
