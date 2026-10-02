package services

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// StatsService computes uptime percentages, latency figures and heartbeat
// series. It is the single implementation used by the dashboard, the monitor
// detail page, the public status pages and the public API, so the numbers are
// always consistent.
type StatsService struct {
	db *gorm.DB
}

// NewStatsService builds the statistics service.
func NewStatsService(db *gorm.DB) *StatsService { return &StatsService{db: db} }

// storedPrecision is the fraction of a second the datetime columns hold: GORM
// creates them as datetime(3).
const storedPrecision = time.Millisecond

// queryTime floors an instant to the precision the columns store, and every bound
// of every window this package sends goes through it.
//
// It is not cosmetic. The bounds are built from time.Now(), which carries
// nanoseconds, and the driver puts them into the statement as they are:
// '2026-10-01 20:36:27.303124327'. A temporal literal with nine fractional
// digits is finer than the column type and finer than the server compares, so the
// range condition stops being usable and the plan falls back to whatever index
// covers the other column alone. On heartbeats that is the difference between
// reading a few thousand index entries and walking the whole retained history:
// the same predicate estimates 4 873 349 rows with a nanosecond bound and 32 499
// with a millisecond one.
func queryTime(t time.Time) time.Time { return t.Truncate(storedPrecision) }

// windowRow is the aggregate of one monitor, shaped like the single query the
// History used to run, so the mapping to the API payload did not have to change
// when the computation moved behind the hourly rollups.
type windowRow struct {
	MonitorID   uint
	Up24h       int64
	Total24h    int64
	Down24h     int64
	Pending24h  int64
	Up7d        int64
	Total7d     int64
	Up30d       int64
	Total30d    int64
	UpWindow    int64
	TotalWindow int64
	AvgMS24h    *float64
	MinMS24h    *int64
	MaxMS24h    *int64
}

// MonitorHistory is the aggregated history of one monitor: the configured
// window (what the UI shows) plus the fixed 24 h / 7 d / 30 d percentages the
// API has always exposed.
type MonitorHistory struct {
	// Window carries the configured period: Uptime, Up/Down/Pending/Total,
	// Hours and the latency figures.
	Window *models.UptimeStats
	// Uptime24h, Uptime7d and Uptime30d are the fixed windows of the public API.
	Uptime24h float64
	Uptime7d  float64
	Uptime30d float64
}

// historyWindowSpec is one of the periods a History call reports: how many hours
// it spans, when it starts, and the first whole hour inside it.
type historyWindowSpec struct {
	hours int
	// start is now - hours.
	start time.Time
	// leadEnd is the first hour boundary inside the window. The hour that holds
	// start is only partly covered, so the rollup of that hour cannot be used
	// as it is: the boundary heartbeats complete it instead.
	leadEnd time.Time
}

// historyWindowSet is the window selected in Admin > Settings plus the fixed
// 24 h / 7 d / 30 d windows the public API has always exposed. It also owns the
// two hour boundaries that split every window between the hourly rollups and the
// raw heartbeats:
//
//	start ....... leadEnd ............ endHour ......... now
//	|---- raw ----|----- rollups ------|------ raw ------|
//	  hour the        whole hours          hour in progress
//	  window starts
//
// The split keeps the numbers identical to those of the single raw aggregate the
// query used to run, because a rollup bucket is exactly the reduction of the
// heartbeats of its hour.
type historyWindowSet struct {
	// selected is the window requested by the caller (24/168/336/720).
	selected int
	// windows holds the distinct periods, deduplicated by hour count.
	windows []historyWindowSpec
	// indexOf maps an hour count to its position in windows.
	indexOf map[int]int
	// rollupStart is the oldest bucket a rollup query may read.
	rollupStart time.Time
	// endHour is the start of the hour in progress: the rollups only hold
	// complete hours, so this hour is read from the raw table.
	endHour time.Time
}

