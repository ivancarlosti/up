package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/lighthouse"
	"github.com/ivancarlosti/up/internal/models"
)

// firstRunDebounce folds the several saves of one form edit into a single API
// call: enabling the switch, then fixing the target, is one run, not three.
const firstRunDebounce = 2 * time.Second

// lighthouseLimiterKey is the pacing slot of the outbound calls. PageSpeed
// Insights is a single endpoint, so one slot is enough (the limiter stays keyed
// so a future second endpoint never waits behind this one).
const lighthouseLimiterKey = "pagespeed-insights"

// Settings returns the current job configuration.
func (s *LighthouseService) Settings() models.LighthouseSettings {
	return s.settings.LighthouseSettings()
}

// NextRun reports the next instant of the DEFAULT cadence (the job setting), for
// the admin page. A monitor that carries a cadence of its own is reported
// per monitor, in the listing.
func (s *LighthouseService) NextRun(now time.Time) time.Time {
	settings := s.Settings()
	return settings.Schedule().NextRun(now, settings.Interval)
}

// RetentionDays resolves the retention policy: the shared setting wins,
// LIGHTHOUSE_RETENTION_DAYS is the fallback of a deployment that never opened
// Admin > Settings, and the documented default comes last. 0 means "never
// purge".
func (s *LighthouseService) RetentionDays() int {
	if s.settings != nil {
		return s.settings.LighthouseRetentionDays()
	}
	if s.cfg != nil && s.cfg.LighthouseRetentionDays > 0 {
		return s.cfg.LighthouseRetentionDays
	}
	return models.DefaultLighthouseRetentionDays
}

// Purge applies the retention policy to the run history and returns how many
// rows it deleted. It is cheap and idempotent when there is nothing to do.
func (s *LighthouseService) Purge(ctx context.Context) (int64, error) {
	days := s.RetentionDays()
	if days <= 0 {
		return 0, nil
	}
	return s.PurgeBefore(ctx, time.Now().UTC().AddDate(0, 0, -days))
}

// PurgeBefore deletes the runs older than the cut-off (the explicit purge of the
// admin page uses the same door).
func (s *LighthouseService) PurgeBefore(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("ran_at < ?", before).Delete(&models.LighthouseRun{})
	if result.Error != nil {
		return 0, ErrInternal(result.Error)
	}
	return result.RowsAffected, nil
}

// LighthouseStats summarises what the two tables hold: it is what the web
// performance page shows next to the retention policy.
type LighthouseStats struct {
	// Measuring is how many monitors have a stored observation.
	Measuring int64 `json:"measuring"`
	// Runs is how many history rows exist.
	Runs   int64      `json:"runs"`
	Oldest *time.Time `json:"oldest,omitempty"`
	Newest *time.Time `json:"newest,omitempty"`
}

// Stats reads the counters of the page header.
func (s *LighthouseService) Stats(ctx context.Context) (LighthouseStats, error) {
	var out LighthouseStats
	// COUNT(DISTINCT monitor_id): a monitor that measures both runs holds two
	// rows in the current-observation table but is ONE monitor measured.
	if err := s.db.WithContext(ctx).Model(&models.MonitorLighthouse{}).
		Distinct("monitor_id").Count(&out.Measuring).Error; err != nil {
		return out, ErrInternal(err)
	}
	if err := s.db.WithContext(ctx).Model(&models.LighthouseRun{}).Count(&out.Runs).Error; err != nil {
		return out, ErrInternal(err)
	}
	if out.Runs == 0 {
		return out, nil
	}
	var bounds struct {
		Oldest *time.Time
		Newest *time.Time
	}
	if err := s.db.WithContext(ctx).Model(&models.LighthouseRun{}).
		Select("MIN(ran_at) AS oldest, MAX(ran_at) AS newest").
		Scan(&bounds).Error; err != nil {
		return out, ErrInternal(err)
	}
	out.Oldest, out.Newest = bounds.Oldest, bounds.Newest
	return out, nil
}

