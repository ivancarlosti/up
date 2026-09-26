package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/expiry"
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
	// domains materialises a manual domain expiration date into the stored
	// observation of the whole domain. It is optional: a nil value just leaves the
	// date to the daily job.
	domains *DomainService
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
	return s.decorate(ctx, monitors, s.uptimeWindowHours(), true)
}

// DecorateWithWindow is Decorate with an explicit uptime window: the public
// status pages override the global one per page.
func (s *MonitorService) DecorateWithWindow(ctx context.Context, monitors []*models.Monitor, windowHours int) error {
	return s.decorate(ctx, monitors, windowHours, true)
}

// DecorateList decorates a whole listing without the per node votes.
//
// The votes are the bulk of the payload and the monitor detail is the only view
// that renders them, so the listing endpoints (the dashboard, the monitors table
// and the public API) ask for the aggregated status only. Everything else is
// identical to Decorate, which keeps a row of the table and a card of the
// dashboard showing the same values.
func (s *MonitorService) DecorateList(ctx context.Context, monitors []*models.Monitor) error {
	return s.decorate(ctx, monitors, s.uptimeWindowHours(), false)
}

// decorateSources is the runtime data a listing needs. Every field is read with a
// single query for the whole listing, never one per monitor.
type decorateSources struct {
	histories    map[uint]MonitorHistory
	bars         map[uint][]string
	states       map[uint]models.MonitorState
	votes        map[uint][]models.NodeVote
	aggregates   map[uint]models.AggregateStatus
	links        []models.MonitorNotification
	groups       map[uint][]uint
	certificates map[uint]*models.CertificateInfo
	domains      map[uint]*models.DomainInfo
	templates    map[string]string
}

// decorate fills the runtime fields of a listing.
func (s *MonitorService) decorate(ctx context.Context, monitors []*models.Monitor, windowHours int, withVotes bool) error {
	if len(monitors) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(monitors))
	for _, m := range monitors {
		ids = append(ids, m.ID)
	}
	windowHours = models.NormalizeUptimeWindowHours(windowHours)

	started := time.Now()
	sources, err := s.gatherSources(ctx, monitors, ids, windowHours, withVotes)
	if err != nil {
		return err
	}
	s.applySources(monitors, windowHours, sources)

	// The decoration is the part of a listing that grows with the stored history,
	// so its cost is logged: with LOG_LEVEL=debug it is the first thing an operator
	// looks at when the monitors table slows down.
	s.log.Debug("monitors decorated",
		"monitors", len(monitors),
		"window_hours", windowHours,
		"votes", withVotes,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return nil
}

// gatherSources runs the reads of the decoration concurrently.
//
// Each read is already a grouped query over the whole listing, but their costs add
// up: running them in parallel keeps a request close to its slowest query instead
// of the sum of all of them. The connection pool (internal/database) bounds the
// fan out, so a burst of listings back pressures instead of flooding the server.
func (s *MonitorService) gatherSources(ctx context.Context, monitors []*models.Monitor, ids []uint, windowHours int, withVotes bool) (*decorateSources, error) {
	sources := &decorateSources{}
	group, gctx := errgroup.WithContext(ctx)

	group.Go(func() error {
		histories, err := s.stats.History(gctx, ids, windowHours)
		sources.histories = histories
		return err
	})
	group.Go(func() error {
		bars, err := s.stats.RecentBars(gctx, ids, windowHours, HeartbeatBarSlots)
		sources.bars = bars
		return err
	})
	// The last check and its latency come from monitor_states: one row per monitor,
	// upserted on every check by the evaluator. It replaces the "newest heartbeat
	// per monitor" query, which had to walk the whole retained history because the
	// index on (monitor_id, created_at) cannot seek a grouped MAX(id).
	group.Go(func() error {
		states, err := s.monitorStatesFor(gctx, ids)
		sources.states = states
		return err
	})
	if s.votes != nil {
		// EvaluateAll merges the latest heartbeat of every node - itself bounded by
		// the widest vote window - into the aggregated status of each monitor.
		group.Go(func() error {
			votes, aggregates, err := s.votes.EvaluateAll(gctx, monitors)
			sources.aggregates = aggregates
			if withVotes {
				sources.votes = votes
			}
			return err
		})
	}
	group.Go(func() error {
		links, err := s.AllLinks(gctx)
		sources.links = links
		return err
	})
	group.Go(func() error {
		groups, err := s.AllGroupMembers(gctx)
		sources.groups = groups
		return err
	})
	group.Go(func() error {
		certificates, err := s.certificatesFor(gctx, ids)
		sources.certificates = certificates
		return err
	})
	group.Go(func() error {
		domains, err := s.domainsFor(gctx, ids)
		sources.domains = domains
		return err
	})
	group.Go(func() error {
		templates, err := s.templateNamesFor(gctx, monitors)
		sources.templates = templates
		return err
	})

	if err := group.Wait(); err != nil {
		return nil, ErrInternal(err)
	}
	return sources, nil
}

