package services

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/lighthouse"
	"github.com/ivancarlosti/up/internal/models"
)

// LighthouseService owns the web performance measurements: what the daily job
// stored for each monitor, the history the admin page charts, and the job
// itself (see lighthouse_run.go).
//
// The storage half mirrors the certificate service on purpose: one row per
// monitor holds the last observation (so a listing decorates itself with a
// single query) and a second table holds the history, which is the only one
// that grows and therefore the only one the retention purges.
type LighthouseService struct {
	db       *gorm.DB
	cfg      *config.Config
	log      *slog.Logger
	hub      EventPublisher
	settings *SettingService
	monitors *MonitorService
	cluster  *ClusterService
	limiter  *lighthouse.Limiter
	// runMu serialises the runs: the ticker, the global run-now and a per-monitor
	// run-now could otherwise overlap and spend the quota twice.
	//
	// The ticker takes it with TryLock (a tick never queues behind the run of the
	// previous one) while the manual actions wait for it (the operator asked for
	// that run).
	runMu sync.Mutex
	// queueMu guards queued, the monitors whose first run is already waiting on
	// the debounce (see Enqueue).
	queueMu sync.Mutex
	queued  map[uint]bool
}

// NewLighthouseService builds the web performance service.
func NewLighthouseService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *LighthouseService {
	return &LighthouseService{
		db:      db,
		cfg:     cfg,
		log:     log,
		limiter: lighthouse.NewLimiter(),
	}
}

// SetPublisher injects the real time publisher.
func (s *LighthouseService) SetPublisher(p EventPublisher) { s.hub = p }

// SetSettingService injects the shared settings (the job configuration lives
// there, like the expiry one).
func (s *LighthouseService) SetSettingService(settings *SettingService) { s.settings = settings }

// SetMonitorService injects the monitor service (the worklist is built from the
// monitors that measure their page).
func (s *LighthouseService) SetMonitorService(m *MonitorService) { s.monitors = m }

// SetClusterService injects the service that elects the daily runner, so a
// cluster calls PageSpeed Insights once a day and not once per node.
func (s *LighthouseService) SetClusterService(c *ClusterService) { s.cluster = c }

func (s *LighthouseService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// Record stores one observation of a monitor: the current row of its run is
// upserted and a history row is appended.
//
// The history is appended on every run (a run that repeats yesterday's score is
// still a point of the trend), while the current row is overwritten, so the
// table of the listing never grows. A monitor that measures both runs holds one
// current row PER run: the row is identified by the monitor and the strategy,
// which is why a monitor switched to "both" keeps both of its scores.
func (s *LighthouseService) Record(ctx context.Context, monitorID uint, result *models.LighthouseResult) error {
	if result == nil || monitorID == 0 {
		return nil
	}
	if result.RanAt.IsZero() {
		result.RanAt = time.Now().UTC()
	}
	row := models.MonitorLighthouse{
		MonitorID:              monitorID,
		Strategy:               models.NormalizeLighthouseStrategy(result.Strategy),
		Status:                 result.Status,
		Message:                result.Message,
		FinalURL:               trimmed(result.FinalURL, 500),
		RanAt:                  result.RanAt,
		FetchTime:              result.FetchTime,
		RanByNode:              result.RanByNode,
		LighthouseMeasurements: result.LighthouseMeasurements,
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "monitor_id"}, {Name: "strategy"}},
		DoUpdates: clause.AssignmentColumns(lighthouseMeasurementColumns()),
	}).Create(&row).Error; err != nil {
		return ErrInternal(fmt.Errorf("storing the lighthouse result of monitor %d: %w", monitorID, err))
	}

	run := models.LighthouseRun{
		MonitorID:              monitorID,
		RanAt:                  result.RanAt,
		Strategy:               result.Strategy,
		Status:                 result.Status,
		Message:                result.Message,
		RanByNode:              result.RanByNode,
		LighthouseMeasurements: result.LighthouseMeasurements,
	}
	if err := s.db.WithContext(ctx).Create(&run).Error; err != nil {
		return ErrInternal(fmt.Errorf("storing the lighthouse run of monitor %d: %w", monitorID, err))
	}
	return nil
}

// lighthouseMeasurementColumns lists the columns a new observation overwrites.
// They are spelled out (instead of letting GORM update every column) so the row
// identity and the creation columns are never rewritten by an upsert: the
// strategy is absent because it is part of the identity (the row of a run).
func lighthouseMeasurementColumns() []string {
	return []string{
		"status", "message", "final_url", "ran_at", "fetch_time", "ran_by_node",
		"performance", "accessibility", "best_practices", "seo",
		"first_contentful_paint_ms", "largest_contentful_paint_ms", "total_blocking_time_ms",
		"cumulative_layout_shift", "speed_index_ms",
	}
}