// newHistoryWindowSet lays out the periods of one History call.
//
// The instant is floored to the precision the columns store before anything is
// derived from it, which is what keeps every bound the boundary query binds
// usable as an index range (see queryTime). Doing it here and not at the call
// site means no caller can hand the query a nanosecond bound by accident.
func newHistoryWindowSet(now time.Time, windowHours int) historyWindowSet {
	now = queryTime(now)
	set := historyWindowSet{
		selected: windowHours,
		indexOf:  map[int]int{},
		endHour:  now.Truncate(time.Hour),
	}
	for _, hours := range []int{
		models.DefaultUptimeWindowHours,
		7 * 24,
		models.HeartbeatRollupHorizonHours,
		windowHours,
	} {
		if _, ok := set.indexOf[hours]; ok {
			continue
		}
		start := now.Add(-time.Duration(hours) * time.Hour)
		set.indexOf[hours] = len(set.windows)
		set.windows = append(set.windows, historyWindowSpec{
			hours:   hours,
			start:   start,
			leadEnd: ceilHour(start),
		})
	}
	set.rollupStart = set.windows[0].start.Truncate(time.Hour)
	for _, spec := range set.windows {
		if oldest := spec.start.Truncate(time.Hour); oldest.Before(set.rollupStart) {
			set.rollupStart = oldest
		}
	}
	return set
}

// rollupInside reports whether a rollup bucket is wholly inside the window, so
// its counters can be added as they are.
func (s historyWindowSet) rollupInside(window int, bucket time.Time) bool {
	spec := s.windows[window]
	return !bucket.Before(spec.leadEnd) && bucket.Before(s.endHour)
}

// historyRange is a half-open [start, end) time range of heartbeats plus the
// periods it completes. A zero end is the hour in progress, which no clock
// bounds.
type historyRange struct {
	start   time.Time
	end     time.Time
	windows []int
}

// serves reports whether the range feeds the period at the given index.
func (r historyRange) serves(window int) bool {
	for _, index := range r.windows {
		if index == window {
			return true
		}
	}
	return false
}

// boundaryRanges returns the raw ranges of one History call. They are disjoint
// by construction - one range per window for the hour the window starts in, plus
// the hour in progress - so a heartbeat selected by two ranges can never be
// counted twice. Every range also carries the periods it belongs to, which is
// what lets the query aggregate each range on its own: the hour in progress
// counts for every window, the partial hour a window starts in only for that
// window.
func (s historyWindowSet) boundaryRanges() []historyRange {
	all := make([]int, len(s.windows))
	for i := range all {
		all[i] = i
	}
	ranges := []historyRange{{start: s.endHour, windows: all}}
	for i, spec := range s.windows {
		end := spec.leadEnd
		if end.After(s.endHour) {
			end = s.endHour
		}
		if spec.start.Before(end) {
			ranges = append(ranges, historyRange{start: spec.start, end: end, windows: []int{i}})
		}
	}
	return ranges
}

// ceilHour rounds a timestamp up to the next whole hour, or returns it unchanged
// when it already is one.
func ceilHour(t time.Time) time.Time {
	floor := t.Truncate(time.Hour)
	if t.Equal(floor) {
		return floor
	}
	return floor.Add(time.Hour)
}

// historyTotals accumulates one period of one monitor from the rollups and the
// boundary heartbeats.
type historyTotals struct {
	up        int64
	down      int64
	pending   int64
	total     int64
	upLatSum  int64
	upLatCnt  int64
	minMS     int64
	maxMS     int64
	hasMinMax bool
}

// addRollup folds one hourly bucket into the period.
func (t *historyTotals) addRollup(row historyRollupRow) {
	t.up += row.Up
	t.down += row.Down
	t.pending += row.Pending
	t.total += row.Total
	t.upLatSum += row.UpLatencySum
	t.upLatCnt += row.UpLatencyCount
	if row.Total > 0 {
		t.observe(row.LatencyMin, row.LatencyMax)
	}
}

// observe widens the latency range of the period.
func (t *historyTotals) observe(minMS, maxMS int64) {
	if !t.hasMinMax {
		t.minMS, t.maxMS = minMS, maxMS
		t.hasMinMax = true
		return
	}
	if minMS < t.minMS {
		t.minMS = minMS
	}
	if maxMS > t.maxMS {
		t.maxMS = maxMS
	}
}

