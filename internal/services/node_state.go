package services

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// monitorNodeStateUpsert stores the newest verdict of one node about one monitor.
//
// It is a single statement - the same upsert shape as heartbeatRollupUpsert, and
// for the same reason (VALUES(col) is deprecated in MySQL 8.0.20+ and MariaDB
// never grew the alias syntax, so the values are passed twice) - and it never
// moves a row backwards: an out of order write (a retry that lands after the next
// check, a manual "check now", a clock that stepped back) keeps the newer verdict,
// because every column is only replaced when the incoming checked_at is at least
// the stored one. `checked_at` is the LAST assignment on purpose: the comparisons
// in the ones before it still read the stored value.
const monitorNodeStateUpsert = `
	INSERT INTO monitor_node_states
		(monitor_id, node_id, status, latency_ms, status_code, message, important, checked_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON DUPLICATE KEY UPDATE
		status      = IF(? >= checked_at, ?, status),
		latency_ms  = IF(? >= checked_at, ?, latency_ms),
		status_code = IF(? >= checked_at, ?, status_code),
		message     = IF(? >= checked_at, ?, message),
		important   = IF(? >= checked_at, ?, important),
		checked_at  = IF(? >= checked_at, ?, checked_at)`

// saveNodeState mirrors a stored heartbeat into the per node state table the
// cluster voting reads (see models.MonitorNodeState).
//
// It runs on the write path of every check, so it is one statement with no read
// before it: the primary key decides insert or replace.
func (s *HeartbeatService) saveNodeState(ctx context.Context, heartbeat *models.Heartbeat) error {
	return s.db.WithContext(ctx).Exec(monitorNodeStateUpsert,
		// INSERT: the verdict that was just stored.
		heartbeat.MonitorID, heartbeat.NodeID, heartbeat.Status, heartbeat.LatencyMS,
		heartbeat.StatusCode, heartbeat.Message, heartbeat.Important, heartbeat.CreatedAt,
		// UPDATE: the same verdict, applied only where it is not older than the
		// stored one.
		heartbeat.CreatedAt, heartbeat.Status,
		heartbeat.CreatedAt, heartbeat.LatencyMS,
		heartbeat.CreatedAt, heartbeat.StatusCode,
		heartbeat.CreatedAt, heartbeat.Message,
		heartbeat.CreatedAt, heartbeat.Important,
		heartbeat.CreatedAt, heartbeat.CreatedAt,
	).Error
}

// nodeStateSeedChunk is how many monitors one seed statement covers.
//
// The seed binds a monitor id list into an IN clause, and the range optimizer
// walks that many index ranges before it produces its plan. 500 keeps the
// statement short while the number of statements (four for 2 000 monitors) stays
// trivial.
const nodeStateSeedChunk = 500

// nodeStateSeedQuery copies the newest stored heartbeat of every (monitor, node)
// pair of the given monitors, inside the vote window the caller computed.
//
// The index hint is the point of the statement. `heartbeats` carries two indexes
// that could answer it, and the optimizer prefers idx_hb_monitor_node because it
// also satisfies the GROUP BY - but with the node unconstrained it can only range
// on monitor_id, which turns the created_at bound into a filter over EVERY check
// the installation ever stored for those monitors (180 days at a 60 s interval is
// what makes this shape of query take half a minute). Pinning idx_hb_monitor_time
// (monitor_id, created_at) makes the timestamp a range of the index: the scan is
// then proportional to the window (a couple of entries per monitor at the default
// interval) instead of to the history.
//
// VALUES() is the same trade-off rollupBackfillQuery documents: deprecated in
// MySQL 8.0.20+, never replaced in MariaDB, and the only portable spelling of
// "the row that was just inserted". The stored columns are qualified with the
// table name because an INSERT ... SELECT ... ON DUPLICATE KEY UPDATE resolves an
// unqualified name against the SELECT list as well, and the engine rejects the
// statement as ambiguous.
const nodeStateSeedQuery = `
	INSERT INTO monitor_node_states
		(monitor_id, node_id, status, latency_ms, status_code, message, important, checked_at)
	SELECT h.monitor_id, h.node_id, h.status, h.latency_ms, h.status_code, h.message,
	       h.important, h.created_at
	FROM heartbeats h
	JOIN (
		SELECT monitor_id, node_id, MAX(id) AS max_id
		FROM heartbeats FORCE INDEX (idx_hb_monitor_time)
		WHERE monitor_id IN ? AND created_at >= ?
		GROUP BY monitor_id, node_id
	) newest ON newest.max_id = h.id
	ON DUPLICATE KEY UPDATE
		status      = IF(VALUES(checked_at) >= monitor_node_states.checked_at, VALUES(status), monitor_node_states.status),
		latency_ms  = IF(VALUES(checked_at) >= monitor_node_states.checked_at, VALUES(latency_ms), monitor_node_states.latency_ms),
		status_code = IF(VALUES(checked_at) >= monitor_node_states.checked_at, VALUES(status_code), monitor_node_states.status_code),
		message     = IF(VALUES(checked_at) >= monitor_node_states.checked_at, VALUES(message), monitor_node_states.message),
		important   = IF(VALUES(checked_at) >= monitor_node_states.checked_at, VALUES(important), monitor_node_states.important),
		checked_at  = IF(VALUES(checked_at) >= monitor_node_states.checked_at, VALUES(checked_at), monitor_node_states.checked_at)`

