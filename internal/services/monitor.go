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
	// settings owns the global uptime window applied by the decoration. It is
	// optional: a nil value falls back to the documented default.
	settings *SettingService
	hub      EventPublisher
	votes    VoteProvider
	// emit publishes the local writes to the peers. It is nil on a node that does
	// not publish (anything but federated mode), and every helper below is a no-op
	// in that case.
	emit *SyncEmitter
}

// SetSyncEmitter injects the publisher of the synchronisation outbox.
func (s *MonitorService) SetSyncEmitter(emitter *SyncEmitter) { s.emit = emitter }

// snapshotLinks reads the relations of a monitor before they are rewritten.
func (s *MonitorService) snapshotLinks(ctx context.Context, tx *gorm.DB, id uint) (MonitorLinks, error) {
	if s.emit == nil {
		return EmptyMonitorLinks(), nil
	}
	return s.emit.SnapshotMonitorLinks(ctx, tx, id)
}

// publishMonitor records the monitor and the relations it now has, compared with
// the state captured before the write. Both writes happen inside the caller's
// transaction, so the outbox can never drift from the data.
func (s *MonitorService) publishMonitor(ctx context.Context, tx *gorm.DB, id uint, before MonitorLinks) error {
	if s.emit == nil {
		return nil
	}
	if err := s.emit.EmitMonitor(ctx, tx, id); err != nil {
		return err
	}
	return s.emit.PublishMonitorLinks(ctx, tx, id, before)
}

// monitorUUID reads the global identity of a monitor (needed before a delete).
func (s *MonitorService) monitorUUID(ctx context.Context, tx *gorm.DB, id uint) (string, error) {
	if s.emit == nil {
		return "", nil
	}
	return s.emit.RowUUID(ctx, tx, models.EntityMonitor, id)
}

// tombstoneMonitor publishes the removal of a monitor and of its relations. The
// relations go first: the receiver deletes the monitor with its cascade, so the
// relation tombstones would otherwise refer to rows that are already gone.
func (s *MonitorService) tombstoneMonitor(ctx context.Context, tx *gorm.DB, uuid string, links MonitorLinks) error {
	if s.emit == nil || uuid == "" {
		return nil
	}
	if err := s.emit.TombstoneMonitorLinks(ctx, tx, uuid, links); err != nil {
		return err
	}
	return s.emit.EmitDelete(ctx, tx, models.EntityMonitor, uuid)
}

// NewMonitorService builds the monitor service.
func NewMonitorService(db *gorm.DB, cfg *config.Config, log *slog.Logger, stats *StatsService) *MonitorService {
	return &MonitorService{db: db, cfg: cfg, log: log, stats: stats}
}

// SetPublisher injects the real time publisher.
func (s *MonitorService) SetPublisher(p EventPublisher) { s.hub = p }

// SetVoteProvider injects the cluster vote provider.
func (s *MonitorService) SetVoteProvider(v VoteProvider) { s.votes = v }

// SetSettingService injects the settings service, which owns the global uptime
// window applied by the decoration (Admin > Settings).
func (s *MonitorService) SetSettingService(settings *SettingService) { s.settings = settings }

// uptimeWindowHours is the global uptime window.
func (s *MonitorService) uptimeWindowHours() int {
	if s.settings != nil {
		return s.settings.UptimeWindowHours()
	}
	return models.DefaultUptimeWindowHours
}

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
// uptime over the configured window, latency of the last check, per node votes
// and notification ids.
func (s *MonitorService) Decorate(ctx context.Context, monitors []*models.Monitor) error {
	return s.DecorateWithWindow(ctx, monitors, s.uptimeWindowHours())
}

// DecorateWithWindow is Decorate with an explicit uptime window: the public
// status pages override the global one per page.
func (s *MonitorService) DecorateWithWindow(ctx context.Context, monitors []*models.Monitor, windowHours int) error {
	if len(monitors) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(monitors))
	for _, m := range monitors {
		ids = append(ids, m.ID)
	}

	windowHours = models.NormalizeUptimeWindowHours(windowHours)
	histories, err := s.stats.History(ctx, ids, windowHours)
	if err != nil {
		return ErrInternal(err)
	}
	bars, err := s.stats.RecentBars(ctx, ids, windowHours, HeartbeatBarSlots)
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

	domains, err := s.domainsFor(ctx, ids)
	if err != nil {
		return err
	}

	templates, err := s.templateNamesFor(ctx, monitors)
	if err != nil {
		return err
	}

	for _, m := range monitors {
		// The window is reported even without a heartbeat in it: the UI labels
		// the period it is showing instead of pretending it is 24 h.
		m.UptimeHours = windowHours
		if history, ok := histories[m.ID]; ok {
			m.Uptime = history.Window.Uptime
			m.Uptime24h = history.Uptime24h
			m.Uptime7d = history.Uptime7d
			m.Uptime30d = history.Uptime30d
		}
		if len(bars[m.ID]) > 0 {
			m.HeartbeatBars = bars[m.ID]
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
		if m.DomainWatch {
			if info, ok := domains[m.ID]; ok {
				m.Domain = info
			}
		}
		if name, ok := templates[m.TemplateUUID]; ok {
			m.TemplateName = name
		}
	}
	return nil
}

// templateNamesFor maps the template uuid of a monitor to its name (one query for
// a whole listing).
func (s *MonitorService) templateNamesFor(ctx context.Context, monitors []*models.Monitor) (map[string]string, error) {
	out := map[string]string{}
	uuids := make([]string, 0, len(monitors))
	for _, monitor := range monitors {
		if uuid := strings.TrimSpace(monitor.TemplateUUID); uuid != "" {
			uuids = append(uuids, uuid)
		}
	}
	if len(uuids) == 0 {
		return out, nil
	}
	var rows []struct {
		UUID string
		Name string
	}
	if err := s.db.WithContext(ctx).Model(&models.MonitorTemplate{}).
		Select("uuid", "name").Where("uuid IN ?", uuids).Scan(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for _, row := range rows {
		out[row.UUID] = row.Name
	}
	return out, nil
}

// domainsFor returns the stored domain expiration of every monitor that watches
// one (one query for a whole listing).
func (s *MonitorService) domainsFor(ctx context.Context, monitorIDs []uint) (map[uint]*models.DomainInfo, error) {
	out := map[uint]*models.DomainInfo{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var rows []models.MonitorDomain
	if err := s.db.WithContext(ctx).Where("monitor_id IN ?", monitorIDs).Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for i := range rows {
		out[rows[i].MonitorID] = rows[i].Info()
	}
	return out, nil
}

// ExpiryWatchers returns the active monitors that watch a certificate or a
// domain. It is the worklist of the daily expiry job: the targets are then
// normalized and deduplicated, so several monitors on the same endpoint or the
// same registrable domain produce a single lookup.
func (s *MonitorService) ExpiryWatchers(ctx context.Context) ([]*models.Monitor, error) {
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).
		Where("active = ? AND (cert_watch = ? OR domain_watch = ?)", true, true, true).
		Order("id ASC").
		Find(&monitors).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the expiry watchers: %w", err))
	}
	return monitors, nil
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
