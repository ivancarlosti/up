package scheduler

import (
	"context"

	"github.com/ivancarlosti/up/internal/models"
)

// upsert creates or refreshes a single worker.
func (s *Scheduler) upsert(ctx context.Context, monitorID uint) {
	s.mu.RLock()
	_, exists := s.workers[monitorID]
	s.mu.RUnlock()

	monitor, err := s.monitors.Get(ctx, monitorID)
	if err != nil {
		s.log.Warn("could not configure the monitor worker", "monitor_id", monitorID, "error", err)
		return
	}

	// A paused monitor (or one this node does not handle) must not run here.
	if !monitor.Active || !s.handles(monitor, s.cluster.IsPrimary(ctx)) {
		if exists {
			s.remove(monitorID)
		}
		return
	}
	if !exists {
		s.startWorker(ctx, monitor)
		return
	}
	if w, ok := s.worker(monitorID); ok {
		w.update(monitor)
	}
}

// remove stops and forgets a worker.
func (s *Scheduler) remove(monitorID uint) {
	s.mu.Lock()
	w, ok := s.workers[monitorID]
	if ok {
		delete(s.workers, monitorID)
	}
	s.mu.Unlock()
	if ok {
		w.stop()
		s.log.Info("monitor worker removed", "monitor_id", monitorID)
	}
}

// checkNow forwards an immediate check to the worker.
func (s *Scheduler) checkNow(monitorID uint) {
	if w, ok := s.worker(monitorID); ok {
		w.triggerNow()
	}
}

// worker returns a running worker.
func (s *Scheduler) worker(monitorID uint) (*worker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.workers[monitorID]
	return w, ok
}

// handles tells whether this node executes the monitor (run_on).
//
// The rule itself lives in models.MonitorRunsOn, shared with the voter middleware: a
// node that probes a monitor nobody counts, or one asked to vote on a monitor it never
// saw, would each look like a monitor going silently unknown.
func (s *Scheduler) handles(monitor *models.Monitor, isPrimary bool) bool {
	return models.MonitorRunsOn(monitor, s.cfg.NodeID, isPrimary)
}

func (s *Scheduler) startWorker(ctx context.Context, monitor *models.Monitor) {
	if monitor.IntervalSeconds < 5 {
		monitor.IntervalSeconds = 5
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w := &worker{
		id:        monitor.ID,
		scheduler: s,
		trigger:   make(chan struct{}, 1),
		monitor:   monitor,
		cancel:    cancel,
	}

	// Registering is check-and-set under the same lock: the command loop
	// (upsert) and the periodic reconciliation can race on the same monitor id,
	// and a second goroutine for the same monitor would never be stopped.
	s.mu.Lock()
	if _, exists := s.workers[monitor.ID]; exists {
		s.mu.Unlock()
		cancel()
		return
	}
	s.workers[monitor.ID] = w
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		w.run(workerCtx)
	}()
	s.log.Info("monitor worker started",
		"monitor_id", monitor.ID, "monitor", monitor.Name,
		"type", monitor.Type, "interval_seconds", monitor.IntervalSeconds)
}

func (s *Scheduler) workerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.workers)
}