// monitorStatesFor reads the aggregated state of the given monitors: the current
// status, the time of the last check and its latency. The table holds one row per
// monitor, so it is the cheap source of the "latest check" columns of a listing.
func (s *MonitorService) monitorStatesFor(ctx context.Context, monitorIDs []uint) (map[uint]models.MonitorState, error) {
	out := make(map[uint]models.MonitorState, len(monitorIDs))
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var states []models.MonitorState
	if err := s.db.WithContext(ctx).Where("monitor_id IN ?", monitorIDs).Find(&states).Error; err != nil {
		return nil, fmt.Errorf("loading the monitor states: %w", err)
	}
	for _, state := range states {
		out[state.MonitorID] = state
	}
	return out, nil
}

// applySources copies the gathered data onto the monitors.
func (s *MonitorService) applySources(monitors []*models.Monitor, windowHours int, sources *decorateSources) {
	linksByMonitor := map[uint][]uint{}
	for _, link := range sources.links {
		linksByMonitor[link.MonitorID] = append(linksByMonitor[link.MonitorID], link.NotificationID)
	}

	for _, m := range monitors {
		// The window is reported even without a heartbeat in it: the UI labels
		// the period it is showing instead of pretending it is 24 h.
		m.UptimeHours = windowHours
		if history, ok := sources.histories[m.ID]; ok {
			m.Uptime = history.Window.Uptime
			m.Uptime24h = history.Uptime24h
			m.Uptime7d = history.Uptime7d
			m.Uptime30d = history.Uptime30d
		}
		if len(sources.bars[m.ID]) > 0 {
			m.HeartbeatBars = sources.bars[m.ID]
		}
		if state, ok := sources.states[m.ID]; ok {
			if state.LastCheckAt != nil {
				checked := *state.LastCheckAt
				m.LastCheckAt = &checked
			}
			m.LastLatencyMS = state.LastLatency
		}
		if status, ok := sources.aggregates[m.ID]; ok {
			m.Status = status
		}
		if m.Status == "" {
			m.Status = models.AggregateUnknown
		}
		if sources.votes != nil {
			m.Votes = sources.votes[m.ID]
		}
		if ids, ok := linksByMonitor[m.ID]; ok {
			m.NotificationIDs = ids
		} else {
			m.NotificationIDs = []uint{}
		}
		if ids, ok := sources.groups[m.ID]; ok {
			m.GroupIDs = ids
		} else {
			m.GroupIDs = []uint{}
		}
		if m.CertWatch {
			if info, ok := sources.certificates[m.ID]; ok {
				m.Certificate = info
			}
		}
		if m.DomainWatch {
			if info, ok := sources.domains[m.ID]; ok {
				m.Domain = info
			}
		}
		// The manual date is the operator's truth: it is reported as soon as it is
		// typed, even while the stored observation is still an older lookup. A
		// stale "unsupported" used to read as "no parser" for up to a day.
		if manual := manualDomainInfo(m, m.Domain); manual != nil {
			m.Domain = manual
		}
		if name, ok := sources.templates[m.TemplateUUID]; ok {
			m.TemplateName = name
		}
	}
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

// manualDomainInfo returns the observation of the manual date of a monitor (nil
// when it carries none).
//
// The domain the date belongs to comes from the stored observation when there is
// one, and from the monitor target otherwise, so the badge names the right
// registration even before the first lookup ever ran.
func manualDomainInfo(monitor *models.Monitor, stored *models.DomainInfo) *models.DomainInfo {
	if monitor == nil {
		return nil
	}
	date := validDate(monitor.DomainExpiresAt)
	if date == nil {
		return nil
	}
	domain := ""
	if stored != nil {
		domain = stored.Domain
	}
	if domain == "" {
		domain = expiry.DomainName(monitor)
	}
	return models.ManualDomainInfo(domain, date, time.Now().UTC())
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
