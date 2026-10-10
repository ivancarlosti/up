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
// turn is the address family of this execution (see worker.probeFamily): every
// attempt of the execution - and every connection of it, redirects included -
// uses that family, so a retry re-tests the path that just failed instead of
// hiding it behind the other one. The next execution rotates.
//
// singleShot is set by a manual "check now": the manual probe then runs once and
// the retry policy is bypassed, so the operator gets a heartbeat at once instead
// of waiting minutes behind a backoff.
//
// interrupt breaks the retry backoff when a new "check now" arrives while this
// execution is waiting between attempts (see worker). It returns true in that
// case, telling the caller that the heartbeat was NOT stored and that a fresh
// single-shot execution is owed: the pending manual check must not be delayed
// by the rest of the backoff, nor duplicated by it.
//
// Concurrency is capped by the scheduler semaphore (SCHEDULER_MAX_CONCURRENT),
// so hundreds of monitors do not open hundreds of sockets at the same instant.
func (s *Scheduler) execute(ctx context.Context, monitor *models.Monitor, turn string, singleShot bool, interrupt <-chan struct{}) bool {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return false
	}

	interval := time.Duration(monitor.RetriesIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Duration(monitor.IntervalSeconds) * time.Second
	}
	retries := monitor.Retries
	if singleShot && retries > 0 {
		retries = 0
	}
	result, _, interrupted := probeWithRetries(ctx, retries, interval, interrupt, func() checkers.Result {
		return checkers.Check(ctx, monitor, turn)
	})
	if interrupted {
		return true
	}

	heartbeat := &models.Heartbeat{
		MonitorID:  monitor.ID,
		NodeID:     s.cfg.NodeID,
		Status:     result.Status,
		LatencyMS:  result.LatencyMS,
		StatusCode: result.StatusCode,
		Message:    result.Message,
		Important:  verdictIsImportant(singleShot, monitor.Retries),
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.heartbeat.Record(ctx, heartbeat); err != nil {
		s.log.Error("could not store the heartbeat", "monitor_id", monitor.ID, "error", err)
		return false
	}

	// The certificate is a property of the handshake: it is stored right next to
	// the heartbeat and evaluated when it is new or has changed, so a monitor
	// pointed at an expired certificate warns immediately.
	if result.Certificate != nil && s.certificates != nil {
		changed, certErr := s.certificates.Record(ctx, monitor.ID, s.cfg.NodeID, result.Certificate)
		if certErr != nil {
			s.log.Error("could not store the certificate", "monitor_id", monitor.ID, "error", certErr)
		} else if changed {
			if row, rowErr := s.certificates.Get(ctx, monitor.ID); rowErr == nil && row != nil {
				s.certificates.Evaluate(ctx, row, monitor, time.Now().UTC())
			}
		}
	}

	if err := s.cluster.EvaluateAndNotify(ctx, monitor.ID); err != nil {
		s.log.Error("could not evaluate the monitor status", "monitor_id", monitor.ID, "error", err)
	}
	return false
}

// probeWithRetries runs the probe of an execution applying the retry policy.
//
// The first attempt is made immediately. While it fails with a down status and
// the budget is not exhausted, the helper waits `interval` (retries_interval_seconds,
// or the monitor interval when that is unset) and probes again.
//
// The wait is interruptible: a manual "check now" sends on `interrupt` (the
// worker trigger channel) and the helper returns at once with interrupted=true,
// so the pending check can run as a single attempt instead of queueing behind
// the backoff. attempts counts the extra probes actually performed.
func probeWithRetries(ctx context.Context, retries int, interval time.Duration, interrupt <-chan struct{}, probe func() checkers.Result) (result checkers.Result, attempts int, interrupted bool) {
	result = probe()
	for result.Status == models.StatusDown && attempts < retries {
		select {
		case <-time.After(interval):
		case <-interrupt:
			return result, attempts, true
		case <-ctx.Done():
			return result, attempts, false
		}
		result = probe()
		attempts++
	}
	return result, attempts, false
}

// verdictIsImportant reports whether a stored heartbeat is the monitor's
// definitive verdict - the only kind that may open or close an incident (see
// services.ClusterService.EvaluateAndNotify).
//
// A scheduled execution always produces the verdict: its retry policy has just
// run to completion. A manual "check now" that bypassed a retry policy is not a
// verdict: one failing probe must not alert while the monitor's own policy would
// still be retrying. A monitor with no retries reaches a verdict in a single
// probe, manual or not, so its heartbeat is important either way.
func verdictIsImportant(singleShot bool, retries int) bool {
	return !singleShot || retries == 0
}