// row shapes the accumulated periods like the aggregate the rest of the function
// already consumed.
func (s historyWindowSet) row(monitorID uint, totals []historyTotals) windowRow {
	day := totals[s.indexOf[models.DefaultUptimeWindowHours]]
	week := totals[s.indexOf[7*24]]
	month := totals[s.indexOf[models.HeartbeatRollupHorizonHours]]
	chosen := totals[s.indexOf[s.selected]]

	row := windowRow{
		MonitorID:   monitorID,
		Up24h:       day.up,
		Total24h:    day.total,
		Down24h:     day.down,
		Pending24h:  day.pending,
		Up7d:        week.up,
		Total7d:     week.total,
		Up30d:       month.up,
		Total30d:    month.total,
		UpWindow:    chosen.up,
		TotalWindow: chosen.total,
	}
	if day.upLatCnt > 0 {
		// The AVG of the query this replaced came back as a DECIMAL rounded to
		// the four fractional digits MySQL gives a division
		// (div_precision_increment), and the dashboard and the public API have
		// always exposed that shape. Rounding keeps the refactor invisible.
		avg := math.Round(float64(day.upLatSum)/float64(day.upLatCnt)*10000) / 10000
		row.AvgMS24h = &avg
	}
	if day.hasMinMax {
		minMS, maxMS := day.minMS, day.maxMS
		row.MinMS24h = &minMS
		row.MaxMS24h = &maxMS
	}
	return row
}

// History returns the aggregated history of a set of monitors for the given
// window.
//
// The fixed 24 h / 7 d / 30 d percentages and the selected window are still
// computed together (one scan of the rollup table plus the boundary hours of
// every period), but the bulk of the data now comes from the hourly rollups
// instead of the heartbeats: at most 720 rollup rows per monitor for 30 days
// plus at most two hours of raw checks per period, where the single raw query
// read every heartbeat of the last 30 days (43 200 rows per monitor at a 60 s
// interval, 518 400 at 5 s, and twice that on a two node cluster).
func (s *StatsService) History(ctx context.Context, monitorIDs []uint, windowHours int) (map[uint]MonitorHistory, error) {
	out := map[uint]MonitorHistory{}
	if len(monitorIDs) == 0 {
		return out, nil
	}
	windowHours = models.NormalizeUptimeWindowHours(windowHours)
	set := newHistoryWindowSet(time.Now().UTC(), windowHours)

	rollups, err := s.historyRollups(ctx, monitorIDs, set)
	if err != nil {
		return nil, err
	}
	boundaryRanges := set.boundaryRanges()
	boundary, err := s.historyBoundaryHeartbeats(ctx, monitorIDs, boundaryRanges)
	if err != nil {
		return nil, err
	}

	totals := map[uint][]historyTotals{}
	accumulate := func(monitorID uint) []historyTotals {
		list, ok := totals[monitorID]
		if !ok {
			list = make([]historyTotals, len(set.windows))
			totals[monitorID] = list
		}
		return list
	}
	for _, bucket := range rollups {
		list := accumulate(bucket.MonitorID)
		for i := range set.windows {
			if set.rollupInside(i, bucket.BucketAt) {
				list[i].addRollup(bucket)
			}
		}
	}
	// Every boundary range arrives reduced to one row per monitor, so folding it
	// into the periods is a handful of additions instead of one per heartbeat.
	for _, aggregate := range boundary {
		if aggregate.RangeIndex < 0 || aggregate.RangeIndex >= len(boundaryRanges) {
			continue
		}
		list := accumulate(aggregate.MonitorID)
		window := boundaryRanges[aggregate.RangeIndex]
		for i := range set.windows {
			if window.serves(i) {
				list[i].addRollup(aggregate.rollup())
			}
		}
	}

	rows := make([]windowRow, 0, len(monitorIDs))
	for _, monitorID := range monitorIDs {
		list, ok := totals[monitorID]
		if !ok || list[set.indexOf[models.HeartbeatRollupHorizonHours]].total == 0 {
			// A monitor is reported only when it has a heartbeat inside the
			// widest window: the grouped query bounded its scan to 30 days, so
			// its COUNT(*) was never zero for a monitor without data in it.
			continue
		}
		rows = append(rows, set.row(monitorID, list))
	}

	for _, row := range rows {
		stats := &models.UptimeStats{
			MonitorID: row.MonitorID,
			Hours:     windowHours,
			Up:        int(row.UpWindow),
			Down:      int(row.Down24h),
			Pending:   int(row.Pending24h),
			Total:     int(row.TotalWindow),
		}
		stats.Uptime = percentage(row.UpWindow, row.TotalWindow)
		if row.AvgMS24h != nil {
			stats.AvgMS = *row.AvgMS24h
		}
		if row.MinMS24h != nil {
			stats.MinMS = *row.MinMS24h
		}
		if row.MaxMS24h != nil {
			stats.MaxMS = *row.MaxMS24h
		}
		out[row.MonitorID] = MonitorHistory{
			Window:    stats,
			Uptime24h: percentage(row.Up24h, row.Total24h),
			Uptime7d:  percentage(row.Up7d, row.Total7d),
			Uptime30d: percentage(row.Up30d, row.Total30d),
		}
	}
	return out, nil
}