// Monitors returns every monitor that measures its page, decorated like the
// monitors table (status, group, tags) and carrying its last observations in
// Monitor.Lighthouse (one run) and Monitor.Lighthouses (keyed by strategy). It is
// the list the web performance page renders.
//
// The cadence of each monitor and the instant of its next measurement are filled
// in here: the listing is the only place that knows both the job clock and the
// last run of every monitor, so the page never has to guess either of them.
func (s *LighthouseService) Monitors(ctx context.Context) ([]*models.Monitor, error) {
	measurers, err := s.monitors.LighthouseMeasurers(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.monitors.DecorateList(ctx, measurers); err != nil {
		return nil, err
	}
	settings := s.Settings()
	schedule := settings.Schedule()
	for _, monitor := range measurers {
		// A monitor may hold one observation per run ("both"): each of them is
		// projected on the clock of the job (an empty map is a monitor that was
		// never measured, which has no cadence to project yet).
		for _, result := range monitor.Lighthouses {
			result.Interval = lighthouse.IntervalFor(monitor, schedule.Interval)
			if settings.Enabled {
				next := schedule.NextRun(result.RanAt, result.Interval)
				result.NextRun = &next
			}
		}
	}
	return measurers, nil
}

// RunHistory returns the runs of one monitor, oldest first (the order a chart
// wants). A limit of zero returns the whole stored history.
func (s *LighthouseService) RunHistory(ctx context.Context, monitorID uint, limit int) ([]models.LighthouseRun, error) {
	return s.History(ctx, monitorID, limit)
}

// Enqueue asks for one asynchronous run of a monitor.
//
// It is what turning the switch on (or changing the page or the strategy) does:
// the operator sees a score within seconds instead of waiting for the daily
// instant. The several saves of one edit are folded by a debounce and the calls
// are paced by the shared limiter, so a bulk edit never bursts the API.
//
// It is a no-op until the service is fully wired (settings and monitors): a
// service that only holds the storage never opens a socket on its own.
func (s *LighthouseService) Enqueue(monitorID uint) {
	if s == nil || monitorID == 0 || s.monitors == nil || s.settings == nil {
		return
	}
	s.queueMu.Lock()
	if s.queued == nil {
		s.queued = map[uint]bool{}
	}
	if s.queued[monitorID] {
		s.queueMu.Unlock()
		return
	}
	s.queued[monitorID] = true
	s.queueMu.Unlock()

	go func() {
		defer func() {
			s.queueMu.Lock()
			delete(s.queued, monitorID)
			s.queueMu.Unlock()
		}()
		timer := time.NewTimer(firstRunDebounce)
		defer timer.Stop()
		<-timer.C
		settings := s.Settings()
		ctx, cancel := context.WithTimeout(context.Background(),
			time.Duration(settings.TimeoutSeconds)*time.Second+30*time.Second)
		defer cancel()
		if err := s.Refresh(ctx, monitorID); err != nil {
			s.log.Warn("lighthouse: the first run of a monitor failed",
				"monitor_id", monitorID, "error", err)
		}
	}()
}

// EnqueueAll asks for the first run of a whole listing (a bulk creation, a
// template link), still paced by the limiter.
func (s *LighthouseService) EnqueueAll(monitorIDs []uint) {
	for _, id := range monitorIDs {
		s.Enqueue(id)
	}
}

// Refresh measures one monitor now: the per-monitor "run now" of the admin page
// and the body of Enqueue.
func (s *LighthouseService) Refresh(ctx context.Context, monitorID uint) error {
	if s.monitors == nil {
		return ErrInternal(fmt.Errorf("the lighthouse service is not wired to the monitors"))
	}
	monitor, err := s.monitors.Get(ctx, monitorID)
	if err != nil {
		return err
	}
	if monitor == nil {
		return ErrNotFound(i18n.CodeNotFound, "monitor not found")
	}
	settings := s.Settings()
	targets := lighthouse.TargetsFor(monitor, models.DefaultLighthouseStrategy)
	if len(targets) == 0 {
		return ErrBadRequest(i18n.CodeValidation,
			"this monitor does not measure a page with Lighthouse")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	// One monitor may describe two runs ("both"): each of them is measured and
	// stored under its own strategy.
	for _, target := range targets {
		s.refreshTarget(ctx, target, []*models.Monitor{monitor}, settings)
	}
	return nil
}

// RunDue measures every monitor whose cadence has elapsed. It is called from the
// maintenance ticker, which ticks every minute.
//
// The due-ness of a monitor is derived from the instant of its last stored run
// (its cadence and the clock of the job say when the next one is), so there is no
// counter to keep in sync and nothing is lost by a restart. The cluster lock
// claims the HOUR slot and is only taken when there really is work: without it
// every node of a cluster would call PageSpeed Insights for the same page.
func (s *LighthouseService) RunDue(ctx context.Context, now time.Time) error {
	settings := s.Settings()
	if !settings.Enabled {
		return nil
	}
	// A run can outlast a ticker tick: the limiter paces one API call a minute,
	// so a large worklist takes many of them. The tick never queues behind the
	// run of the previous one, it simply waits for the next tick (the due
	// monitors it did not measure are still due then).
	if !s.runMu.TryLock() {
		s.log.Debug("lighthouse job: a run is already in progress")
		return nil
	}
	defer s.runMu.Unlock()

	watchers, err := s.monitors.LighthouseWatchers(ctx)
	if err != nil {
		return err
	}
	due, err := s.dueMonitors(ctx, now, watchers, settings)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		s.log.Debug("lighthouse job: nothing is due",
			"monitors", len(watchers),
			"next_run", s.NextRun(now).Format(time.RFC3339))
		return nil
	}
	if s.cluster != nil {
		slot := models.LighthouseSlot(now)
		allowed, err := s.cluster.ClaimDailyJob(ctx, "lighthouse_run", slot)
		if err != nil {
			return err
		}
		if !allowed {
			s.log.Debug("lighthouse job skipped: another node owns this slot", "slot", slot)
			return nil
		}
	}
	s.log.Info("lighthouse job started",
		"due", len(due), "monitors", len(watchers),
		"check_time", settings.CheckTime, "timezone", settings.CheckTimezone,
		"interval", settings.Interval)
	if err := s.runTargets(ctx, due, settings); err != nil {
		return err
	}
	// The day bucket is kept as information (the admin page reports the last run
	// day); it no longer decides whether the job runs.
	if err := s.settings.SetLighthouseLastRunDay(ctx, now.UTC().Unix()/86400); err != nil {
		s.log.Warn("could not record the lighthouse run day", "error", err)
	}
	return nil
}

// dueMonitors filters a worklist down to the monitors whose own cadence has
// elapsed at `now`. The last runs are read with one query for the whole listing.
func (s *LighthouseService) dueMonitors(ctx context.Context, now time.Time, watchers []*models.Monitor, settings models.LighthouseSettings) ([]*models.Monitor, error) {
	if len(watchers) == 0 {
		return nil, nil
	}
	ids := make([]uint, 0, len(watchers))
	for _, monitor := range watchers {
		ids = append(ids, monitor.ID)
	}
	last, err := s.LastRuns(ctx, ids)
	if err != nil {
		return nil, err
	}
	schedule := settings.Schedule()
	due := make([]*models.Monitor, 0, len(watchers))
	for _, monitor := range watchers {
		interval := lighthouse.IntervalFor(monitor, schedule.Interval)
		if schedule.RunDue(now, last[monitor.ID], interval) {
			due = append(due, monitor)
		}
	}
	return due, nil
}

// RunNow measures every deduplicated target, whatever the cadence of the monitors
// says. It is the manual action of the web performance page and, unlike the
// ticker, it waits for a run in flight instead of skipping itself.
func (s *LighthouseService) RunNow(ctx context.Context) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	settings := s.Settings()
	watchers, err := s.monitors.LighthouseWatchers(ctx)
	if err != nil {
		return err
	}
	return s.runTargets(ctx, watchers, settings)
}

