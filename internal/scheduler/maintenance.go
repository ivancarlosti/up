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
//   - the expiry job (ticked every minute, run once a day at the configured
//     time): it refreshes the deduplicated TLS certificate and domain
//     registrations with a single lookup per target and sends the reminders that
//     are due (see services.ExpiryService).
//   - retention (every 6h): heartbeats when HEARTBEAT_RETENTION_DAYS > 0,
//     notification de-duplication locks always, delivery history when
//     NOTIFICATION_LOG_RETENTION_DAYS > 0.
func (s *Scheduler) StartMaintenance(ctx context.Context) {
	ping := time.NewTicker(30 * time.Second)
	sweep := time.NewTicker(30 * time.Second)
	reconcile := time.NewTicker(s.reconcileInterval())
	// The expiry job ticks every minute and decides for itself whether the
	// configured time of day has been reached: a daily certificate/domain check
	// must not depend on a process that happens to be up at 03:00.
	expiry := time.NewTicker(time.Minute)
	retention := time.NewTicker(6 * time.Hour)

	// The pull loop only exists when this node synchronises with peers. A nil
	// channel blocks forever, which is exactly what "no loop" means inside a
	// select.
	var pullC <-chan time.Time
	var pull *time.Ticker
	var votesC <-chan time.Time
	var votes *time.Ticker
	if s.sync != nil && s.sync.Enabled() {
		pull = time.NewTicker(s.sync.NextSyncTick())
		pullC = pull.C
		// The verdicts are fetched twice as often as the configuration: a peer's vote is
		// only worth anything while it is fresh, so waiting a whole sync interval for it
		// would shrink the window the aggregation can use.
		votesEvery := s.sync.NextSyncTick() / 2
		if votesEvery < time.Second {
			votesEvery = time.Second
		}
		votes = time.NewTicker(votesEvery)
		votesC = votes.C
		s.log.Info("federated synchronisation started", "node_id", s.cfg.NodeID, "every", s.sync.NextSyncTick())
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer ping.Stop()
		defer sweep.Stop()
		defer reconcile.Stop()
		defer expiry.Stop()
		defer retention.Stop()
		if pull != nil {
			defer pull.Stop()
		}
		if votes != nil {
			defer votes.Stop()
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
		// The retention policy lives in the settings table (Admin > Settings),
		// with HEARTBEAT_RETENTION_DAYS as a fallback: report the effective value
		// so an operator can tell what the housekeeping loop will do.
		if days := s.retentionDays(); days > 0 {
			s.log.Info("heartbeat retention active", "days", days)
		} else {
			s.log.Info("heartbeat retention disabled: the history is kept forever")
		}
		// Make the daily schedule visible from the first seconds: the job is silent
		// until its time comes (a once-a-day cadence on a one-minute ticker), which
		// otherwise reads like a job that never runs.
		if s.expiry != nil {
			settings := s.expiry.Settings()
			s.log.Info("expiry job scheduled",
				"check_time", settings.CheckTime,
				"timezone", settings.CheckTimezone,
				"next_run", models.NextDailyRun(time.Now().UTC(), settings.CheckTime, settings.CheckTimezone).Format(time.RFC3339),
				"rdap", settings.RDAPEnabled,
				"whois", settings.WHOISEnabled)
		}
		// The first pull happens right after the boot, so a node that was restarted
		// catches up without waiting a whole interval.
		if s.sync != nil {
			s.sync.PullPeers(ctx)
			s.sync.PullVotes(ctx)
		}
		// A long lived certificate must not wait for its first evaluation: the
		// pass ages what is already stored (no socket) and sends any due reminder.
		if s.expiry != nil {
			if err := s.expiry.Evaluate(ctx); err != nil {
				s.log.Warn("expiry evaluation failed", "error", err)
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
			case <-expiry.C:
				if s.expiry != nil {
					if err := s.expiry.RunDue(ctx, time.Now().UTC()); err != nil {
						s.log.Error("could not run the expiry job", "error", err)
					}
				}
			case <-retention.C:
				s.purgeRetention(ctx)
			case <-pullC:
				s.sync.PullPeers(ctx)
			case <-votesC:
				s.sync.PullVotes(ctx)
			}
		}
	}()
}

// retentionDays resolves the heartbeat retention policy: the shared setting
// wins, HEARTBEAT_RETENTION_DAYS is the fallback of a deployment that never
// opened Admin > Settings. 0 means "never purge".
func (s *Scheduler) retentionDays() int {
	if s.settings != nil {
		return s.settings.HeartbeatRetentionDays()
	}
	if s.cfg.HeartbeatRetentionDays > 0 {
		return s.cfg.HeartbeatRetentionDays
	}
	return models.DefaultHeartbeatRetentionDays
}

// purge applies the heartbeat retention policy.
func (s *Scheduler) purge(ctx context.Context) {
	days := s.retentionDays()
	if days <= 0 {
		return
	}
	before := time.Now().UTC().AddDate(0, 0, -days)
	deleted, err := s.stats.Purge(ctx, before)
	if err != nil {
		s.log.Error("heartbeat retention purge failed", "error", err)
		return
	}
	if deleted > 0 {
		s.log.Info("heartbeat retention purge finished",
			"deleted", deleted, "older_than_days", days)
	}
}

// purgeRetention applies every retention policy of the tables that grow over
// time. It runs at boot and then every six hours.
func (s *Scheduler) purgeRetention(ctx context.Context) {
	now := time.Now().UTC()

	if s.retentionDays() > 0 {
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

	// The synchronisation outbox keeps the changes a peer may still need, and the
	// conflict log and dead letters keep the history of what went wrong. Older than the
	// retention both are pruned; a peer whose cursor points before the pruned region is
	// reconciled from the snapshots instead, and the conflict log is history rather
	// than state.
	if s.sync != nil && s.sync.Enabled() {
		before := now.AddDate(0, 0, -s.cfg.ClusterSyncTombstoneDays)
		switch deleted, err := s.sync.PruneOutbox(ctx, before); {
		case err != nil:
			s.log.Error("outbox prune failed", "error", err)
		case deleted > 0:
			s.log.Info("outbox prune finished",
				"deleted", deleted, "older_than_days", s.cfg.ClusterSyncTombstoneDays)
		}
		switch deleted, err := s.sync.PruneSyncHistory(ctx, before); {
		case err != nil:
			s.log.Error("sync history prune failed", "error", err)
		case deleted > 0:
			s.log.Info("sync history prune finished",
				"deleted", deleted, "older_than_days", s.cfg.ClusterSyncTombstoneDays)
		}
	}
}