// historyRollupRow is one hourly bucket read back from the rollup table.
type historyRollupRow struct {
	MonitorID      uint      `gorm:"column:monitor_id"`
	BucketAt       time.Time `gorm:"column:bucket_at"`
	Up             int64     `gorm:"column:up"`
	Down           int64     `gorm:"column:down"`
	Pending        int64     `gorm:"column:pending"`
	Maintenance    int64     `gorm:"column:maintenance"`
	Total          int64     `gorm:"column:total"`
	UpLatencySum   int64     `gorm:"column:up_latency_sum"`
	UpLatencyCount int64     `gorm:"column:up_latency_count"`
	LatencyMin     int64     `gorm:"column:latency_min"`
	LatencyMax     int64     `gorm:"column:latency_max"`
}

// historyBoundaryRow is one (range, monitor) aggregate of the boundary
// heartbeats. The counters are the ones a rollup bucket holds, so the periods
// accumulate it with addRollup; Total counts every heartbeat of the range,
// whatever its status, exactly like the COUNT(*) it replaced.
type historyBoundaryRow struct {
	// RangeIndex indexes the ranges the query was built from, which is what says
	// which periods the counters belong to.
	RangeIndex     int   `gorm:"column:range_index"`
	MonitorID      uint  `gorm:"column:monitor_id"`
	Up             int64 `gorm:"column:up"`
	Down           int64 `gorm:"column:down"`
	Pending        int64 `gorm:"column:pending"`
	Maintenance    int64 `gorm:"column:maintenance"`
	Total          int64 `gorm:"column:total"`
	UpLatencySum   int64 `gorm:"column:up_latency_sum"`
	UpLatencyCount int64 `gorm:"column:up_latency_count"`
	LatencyMin     int64 `gorm:"column:latency_min"`
	LatencyMax     int64 `gorm:"column:latency_max"`
}

// rollup shapes the aggregate as the bucket the periods accumulate.
func (r historyBoundaryRow) rollup() historyRollupRow {
	return historyRollupRow{
		MonitorID:      r.MonitorID,
		Up:             r.Up,
		Down:           r.Down,
		Pending:        r.Pending,
		Maintenance:    r.Maintenance,
		Total:          r.Total,
		UpLatencySum:   r.UpLatencySum,
		UpLatencyCount: r.UpLatencyCount,
		LatencyMin:     r.LatencyMin,
		LatencyMax:     r.LatencyMax,
	}
}

// historyRollups reads the hourly buckets the periods of the call cover.
func (s *StatsService) historyRollups(ctx context.Context, monitorIDs []uint, set historyWindowSet) ([]historyRollupRow, error) {
	var rows []historyRollupRow
	if err := s.db.WithContext(ctx).Model(&models.HeartbeatRollup{}).
		Select("monitor_id, bucket_at, up, down, pending, maintenance, total, "+
			"up_latency_sum, up_latency_count, latency_min, latency_max").
		Where("monitor_id IN ? AND bucket_at >= ? AND bucket_at < ?",
			monitorIDs, set.rollupStart, set.endHour).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("loading the heartbeat rollups: %w", err)
	}
	return rows, nil
}