// monitorInterval is the monitor data the seed needs: the id to bind and the
// interval that decides how far back its verdict can still be valid.
type monitorInterval struct {
	ID              uint `gorm:"column:id"`
	IntervalSeconds int  `gorm:"column:interval_seconds"`
}

// voteWindowGroup is a set of monitors whose vote window has the same width, so
// one statement can seed them all.
type voteWindowGroup struct {
	window time.Duration
	ids    []uint
}

// groupByVoteWindow buckets the monitors by the width of their vote window.
//
// The bucketing is what keeps the seed honest and cheap at once: the widest
// window of an installation is normally 2 minutes (the floor of voteWindow), so
// one statement covers every monitor, while the few monitors checked every few
// hours get their own statement with their own - necessarily wider - bound
// instead of dragging every other monitor a day back.
func groupByVoteWindow(monitors []monitorInterval) []voteWindowGroup {
	groups := map[time.Duration][]uint{}
	for _, monitor := range monitors {
		window := voteWindow(&models.Monitor{IntervalSeconds: monitor.IntervalSeconds})
		groups[window] = append(groups[window], monitor.ID)
	}
	out := make([]voteWindowGroup, 0, len(groups))
	for window, ids := range groups {
		out = append(out, voteWindowGroup{window: window, ids: ids})
	}
	// Ascending width: the common group first, so the boot log reads in the order
	// the statements run.
	sort.Slice(out, func(i, j int) bool { return out[i].window < out[j].window })
	return out
}

// chunkIDs splits an id list into groups of at most size, so a statement never
// binds more ids than its plan can handle. An empty list has no chunks: an
// `IN ()` is not a statement any engine accepts.
func chunkIDs(ids []uint, size int) [][]uint {
	if len(ids) == 0 {
		return nil
	}
	if size <= 0 || len(ids) <= size {
		return [][]uint{ids}
	}
	out := make([][]uint, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[start:end])
	}
	return out
}

// reapOrphanNodeStates removes the verdicts of monitors that no longer exist.
//
// A probe that was already running when its monitor was deleted can still store
// its heartbeat, and with it the verdict: the scheduler stops the worker on its
// next reconcile pass, but the statement that was in flight lands afterwards and
// recreates the row the deletion had just removed. Nothing reads it (no monitor
// references it) and the table is bounded by construction, so this is hygiene
// rather than a correctness problem - but the row would otherwise stay for ever,
// because the retention policy never prunes this table. The boot is the natural
// place for it, and it heals a crash halfway through a deletion the same way.
func reapOrphanNodeStates(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	result := db.WithContext(ctx).Exec(`
		DELETE states FROM monitor_node_states states
		LEFT JOIN monitors m ON m.id = states.monitor_id
		WHERE m.id IS NULL`)
	if result.Error != nil {
		return fmt.Errorf("removing the per node states of deleted monitors: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		log.Info("removed the per node states of deleted monitors", "rows", result.RowsAffected)
	}
	return nil
}

// SeedNodeStates fills monitor_node_states from the stored heartbeats.
//
// The table is a derived one: the probes write it themselves (see Record), so it
// fills up again by itself after a release that introduces it - but only as fast
// as the monitors are checked. A monitor on a six hour interval would spend the
// next six hours with no verdict at all, which reads as "pending" on the
// dashboard, so the boot copies the last verdict of every node from the history
// before the scheduler starts.
//
// It is bounded by the answer and not by the history: the bound of each statement
// is the vote window of the monitors it covers (two minutes for the default
// interval) and the index hint of nodeStateSeedQuery keeps that bound a range. It
// is idempotent and it never moves a row backwards, so a second boot - or a boot
// after a crash halfway through - changes nothing. It also removes the verdicts of
// monitors that no longer exist (reapOrphanNodeStates).
func SeedNodeStates(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	// Before anything else: the rows of monitors that are already gone (see
	// reapOrphanNodeStates), so the seed cannot resurrect a verdict either.
	if err := reapOrphanNodeStates(ctx, db, log); err != nil {
		return err
	}
	var monitors []monitorInterval
	if err := db.WithContext(ctx).Model(&models.Monitor{}).
		Select("id", "interval_seconds").
		Find(&monitors).Error; err != nil {
		return fmt.Errorf("reading the monitors to seed the per node states: %w", err)
	}
	if len(monitors) == 0 {
		return nil
	}

	began := time.Now()
	statements, affected := 0, int64(0)
	for _, group := range groupByVoteWindow(monitors) {
		// One instant for the whole group: every statement of a group has to use
		// the same bound, or a slow pass would compare against a moving one.
		since := queryTime(time.Now().UTC().Add(-group.window))
		for _, ids := range chunkIDs(group.ids, nodeStateSeedChunk) {
			if err := ctx.Err(); err != nil {
				return err
			}
			result := db.WithContext(ctx).Exec(nodeStateSeedQuery, ids, since)
			if result.Error != nil {
				return fmt.Errorf("seeding the per node states of %d monitors: %w", len(ids), result.Error)
			}
			statements++
			// INSERT ... ON DUPLICATE KEY UPDATE counts two per replaced row, so
			// the number is the work done, not the rows stored.
			affected += result.RowsAffected
		}
	}
	log.Info("per node state table seeded from the heartbeat history",
		"monitors", len(monitors), "statements", statements,
		"affected", affected, "took_ms", time.Since(began).Milliseconds())
	return nil
}
