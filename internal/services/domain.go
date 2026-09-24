package services

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/expiry"
	"github.com/ivancarlosti/up/internal/models"
)

// DomainService keeps the registry expiration of every monitor that watches one
// and decides when a reminder must be sent.
//
// It is the domain counterpart of CertificateService: it stores what the
// resolver read and, on the daily schedule, turns "days left" into the
// notifications. The network lookups are deduplicated by the expiry job (several
// monitors on the same registrable domain share one lookup).
type DomainService struct {
	db            *gorm.DB
	cfg           *config.Config
	log           *slog.Logger
	hub           EventPublisher
	monitors      *MonitorService
	notifications *NotificationService
	cluster       *ClusterService
	resolver      *expiry.Resolver
}

// NewDomainService builds the domain service.
func NewDomainService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *DomainService {
	return &DomainService{db: db, cfg: cfg, log: log, resolver: expiry.NewResolver()}
}

// SetPublisher injects the real time publisher.
func (s *DomainService) SetPublisher(p EventPublisher) { s.hub = p }

// SetMonitorService injects the monitor service (the watcher needs the monitor
// configuration to know what to watch).
func (s *DomainService) SetMonitorService(m *MonitorService) { s.monitors = m }

// SetNotificationService injects the channel dispatcher.
func (s *DomainService) SetNotificationService(n *NotificationService) { s.notifications = n }

// SetClusterService injects the service that elects the notification sender.
func (s *DomainService) SetClusterService(c *ClusterService) { s.cluster = c }

// Resolver returns the shared resolver. The expiry job configures it with the
// settings and the parsers of the run.
func (s *DomainService) Resolver() *expiry.Resolver { return s.resolver }

func (s *DomainService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// Record stores the observation of a lookup and reports whether it changed.
//
// The notification bookkeeping (notified thresholds, last notified day) is
// preserved: only the observation columns are written, so the daily job does not
// lose the memory of what it already alerted.
func (s *DomainService) Record(ctx context.Context, monitorID uint, nodeID string, info *models.DomainInfo) (bool, error) {
	if info == nil {
		return false, nil
	}
	if info.CheckedAt.IsZero() {
		info.CheckedAt = time.Now().UTC()
	}
	current, err := s.Get(ctx, monitorID)
	if err != nil {
		return false, err
	}
	// The observation is clipped to the column widths before anything else (the
	// registrar and the error are free text coming from a registry), and the
	// comparison uses the clipped row so a clipped value cannot look "changed"
	// on every run.
	row := models.MonitorDomain{
		MonitorID:     monitorID,
		Domain:        trimmed(info.Domain, models.MaxDomainLen),
		Registrar:     trimmed(info.Registrar, models.MaxDomainRegistrarLen),
		ExpiresAt:     info.ExpiresAt,
		Source:        string(info.Source),
		Status:        string(info.Status),
		Error:         trimmed(info.Error, models.MaxDomainErrorLen),
		DaysLeft:      info.DaysLeft,
		CheckedAt:     info.CheckedAt,
		CheckedByNode: nodeID,
	}
	changed := current == nil ||
		current.Domain != row.Domain ||
		!current.ExpiresAt.Equal(row.ExpiresAt) ||
		current.DaysLeft != row.DaysLeft ||
		current.Status != row.Status
	// The columns of the notification memory are only set on insert: an update
	// must not reset them.
	if current == nil {
		row.NotifiedDays = ""
		row.LastNotifiedDay = 0
	}
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "monitor_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"domain", "registrar", "expires_at", "source", "status", "error",
			"days_left", "checked_at", "checked_by_node",
		}),
	}).Create(&row)
	if result.Error != nil {
		return false, ErrInternal(fmt.Errorf("storing the domain of monitor %d: %w", monitorID, result.Error))
	}
	if changed {
		s.log.Info("domain expiration captured",
			"monitor_id", monitorID, "domain", info.Domain, "status", info.Status,
			"expires_at", info.ExpiresAt.Format(time.RFC3339), "days_left", info.DaysLeft, "node_id", nodeID)
	}
	return changed, nil
}

// Get returns the stored domain of a monitor (nil when there is none).
func (s *DomainService) Get(ctx context.Context, monitorID uint) (*models.MonitorDomain, error) {
	var row models.MonitorDomain
	err := s.db.WithContext(ctx).Where("monitor_id = ?", monitorID).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	return &row, nil
}

// ByMonitorIDs returns the stored domain of each monitor (one query). It is what
// the admin expiry view uses to preview a worklist without running it.
func (s *DomainService) ByMonitorIDs(ctx context.Context, monitorIDs []uint) (map[uint]*models.MonitorDomain, error) {
	out := map[uint]*models.MonitorDomain{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var rows []models.MonitorDomain
	if err := s.db.WithContext(ctx).Where("monitor_id IN ?", monitorIDs).Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for i := range rows {
		out[rows[i].MonitorID] = &rows[i]
	}
	return out, nil
}

// All returns the stored domains of the monitors that watch one, with their
// monitor loaded: this is what an evaluation-only pass iterates.
func (s *DomainService) All(ctx context.Context) ([]models.MonitorDomain, []*models.Monitor, error) {
	var rows []models.MonitorDomain
	if err := s.db.WithContext(ctx).
		Joins("JOIN monitors ON monitors.id = monitor_domains.monitor_id").
		Where("monitors.domain_watch = ?", true).
		Order("monitor_domains.monitor_id ASC").
		Find(&rows).Error; err != nil {
		return nil, nil, ErrInternal(err)
	}
	if len(rows) == 0 {
		return rows, nil, nil
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MonitorID)
	}
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&monitors).Error; err != nil {
		return nil, nil, ErrInternal(err)
	}
	byID := map[uint]*models.Monitor{}
	for _, monitor := range monitors {
		byID[monitor.ID] = monitor
	}
	ordered := make([]*models.Monitor, 0, len(rows))
	for _, row := range rows {
		if monitor, ok := byID[row.MonitorID]; ok {
			ordered = append(ordered, monitor)
		}
	}
	return rows, ordered, nil
}