// historyBoundaryAggregate is one branch of the boundary query: the counters of
// one (range, monitor) pair, shaped exactly like a rollup bucket so the periods
// can fold it with addRollup.
//
// The reduction is the one the incremental rollup upsert maintains: the status
// counters mirror its switch, the average latency only moves for an "up"
// heartbeat, and MIN/MAX cover every heartbeat of the range.
const historyBoundaryAggregate = `
	SELECT %d AS range_index, monitor_id,
	       SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END)          AS up,
	       SUM(CASE WHEN status = 0 THEN 1 ELSE 0 END)          AS down,
	       SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END)          AS pending,
	       SUM(CASE WHEN status = 3 THEN 1 ELSE 0 END)          AS maintenance,
	       COUNT(*)                                             AS total,
	       SUM(CASE WHEN status = 1 THEN latency_ms ELSE 0 END) AS up_latency_sum,
	       SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END)          AS up_latency_count,
	       COALESCE(MIN(latency_ms), 0)                         AS latency_min,
	       COALESCE(MAX(latency_ms), 0)                         AS latency_max
	FROM heartbeats
	WHERE monitor_id IN ? AND created_at >= ?`

// historyBoundaryHeartbeats reads the heartbeats of the ranges the rollups do
// not cover - the partial hour every window starts in, plus the hour in progress
// - reduced in the database to one row per (range, monitor).
//
// One aggregate per range, each with its own GROUP BY, instead of a WHERE with an
// OR over all of them: every branch is then a single bounded range on
// (monitor_id, created_at) that the server reduces before anything crosses the
// wire, so the client folds at most monitors x ranges rows instead of one row per
// heartbeat (the boundary hours of a dense installation hold tens of thousands of
// them per monitor).
func (s *StatsService) historyBoundaryHeartbeats(ctx context.Context, monitorIDs []uint, ranges []historyRange) ([]historyBoundaryRow, error) {
	branches := make([]string, 0, len(ranges))
	args := make([]any, 0, 3*len(ranges))
	for i, window := range ranges {
		branch := fmt.Sprintf(historyBoundaryAggregate, i)
		args = append(args, monitorIDs, window.start)
		if !window.end.IsZero() {
			branch += " AND created_at < ?"
			args = append(args, window.end)
		}
		branch += " GROUP BY monitor_id"
		branches = append(branches, branch)
	}

	var rows []historyBoundaryRow
	if err := s.db.WithContext(ctx).Raw(strings.Join(branches, " UNION ALL "), args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("loading the boundary heartbeats: %w", err)
	}
	return rows, nil
}

// Window returns the statistics of a single monitor for the last N hours.
func (s *StatsService) Window(ctx context.Context, monitorID uint, hours int) (*models.UptimeStats, error) {
	since := queryTime(time.Now().UTC().Add(-time.Duration(hours) * time.Hour))

	type aggRow struct {
		Up      int64    `gorm:"column:up"`
		Down    int64    `gorm:"column:down"`
		Pending int64    `gorm:"column:pending"`
		Total   int64    `gorm:"column:total"`
		AvgMS   *float64 `gorm:"column:avg_ms"`
		MinMS   *int64   `gorm:"column:min_ms"`
		MaxMS   *int64   `gorm:"column:max_ms"`
	}
	query := `
		SELECT SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END)      AS up,
		       SUM(CASE WHEN status = 0 THEN 1 ELSE 0 END)      AS down,
		       SUM(CASE WHEN status = 2 THEN 1 ELSE 0 END)      AS pending,
		       COUNT(*)                                         AS total,
		       AVG(CASE WHEN status = 1 THEN latency_ms END)    AS avg_ms,
		       MIN(latency_ms)                                  AS min_ms,
		       MAX(latency_ms)                                  AS max_ms
		FROM heartbeats
		WHERE monitor_id = ? AND created_at >= ?`

	var row aggRow
	if err := s.db.WithContext(ctx).Raw(query, monitorID, since).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("computing statistics: %w", err)
	}

	stats := &models.UptimeStats{
		MonitorID: monitorID,
		Hours:     hours,
		Up:        int(row.Up),
		Down:      int(row.Down),
		Pending:   int(row.Pending),
		Total:     int(row.Total),
	}
	stats.Uptime = percentage(row.Up, row.Total)
	if row.AvgMS != nil {
		stats.AvgMS = *row.AvgMS
	}
	if row.MinMS != nil {
		stats.MinMS = *row.MinMS
	}
	if row.MaxMS != nil {
		stats.MaxMS = *row.MaxMS
	}
	stats.P95MS = s.percentile95(ctx, monitorID, since)
	return stats, nil
}

func percentage(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
