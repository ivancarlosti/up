package scheduler

import (
	"context"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// StartMaintenance launches the background housekeeping loops:
//
//   - node liveness ping (every 30s) so the cluster knows this node is alive;
//   - node liveness sweep (every 30s) marking late/offline nodes;
//   - worker reconciliation (every SCHEDULER_RECONCILE_SECONDS, default 30s); it
//     compares the running workers with the monitors this node must run, which
//     is how a monitor created on another node starts being checked here;
//   - certificate watching (every 6h): it ages the stored certificates and sends
//     the reminders that are due (the certificates themselves are captured by
//     the probes, so no socket is opened here);
//   - retention (every 6h): heartbeats when HEARTBEAT_RETENTION_DAYS > 0,
//     notification de-duplication locks always, delivery history when
//     NOTIFICATION_LOG_RETENTION_DAYS > 0.
func (s *Scheduler) StartMaintenance(ctx context.Context) {
	ping := time.NewTicker(30 * time.Second)
	sweep := time.NewTicker(30 * time.Second)
	reconcile := time.NewTicker(s.reconcileInterval())
	certificates := time.NewTicker(6 * time.Hour)
	retention := time.NewTicker(6 * time.Hour)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer ping.Stop()
		defer sweep.Stop()
		defer reconcile.Stop()
		defer certificates.Stop()
		defer retention.Stop()

		// Run both cluster tasks once at boot so the nodes table is accurate
		// before the first dashboard request.
		if err := s.cluster.Ping(ctx); err != nil {
			s.log.Warn("node liveness ping failed", "error", err)
		}
		if err := s.cluster.Sweep(ctx); err != nil {
			s.log.Warn("node liveness sweep failed", "error", err)
		}
		s.purgeRetention(ctx)
		// A long lived certificate must not wait 6h for its first evaluation.
		if s.certificates != nil {
			if err := s.certificates.Refresh(ctx); err != nil {
				s.log.Warn("certificate watcher failed", "error", err)
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ping.C:
				if err := s.cluster.Ping(ctx); err != nil {
					s.log.Warn("node liveness ping failed", "error", err)
				}
			case <-sweep.C:
				if err := s.cluster.Sweep(ctx); err != nil {
					s.log.Warn("node liveness sweep failed", "error", err)
				}
			case <-reconcile.C:
				if err := s.reload(ctx); err != nil {
					s.log.Error("could not reconcile the monitors", "error", err)
				}
			case <-certificates.C:
				if s.certificates != nil {
					if err := s.certificates.Refresh(ctx); err != nil {
						s.log.Error("could not refresh the certificates", "error", err)
					}
				}
			case <-retention.C:
				s.purgeRetention(ctx)
			}
		}
	}()
}

// purge applies the heartbeat retention policy.
func (s *Scheduler) purge(ctx context.Context) {
	before := time.Now().UTC().AddDate(0, 0, -s.cfg.HeartbeatRetentionDays)
	deleted, err := s.stats.Purge(ctx, before)
	if err != nil {
		s.log.Error("heartbeat retention purge failed", "error", err)
		return
	}
	if deleted > 0 {
		s.log.Info("heartbeat retention purge finished",
			"deleted", deleted, "older_than_days", s.cfg.HeartbeatRetentionDays)
	}
}

// purgeRetention applies every retention policy of the tables that grow over
// time. It runs at boot and then every six hours.
func (s *Scheduler) purgeRetention(ctx context.Context) {
	now := time.Now().UTC()

	if s.cfg.HeartbeatRetentionDays > 0 {
		s.purge(ctx)
	}

	// The de-duplication locks are always pruned: a lock is meaningless after
	// its own window, and nothing else ever removed it, so the table used to
	// grow for the whole life of the deployment.
	before := now.Add(-models.NotificationLockRetentionHours * time.Hour)
	switch deleted, err := s.cluster.PurgeExpiredLocks(ctx, before); {
	case err != nil:
		s.log.Error("notification lock purge failed", "error", err)
	case deleted > 0:
		s.log.Info("notification lock purge finished", "deleted", deleted)
	}

	// The delivery history is opt-in: it is information the operator may want
	// to keep.
	if s.cfg.NotificationLogRetentionDays > 0 && s.notifications != nil {
		before := now.AddDate(0, 0, -s.cfg.NotificationLogRetentionDays)
		switch deleted, err := s.notifications.PurgeOlderThan(ctx, before); {
		case err != nil:
			s.log.Error("notification log retention purge failed", "error", err)
		case deleted > 0:
			s.log.Info("notification log retention purge finished",
				"deleted", deleted, "older_than_days", s.cfg.NotificationLogRetentionDays)
		}
	}
}
