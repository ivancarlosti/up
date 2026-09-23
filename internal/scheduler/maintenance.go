package scheduler

import (
	"context"
	"time"
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
//   - heartbeat retention purge (every 6h) when HEARTBEAT_RETENTION_DAYS > 0.
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
		if s.cfg.HeartbeatRetentionDays > 0 {
			s.purge(ctx)
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
				if s.cfg.HeartbeatRetentionDays > 0 {
					s.purge(ctx)
				}
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
