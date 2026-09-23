package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// EventPublisher receives the real time events broadcast over WebSocket. It is
// implemented by ws.Hub and injected by main, which keeps the services free of
// any WebSocket dependency.
type EventPublisher interface {
	Publish(event string, payload any)
}

// MonitorFilter narrows down a monitor listing.
type MonitorFilter struct {
	Type    string
	Search  string
	Active  *bool
	Tag     string
	GroupID uint
}

// VoteProvider is implemented by the cluster service: it merges the per node
// heartbeats into a single aggregated status per monitor.
type VoteProvider interface {
	EvaluateAll(ctx context.Context, monitors []*models.Monitor) (map[uint][]models.NodeVote, map[uint]models.AggregateStatus, error)
}

// MonitorService owns the monitor CRUD and the runtime decoration (status,
// uptime, latency and per node votes) used by the dashboard.
type MonitorService struct {
	db    *gorm.DB
	cfg   *config.Config
	log   *slog.Logger
	stats *StatsService
	hub   EventPublisher
	votes VoteProvider
}

// NewMonitorService builds the monitor service.
func NewMonitorService(db *gorm.DB, cfg *config.Config, log *slog.Logger, stats *StatsService) *MonitorService {
	return &MonitorService{db: db, cfg: cfg, log: log, stats: stats}
}

// SetPublisher injects the real time publisher.
func (s *MonitorService) SetPublisher(p EventPublisher) { s.hub = p }

// SetVoteProvider injects the cluster vote provider.
func (s *MonitorService) SetVoteProvider(v VoteProvider) { s.votes = v }

// List returns the monitors matching the filter (not decorated).
func (s *MonitorService) List(ctx context.Context, filter MonitorFilter) ([]*models.Monitor, error) {
	query := s.db.WithContext(ctx).Model(&models.Monitor{})

	if filter.Type != "" {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.Active != nil {
		query = query.Where("active = ?", *filter.Active)
	}
	if filter.Tag != "" {
		query = query.Where("tags LIKE ?", "%"+filter.Tag+"%")
	}
	if filter.GroupID > 0 {
		query = query.Where("id IN (?)",
			s.db.Model(&models.MonitorGroupMember{}).Select("monitor_id").Where("group_id = ?", filter.GroupID))
	}
	if filter.Search != "" {
		like := "%" + strings.TrimSpace(filter.Search) + "%"
		query = query.Where("name LIKE ? OR description LIKE ? OR tags LIKE ?", like, like, like)
	}

	var monitors []*models.Monitor
	if err := query.Order("name ASC").Find(&monitors).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing monitors: %w", err))
	}
	return monitors, nil
}

// Get loads one monitor and decorates it.
func (s *MonitorService) Get(ctx context.Context, id uint) (*models.Monitor, error) {
	var monitor models.Monitor
	err := s.db.WithContext(ctx).First(&monitor, id).Error
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeMonitorNotFound, fmt.Sprintf("monitor %d does not exist", id))
	}
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("loading monitor %d: %w", id, err))
	}
	decorated := []*models.Monitor{&monitor}
	if err := s.Decorate(ctx, decorated); err != nil {
		return nil, err
	}
	return &monitor, nil
}

// Decorate fills the runtime fields of the given monitors: aggregated status,
// uptime 24h, latency of the last check, per node votes and notification ids.
func (s *MonitorService) Decorate(ctx context.Context, monitors []*models.Monitor) error {
	if len(monitors) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(monitors))
	for _, m := range monitors {
		ids = append(ids, m.ID)
	}

	windows, err := s.stats.Windows(ctx, ids)
	if err != nil {
		return ErrInternal(err)
	}
	latest, err := s.stats.LatestPerMonitor(ctx, ids)
	if err != nil {
		return ErrInternal(err)
	}

	var (
		votes      map[uint][]models.NodeVote
		aggregates map[uint]models.AggregateStatus
	)
	if s.votes != nil {
		votes, aggregates, err = s.votes.EvaluateAll(ctx, monitors)
		if err != nil {
			return ErrInternal(err)
		}
	}

	links, err := s.AllLinks(ctx)
	if err != nil {
		return err
	}
	linksByMonitor := map[uint][]uint{}
	for _, link := range links {
		linksByMonitor[link.MonitorID] = append(linksByMonitor[link.MonitorID], link.NotificationID)
	}

	groups, err := s.AllGroupMembers(ctx)
	if err != nil {
		return err
	}

	certificates, err := s.certificatesFor(ctx, ids)
	if err != nil {
		return err
	}

	for _, m := range monitors {
		if stats, ok := windows[m.ID]; ok {
			m.Uptime24h = stats.Uptime
		}
		if hb, ok := latest[m.ID]; ok {
			created := hb.CreatedAt
			m.LastCheckAt = &created
			m.LastLatencyMS = hb.LatencyMS
		}
		if aggregates != nil {
			if status, ok := aggregates[m.ID]; ok {
				m.Status = status
			}
		}
		if m.Status == "" {
			m.Status = models.AggregateUnknown
		}
		if votes != nil {
			m.Votes = votes[m.ID]
		}
		if ids, ok := linksByMonitor[m.ID]; ok {
			m.NotificationIDs = ids
		} else {
			m.NotificationIDs = []uint{}
		}
		if ids, ok := groups[m.ID]; ok {
			m.GroupIDs = ids
		} else {
			m.GroupIDs = []uint{}
		}
		if m.CertWatch {
			if info, ok := certificates[m.ID]; ok {
				m.Certificate = info
			}
		}
	}
	return nil
}

// certificatesFor returns the stored certificate of every monitor that watches
// one (one query for a whole listing).
func (s *MonitorService) certificatesFor(ctx context.Context, monitorIDs []uint) (map[uint]*models.CertificateInfo, error) {
	out := map[uint]*models.CertificateInfo{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var rows []models.MonitorCertificate
	if err := s.db.WithContext(ctx).Where("monitor_id IN ?", monitorIDs).Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for i := range rows {
		out[rows[i].MonitorID] = rows[i].Info()
	}
	return out, nil
}

func (s *MonitorService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}
