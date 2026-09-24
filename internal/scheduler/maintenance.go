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

	// The pull loop only exists when this node synchronises with peers. A nil
	// channel blocks forever, which is exactly what "no loop" means inside a
	// select.
	var pullC <-chan time.Time
	var pull *time.Ticker
	if s.sync != nil && s.sync.Enabled() {
		pull = time.NewTicker(s.sync.NextSyncTick())
		pullC = pull.C
		s.log.Info("federated synchronisation started", "node_id", s.cfg.NodeID, "every", s.sync.NextSyncTick())
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer ping.Stop()
		defer sweep.Stop()
		defer reconcile.Stop()
		defer certificates.Stop()
		defer retention.Stop()
		if pull != nil {
			defer pull.Stop()
		}

		// Run both cluster tasks once at boot so the nodes table is accurate
		// before the first dashboard request.
		if err := s.cluster.Ping(ctx); err != nil {
			s.log.Warn("node liveness ping failed", "error", err)
		}
		// The peer rows say who answers over HTTP; the local row above only says
		// that this process is alive.
		s.cluster.PingPeers(ctx)
		if err := s.cluster.Sweep(ctx); err != nil {
			s.log.Warn("node liveness sweep failed", "error", err)
		}
		s.purgeRetention(ctx)
		// The first pull happens right after the boot, so a node that was restarted
		// catches up without waiting a whole interval.
		if s.sync != nil {
			s.sync.PullPeers(ctx)
		}
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
				s.cluster.PingPeers(ctx)
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
			case <-pullC:
				s.sync.PullPeers(ctx)
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

	// The synchronisation outbox keeps the changes a peer may still need. Older
	// than the retention it is pruned, and a peer whose cursor points before the
	// pruned region is reconciled from the snapshots instead.
	if s.sync != nil && s.sync.Enabled() {
		before := now.AddDate(0, 0, -s.cfg.ClusterSyncTombstoneDays)
		switch deleted, err := s.sync.PruneOutbox(ctx, before); {
		case err != nil:
			s.log.Error("outbox prune failed", "error", err)
		case deleted > 0:
			s.log.Info("outbox prune finished",
				"deleted", deleted, "older_than_days", s.cfg.ClusterSyncTombstoneDays)
		}
	}
}
