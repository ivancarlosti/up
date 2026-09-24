package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
	// reloadMu serialises the worker reconciliation: the command loop
	// (cmdReload) and the periodic maintenance ticker can both run it, and two
	// concurrent passes would start the same monitor twice.
	reloadMu sync.Mutex
	// certificates stores and evaluates the TLS certificates read by the probes
	// (optional: nil on a build without the feature).
	certificates *services.CertificateService
	// notifications is injected for the retention job only: the delivery history
	// is the one notification table that grows over time, and the scheduler owns
	// the housekeeping loop.
	notifications *services.NotificationService
	// sync drives the federated pull loop (nil unless it is wired).
	sync *services.SyncService
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
		"reconcile_seconds", s.reconcileIntervalSeconds(),
		"node_id", s.cfg.NodeID)
	return nil
}

// SetCertificateService injects the TLS certificate service: it stores what the
// probes read and decides when a reminder is due.
func (s *Scheduler) SetCertificateService(c *services.CertificateService) { s.certificates = c }

// SetNotificationService injects the notification service so the maintenance
// loop can apply NOTIFICATION_LOG_RETENTION_DAYS to the delivery history.
func (s *Scheduler) SetNotificationService(n *services.NotificationService) {
	s.notifications = n
}

// SetSyncService injects the federated synchronisation service, which the
// maintenance loop drives on its own ticker.
func (s *Scheduler) SetSyncService(sync *services.SyncService) { s.sync = sync }

// reconcileInterval is how often the worker set is compared with the database.
func (s *Scheduler) reconcileInterval() time.Duration {
	return time.Duration(s.reconcileIntervalSeconds()) * time.Second
}

// reconcileIntervalSeconds returns the configured interval with a safety floor.
func (s *Scheduler) reconcileIntervalSeconds() int {
	if s.cfg.SchedulerReconcileSeconds < 5 {
		return 30
	}
	return s.cfg.SchedulerReconcileSeconds
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
//
// It runs at boot, on demand (Scheduler.Reload) and periodically (see
// StartMaintenance): the periodic pass is what makes a monitor created on
// another node of the cluster start being checked here, because nothing pushes
// the monitor to this process.
func (s *Scheduler) reload(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	isPrimary := s.cluster.IsPrimary(ctx)
	monitors, err := s.monitors.ActiveForHost(ctx, s.cfg.NodeID, isPrimary)
	if err != nil {
		return err
	}

	expected := make(map[uint]*models.Monitor, len(monitors))
	expectedIDs := make([]uint, 0, len(monitors))
	for _, monitor := range monitors {
		expected[monitor.ID] = monitor
		expectedIDs = append(expectedIDs, monitor.ID)
	}

	s.mu.RLock()
	current := make(map[uint]*worker, len(s.workers))
	runningIDs := make([]uint, 0, len(s.workers))
	for id, w := range s.workers {
		current[id] = w
		runningIDs = append(runningIDs, id)
	}
	s.mu.RUnlock()

	plan := planReconcile(expectedIDs, runningIDs)

	// Stop the workers that are no longer expected (deleted, paused, or a
	// run_on that no longer matches this node).
	for _, id := range plan.Stop {
		w, ok := current[id]
		if !ok {
			continue
		}
		w.stop()
		s.mu.Lock()
		delete(s.workers, id)
		s.mu.Unlock()
		s.log.Info("monitor worker removed", "monitor_id", id, "reason", "reconciliation")
	}

	// Refresh the configuration of the ones that stay: a reload may have
	// changed the interval or the monitor options.
	for _, id := range plan.Keep {
		if w, ok := current[id]; ok {
			w.update(expected[id])
		}
	}

	// Start the new ones.
	for _, id := range plan.Start {
		s.startWorker(ctx, expected[id])
	}

	if plan.changed() {
		s.log.Info("scheduler reconciled the workers",
			"started", plan.Start, "stopped", plan.Stop, "unchanged", len(plan.Keep),
			"workers", s.workerCount())
	} else {
		s.log.Debug("scheduler reconciliation: nothing to change", "workers", len(plan.Keep))
	}
	return nil
}
