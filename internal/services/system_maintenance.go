package services

import (
	"context"
	"fmt"
	"time"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// SystemRetentionDays is how many days of hardware history are kept.
//
// It lives in the settings table (Admin > Settings), so every node of a cluster
// purges on the same policy, and 0 means "never purge".
func (s *SystemService) SystemRetentionDays() int {
	if s.settings != nil {
		return s.settings.SystemRetentionDays()
	}
	return models.DefaultSystemRetentionDays
}

// PurgeHistory deletes the samples outside the retention window.
//
// The three history tables are pruned together: a container chart and the machine
// chart of the same minute must cover the same period, otherwise the container
// page shows an "empty" container that actually has data outside the window.
// The container snapshots (the current state) and the SMART report are never
// pruned: they are the latest observation, not history.
func (s *SystemService) PurgeHistory(ctx context.Context) (int64, error) {
	days := s.SystemRetentionDays()
	if days <= 0 {
		return 0, nil
	}
	before := time.Now().UTC().AddDate(0, 0, -days)
	var deleted int64
	for _, model := range []any{&models.SystemMetric{}, &models.SystemContainerMetric{}} {
		result := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(model)
		if result.Error != nil {
			return deleted, ErrInternal(fmt.Errorf("purging hardware history: %w", result.Error))
		}
		deleted += result.RowsAffected
	}
	if deleted > 0 {
		s.log.Info("purged hardware history", "rows", deleted, "older_than_days", days)
	}
	return deleted, nil
}

// RefreshSmartNow asks the agent for a fresh SMART report on demand.
//
// It only works on the node that owns the connection: the agent is a socket, so a
// node that does not hold it has nothing to ask. The UI hides the button in that
// case, and the answer here says why instead of pretending it worked.
func (s *SystemService) RefreshSmartNow(ctx context.Context, systemID uint) error {
	session := s.agents.session(systemID)
	if session == nil {
		return ErrConflict(i18n.CodeSystemAgentBusy, "this node is not connected to the agent of that system")
	}
	s.RefreshSmart(ctx, systemID, func() (beszel.SmartReport, error) {
		smartCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		return session.conn.Smart(smartCtx)
	})
	session.smartAt = time.Now().UTC()
	return nil
}