// Get returns the stored observations of a monitor keyed by strategy: one entry
// for mobile or desktop, two for a monitor that measures both runs, none for a
// monitor that was never measured.
func (s *LighthouseService) Get(ctx context.Context, monitorID uint) (map[string]*models.MonitorLighthouse, error) {
	rows, err := s.rowsFor(ctx, []uint{monitorID})
	if err != nil {
		return nil, err
	}
	out := rows[monitorID]
	if out == nil {
		out = map[string]*models.MonitorLighthouse{}
	}
	return out, nil
}

// ByMonitorIDs returns the stored observations of each monitor keyed by strategy:
// it is what decorates a listing, with one query for the whole page.
func (s *LighthouseService) ByMonitorIDs(ctx context.Context, monitorIDs []uint) (map[uint]map[string]*models.LighthouseResult, error) {
	rows, err := s.rowsFor(ctx, monitorIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]map[string]*models.LighthouseResult, len(rows))
	for monitorID, byStrategy := range rows {
		results := make(map[string]*models.LighthouseResult, len(byStrategy))
		for strategy, row := range byStrategy {
			results[strategy] = row.Info()
		}
		out[monitorID] = results
	}
	return out, nil
}

// rowsFor reads the current observation of every run of the given monitors, with
// one query, keyed by monitor and then by strategy.
func (s *LighthouseService) rowsFor(ctx context.Context, monitorIDs []uint) (map[uint]map[string]*models.MonitorLighthouse, error) {
	out := map[uint]map[string]*models.MonitorLighthouse{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var rows []models.MonitorLighthouse
	if err := s.db.WithContext(ctx).Where("monitor_id IN ?", monitorIDs).Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for i := range rows {
		row := &rows[i]
		if out[row.MonitorID] == nil {
			out[row.MonitorID] = map[string]*models.MonitorLighthouse{}
		}
		out[row.MonitorID][row.Strategy] = row
	}
	return out, nil
}

// LastRuns returns when each monitor was measured for the last time (an absent
// entry is a monitor that has never run), with one query for a whole listing.
//
// It is the only state the cadence needs: the schedule is derived from it, so
// nothing has to be written per tick and a restart (or a second node) cannot
// lose a slot. A monitor that measures both runs holds a row per run and the
// newest one wins: the pair is due together.
func (s *LighthouseService) LastRuns(ctx context.Context, monitorIDs []uint) (map[uint]time.Time, error) {
	out := map[uint]time.Time{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	var rows []models.MonitorLighthouse
	if err := s.db.WithContext(ctx).
		Select("monitor_id", "ran_at").
		Where("monitor_id IN ?", monitorIDs).
		Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for i := range rows {
		last, seen := out[rows[i].MonitorID]
		if !seen || rows[i].RanAt.After(last) {
			out[rows[i].MonitorID] = rows[i].RanAt
		}
	}
	return out, nil
}

// History returns the newest runs of a monitor, oldest first (the order a chart
// wants). A limit of zero returns the whole stored history.
func (s *LighthouseService) History(ctx context.Context, monitorID uint, limit int) ([]models.LighthouseRun, error) {
	query := s.db.WithContext(ctx).Where("monitor_id = ?", monitorID).Order("ran_at ASC, id ASC")
	if limit > 0 {
		query = s.db.WithContext(ctx).Where("monitor_id = ?", monitorID).
			Order("ran_at DESC, id DESC").Limit(limit)
	}
	var rows []models.LighthouseRun
	if err := query.Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	if limit > 0 {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	return rows, nil
}

// Clear forgets the observation of the given monitors, which is what turning the
// switch off does: a monitor that no longer measures must not keep showing a
// score. The history is kept (the retention owns it).
func (s *LighthouseService) Clear(ctx context.Context, monitorIDs []uint) error {
	if len(monitorIDs) == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).
		Where("monitor_id IN ?", monitorIDs).
		Delete(&models.MonitorLighthouse{}).Error; err != nil {
		return ErrInternal(err)
	}
	return nil
}

// ClearExcept forgets the observations of a monitor whose run is not one of keep:
// it is what narrowing the strategy of a monitor does ("both" to "mobile" must
// not leave yesterday's desktop score on the monitor forever). The history of
// every run is kept, like Clear.
func (s *LighthouseService) ClearExcept(ctx context.Context, monitorID uint, keep []string) error {
	if monitorID == 0 {
		return nil
	}
	if len(keep) == 0 {
		return s.Clear(ctx, []uint{monitorID})
	}
	if err := s.db.WithContext(ctx).
		Where("monitor_id = ? AND strategy NOT IN ?", monitorID, keep).
		Delete(&models.MonitorLighthouse{}).Error; err != nil {
		return ErrInternal(err)
	}
	return nil
}
