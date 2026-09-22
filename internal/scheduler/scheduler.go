package scheduler

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// Scheduler runs one goroutine per active monitor. Each worker keeps its own
// ticker (no cron dependency), honours the per monitor interval, timeout and
// retry policy and stores the resulting heartbeat with this node identity.
type Scheduler struct {
	cfg       *config.Config
	log       *slog.Logger
	monitors  *services.MonitorService
	heartbeat *services.HeartbeatService
	cluster   *services.ClusterService
	stats     *services.StatsService

	mu       sync.RWMutex
	workers  map[uint]*worker
	commands chan command
	sem      chan struct{}
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

type commandKind int

const (
	cmdUpsert commandKind = iota
	cmdRemove
	cmdReload
	cmdCheckNow
)

type command struct {
	kind      commandKind
	monitorID uint
}

// New builds the scheduler.
func New(cfg *config.Config, log *slog.Logger, monitors *services.MonitorService,
	heartbeat *services.HeartbeatService, cluster *services.ClusterService, stats *services.StatsService) *Scheduler {
	return &Scheduler{
		cfg:       cfg,
		log:       log,
		monitors:  monitors,
		heartbeat: heartbeat,
		cluster:   cluster,
		stats:     stats,
		workers:   map[uint]*worker{},
		commands:  make(chan command, 64),
		sem:       make(chan struct{}, cfg.SchedulerMaxConcurrent),
	}
}

// Start launches the workers and the command loop.
func (s *Scheduler) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel

	if err := s.reload(ctx); err != nil {
		return err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(ctx)
	}()

	s.log.Info("scheduler started",
		"workers", s.workerCount(), "max_concurrent", s.cfg.SchedulerMaxConcurrent,
		"node_id", s.cfg.NodeID)
	return nil
}

// Stop cancels every worker and waits for them to finish.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.log.Info("scheduler stopped")
}

// loop processes the reconciliation commands.
func (s *Scheduler) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-s.commands:
			switch cmd.kind {
			case cmdUpsert:
				s.upsert(ctx, cmd.monitorID)
			case cmdRemove:
				s.remove(cmd.monitorID)
			case cmdReload:
				if err := s.reload(ctx); err != nil {
					s.log.Error("could not reload the monitors", "error", err)
				}
			case cmdCheckNow:
				s.checkNow(cmd.monitorID)
			}
		}
	}
}

// Reload schedules a full reconciliation.
func (s *Scheduler) Reload() {
	select {
	case s.commands <- command{kind: cmdReload}:
	default: // a reload is already pending
	}
}

// Upsert schedules the (re)configuration of a single monitor.
func (s *Scheduler) Upsert(monitorID uint) {
	select {
	case s.commands <- command{kind: cmdUpsert, monitorID: monitorID}:
	default:
	}
}

// Remove stops the worker of a deleted monitor.
func (s *Scheduler) Remove(monitorID uint) {
	select {
	case s.commands <- command{kind: cmdRemove, monitorID: monitorID}:
	default:
	}
}

// CheckNow triggers an immediate execution (the "check now" button of the UI).
func (s *Scheduler) CheckNow(monitorID uint) {
	select {
	case s.commands <- command{kind: cmdCheckNow, monitorID: monitorID}:
	default:
		s.log.Warn("could not queue the immediate check", "monitor_id", monitorID)
	}
}

// reload loads the monitors this node is responsible for and reconciles the
// workers with the current database state.
func (s *Scheduler) reload(ctx context.Context) error {
	isPrimary := s.cluster.IsPrimary(ctx)
	monitors, err := s.monitors.ActiveForHost(ctx, s.cfg.NodeID, isPrimary)
	if err != nil {
		return err
	}

	expected := make(map[uint]*models.Monitor, len(monitors))
	for _, monitor := range monitors {
		expected[monitor.ID] = monitor
	}

	s.mu.RLock()
	current := make(map[uint]*worker, len(s.workers))
	for id, w := range s.workers {
		current[id] = w
	}
	s.mu.RUnlock()

	// Stop the workers that are no longer expected.
	for id, w := range current {
		if _, ok := expected[id]; !ok {
			w.stop()
			s.mu.Lock()
			delete(s.workers, id)
			s.mu.Unlock()
			s.log.Info("monitor worker removed", "monitor_id", id)
		}
	}

	// Start (or refresh) the expected workers.
	for id, monitor := range expected {
		if existing, ok := current[id]; ok {
			existing.update(monitor)
			continue
		}
		s.startWorker(ctx, monitor)
	}
	return nil
}
