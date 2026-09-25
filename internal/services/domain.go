package services

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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

// ---------------------------------------------------------------------------
// Manual expiration dates
// ---------------------------------------------------------------------------

// manualEntry is one observation a reconciliation pass must write.
type manualEntry struct {
	MonitorID uint
	Info      *models.DomainInfo
}

// manualPlan is the work of one reconciliation pass: the observations to write
// and the manual observations that no longer belong to a dated domain.
type manualPlan struct {
	writes []manualEntry
	drops  []uint
}

// planManualDates turns the monitors and the stored manual observations into the
// writes and drops of a pass. It is pure on purpose: the boot pass, the daily
// pass and the monitor write path all describe the same truth with it.
//
// The rules:
//
//   - the date of a domain is the newest manual date its monitors carry (the
//     write path mirrors them so they agree, and the newest one is the
//     operator's last word when two rows still disagree);
//   - only a monitor that WATCHES the domain stores an observation (the switch
//     decides what is reported, the column decides what is remembered);
//   - an observation is current when it already carries the manual date with the
//     right remaining validity, so a no-op pass writes nothing while the daily
//     ageing still refreshes the days left.
func planManualDates(monitors []*models.Monitor, stored []models.MonitorDomain, now time.Time, only string) manualPlan {
	dates := map[string]time.Time{}
	domainOf := map[uint]string{}
	watchers := map[string][]uint{}
	for _, monitor := range monitors {
		if monitor == nil {
			continue
		}
		domain := expiry.DomainName(monitor)
		if domain == "" {
			continue
		}
		domainOf[monitor.ID] = domain
		if date := validDate(monitor.DomainExpiresAt); date != nil {
			if current, ok := dates[domain]; !ok || date.After(current) {
				dates[domain] = *date
			}
		}
		if monitor.DomainWatch {
			watchers[domain] = append(watchers[domain], monitor.ID)
		}
	}

	plan := manualPlan{}
	current := map[uint]bool{}
	for i := range stored {
		row := &stored[i]
		domain, known := domainOf[row.MonitorID]
		date, dated := dates[domain]
		if !known || !dated {
			// The monitor is gone, changed target or lost its date: the stored
			// manual observation is an orphan.
			plan.drops = append(plan.drops, row.MonitorID)
			continue
		}
		if manualRowCurrent(row, date, now) {
			current[row.MonitorID] = true
		}
		// A stale row needs no drop: the write below overwrites it in place.
	}

	for domain, date := range dates {
		if only != "" && domain != only {
			continue
		}
		info := models.ManualDomainInfo(domain, &date, now)
		if info == nil {
			continue
		}
		for _, monitorID := range watchers[domain] {
			if current[monitorID] {
				continue
			}
			plan.writes = append(plan.writes, manualEntry{MonitorID: monitorID, Info: info})
		}
	}
	// Map iteration is random: ordering the work keeps the logs, the tests and
	// the sequence of the writes deterministic.
	sort.Slice(plan.writes, func(i, j int) bool { return plan.writes[i].MonitorID < plan.writes[j].MonitorID })
	sort.Slice(plan.drops, func(i, j int) bool { return plan.drops[i] < plan.drops[j] })
	return plan
}

// manualRowCurrent reports whether a stored observation already holds the manual
// date of its domain. The remaining validity is part of the comparison, which is
// what ages the date the pass applied yesterday.
func manualRowCurrent(row *models.MonitorDomain, date time.Time, now time.Time) bool {
	if row == nil {
		return false
	}
	if row.Source != string(models.DomainSourceManual) || row.Status != string(models.DomainStatusOK) {
		return false
	}
	if !row.ExpiresAt.UTC().Equal(date.UTC()) {
		return false
	}
	return row.DaysLeft == models.DaysLeft(date, now)
}

// ApplyManual materialises the manual expiration date of ONE domain into the
// stored observation of every monitor that watches it, and drops the manual
// observations of a date that was cleared. It never opens a socket.
//
// It is what a monitor save calls: the operator typed the truth for a TLD that
// publishes no date, so waiting for the next daily run used to leave the monitor
// showing a stale "no parser" (unsupported) status for up to a day.
func (s *DomainService) ApplyManual(ctx context.Context, domain string) (int, error) {
	return s.applyManual(ctx, time.Now().UTC(), expiry.NormalizeDomain(domain))
}

// applyManual reconciles the manual dates with what is stored. An empty `only`
// examines every domain (the boot and daily pass), otherwise the pass is
// restricted to one domain (the write path).
//
// The rule itself lives in planManualDates, which is pure: the boot pass, the
// daily pass and the write path cannot disagree about what the operator's date
// means.
func (s *DomainService) applyManual(ctx context.Context, now time.Time, only string) (int, error) {
	monitors, err := s.domainDateMonitors(ctx)
	if err != nil {
		return 0, err
	}
	stored, err := s.manualObservations(ctx, only)
	if err != nil {
		return 0, err
	}
	plan := planManualDates(monitors, stored, now, only)

	applied := 0
	for _, entry := range plan.writes {
		// No node performed a lookup for a date the operator typed, which is why
		// the observation carries an empty checked_by_node.
		if _, err := s.Record(ctx, entry.MonitorID, "", entry.Info); err != nil {
			return applied, err
		}
		applied++
	}
	for _, monitorID := range plan.drops {
		if err := s.db.WithContext(ctx).
			Where("monitor_id = ? AND source = ?", monitorID, models.DomainSourceManual).
			Delete(&models.MonitorDomain{}).Error; err != nil {
			return applied, ErrInternal(fmt.Errorf("dropping the manual domain of monitor %d: %w", monitorID, err))
		}
	}
	if applied > 0 || len(plan.drops) > 0 {
		s.log.Info("manual domain expiration applied",
			"domain", only, "monitors", applied, "cleared", len(plan.drops))
	}
	return applied, nil
}

// domainDateMonitors returns the monitors that watch their domain or carry a
// manual date: the scope of a reconciliation pass.
//
// A manual date is read from EVERY monitor of the domain (the switch decides
// what is looked up and notified, never what is remembered), which is why the
// query cannot narrow the result further.
func (s *DomainService) domainDateMonitors(ctx context.Context) ([]*models.Monitor, error) {
	var monitors []*models.Monitor
	if err := s.db.WithContext(ctx).
		Select("id", "type", "config", "domain_watch", "domain_expires_at").
		Where("domain_watch = ? OR domain_expires_at IS NOT NULL", true).
		Order("id ASC").
		Find(&monitors).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the monitors that carry a domain date: %w", err))
	}
	return monitors, nil
}

// manualObservations returns the stored observations whose source is the manual
// date (optionally restricted to one domain).
func (s *DomainService) manualObservations(ctx context.Context, only string) ([]models.MonitorDomain, error) {
	query := s.db.WithContext(ctx).Where("source = ?", models.DomainSourceManual)
	if only != "" {
		query = query.Where("domain = ?", only)
	}
	var rows []models.MonitorDomain
	if err := query.Order("monitor_id ASC").Find(&rows).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the manual domain observations: %w", err))
	}
	return rows, nil
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