// runTargets performs one run over the given monitors: one API call per
// deduplicated target, fanned out to every monitor that shares it. The caller
// holds runMu.
func (s *LighthouseService) runTargets(ctx context.Context, monitors []*models.Monitor, settings models.LighthouseSettings) error {
	plan := lighthouse.BuildPlan(monitors, models.DefaultLighthouseStrategy)
	if len(plan.Targets) == 0 {
		s.log.Debug("lighthouse job: nothing to measure")
		return nil
	}
	measured := 0
	for _, target := range plan.Targets {
		measured += s.refreshTarget(ctx, target, plan.Monitors[target.Key()], settings)
	}
	s.log.Info("lighthouse job finished",
		"targets", len(plan.Targets), "monitors", measured,
		"rate_limit_ms", settings.RateLimitMS)
	return nil
}

// refreshTarget calls the API once for a target and fans the observation out to
// every monitor that shares it. It returns how many monitors were updated.
//
// A failed call is data, not a job error: every monitor of the target records a
// "status: error" observation with the message, exactly like a certificate that
// could not be read.
func (s *LighthouseService) refreshTarget(ctx context.Context, target lighthouse.Target, monitors []*models.Monitor, settings models.LighthouseSettings) int {
	if len(monitors) == 0 {
		return 0
	}
	if err := s.limiter.Wait(ctx, lighthouseLimiterKey, time.Duration(settings.RateLimitMS)*time.Millisecond); err != nil {
		return 0 // the context is gone: the run stops here
	}
	runner := lighthouse.NewRunner(settings.APIKey, time.Duration(settings.TimeoutSeconds)*time.Second)
	result, runErr := runner.Run(ctx, target)
	if runErr != nil {
		result = failedResult(runErr, target)
		s.log.Warn("lighthouse job: the page could not be measured",
			"target", target.URL, "strategy", target.Strategy, "error", runErr)
	}
	result.RanAt = time.Now().UTC()
	result.RanByNode = s.cfg.NodeID
	for _, monitor := range monitors {
		if err := s.Record(ctx, monitor.ID, result); err != nil {
			s.log.Error("lighthouse job: could not store the result",
				"monitor_id", monitor.ID, "error", err)
			continue
		}
		s.publish("monitor.lighthouse", map[string]any{
			"monitor_id":  monitor.ID,
			"status":      result.Status,
			"strategy":    target.Strategy,
			"performance": result.Performance,
			"ran_at":      result.RanAt,
		})
	}
	return len(monitors)
}

// failedResult renders an API failure into the observation stored for a monitor.
func failedResult(err error, target lighthouse.Target) *models.LighthouseResult {
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 500 {
		message = message[:500]
	}
	return &models.LighthouseResult{
		Status:   models.LighthouseStatusError,
		Message:  message,
		Strategy: target.Strategy,
	}
}
