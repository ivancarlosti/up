package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/expiry"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// resolveDomainExpiresAt mirrors a manual domain expiration date to every
// monitor of the same registrable domain and returns the ids it wrote.
//
// The date is a property of the DOMAIN, not of the monitor that happens to type
// it: "www.example.com" and "app.example.com" share one registry registration,
// and two different dates for one domain only produced two rows of the same
// domain in Admin > TLD/SSL expiration. Mirroring the value on the siblings
// keeps the per-monitor column (and the synchronisation payload that already
// carries it) as the single source of truth, without adding a second table.
//
// The date does NOT belong to the watch switch: domain_watch decides what is
// looked up and what is notified, never what is remembered. Turning the switch
// off (or editing a monitor that never watched) used to send an explicit clear
// for the whole domain, which is why the mirror now runs for any monitor that
// carries a date.
//
// Three rules make "one value per domain" hold:
//
//   - a monitor that types a date sets it for the domain (every sibling follows);
//   - a monitor that WATCHES the domain and carries no date INHERITS the date of
//     its siblings, so a new watcher can never disagree with them;
//   - clearing the date is explicit: it clears the siblings that carried one,
//     while an unrelated edit of a monitor without a date changes nothing.
//
// previous is the stored value of the monitor before the write, which is what
// tells an explicit clear from an unrelated edit.
func (s *MonitorService) resolveDomainExpiresAt(tx *gorm.DB, monitor *models.Monitor, previous *time.Time) ([]uint, error) {
	// A monitor that neither watches its domain nor carries a date (and never
	// did) has nothing to mirror: a plain edit of an unrelated monitor must not
	// scan the table.
	if !monitor.DomainWatch && monitor.DomainExpiresAt == nil && previous == nil {
		return nil, nil
	}
	domain := expiry.DomainName(monitor)
	if domain == "" {
		return nil, nil
	}
	// One query for the whole domain: the siblings are both the target of the
	// mirror and the source of the value when this monitor carries none. A
	// sibling without the watch is included when it carries a date (the date it
	// holds is part of the domain's truth), but a bare monitor without a watch
	// and without a date is left alone.
	var rows []models.Monitor
	if err := tx.Select("id", "type", "config", "domain_watch", "domain_expires_at").
		Where("id <> ? AND (domain_watch = ? OR domain_expires_at IS NOT NULL)", monitor.ID, true).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	siblings := make([]struct {
		ID      uint
		Expires *time.Time
	}, 0, len(rows))
	for i := range rows {
		if expiry.DomainName(&rows[i]) != domain {
			continue
		}
		siblings = append(siblings, struct {
			ID      uint
			Expires *time.Time
		}{ID: rows[i].ID, Expires: validDate(rows[i].DomainExpiresAt)})
	}

	// The value of the domain: what this monitor typed, or the date of a sibling
	// when the monitor NEVER carried one (a watcher added after the date was typed
	// inherits it, so it can never disagree with its siblings).
	//
	// An explicit clear (previous != nil, new value nil) never inherits: reading
	// the sibling date back would silently undo the clear. A monitor that does
	// not watch the domain never reaches this branch (the guard at the top
	// returns for a monitor with no date and no previous value), which is what
	// keeps the inherit rule a watcher rule.
	value := validDate(monitor.DomainExpiresAt)
	if value == nil && previous == nil {
		for _, sibling := range siblings {
			if sibling.Expires == nil {
				continue
			}
			if value == nil || sibling.Expires.After(*value) {
				value = sibling.Expires
			}
		}
	}
	if value == nil && previous == nil {
		// No date anywhere and none typed: nothing to mirror.
		return nil, nil
	}

	// A monitor added after the date was typed adopts it, so the column of the
	// new row agrees with the rest of the domain.
	if value != nil && monitor.DomainExpiresAt == nil {
		if err := tx.Model(&models.Monitor{}).Where("id = ?", monitor.ID).
			Update("domain_expires_at", value).Error; err != nil {
			return nil, err
		}
		monitor.DomainExpiresAt = value
	}

	// Only the siblings that do not already hold the value are written: saving an
	// unrelated edit (or re-saving the same date) must not churn the whole domain.
	mirror := make([]uint, 0, len(siblings))
	for _, sibling := range siblings {
		if !sameDate(sibling.Expires, value) {
			mirror = append(mirror, sibling.ID)
		}
	}
	if len(mirror) == 0 {
		return nil, nil
	}
	updates := map[string]any{
		"domain_expires_at": value,
		// Same reasoning as a normal edit: the mirror is a new version of every
		// sibling row, so a peer must not skip it.
		"revision":   gorm.Expr("revision + 1"),
		"updated_at": time.Now().UTC(),
	}
	if err := tx.Model(&models.Monitor{}).Where("id IN ?", mirror).Updates(updates).Error; err != nil {
		return nil, err
	}
	s.log.Info("manual domain expiration mirrored",
		"domain", domain, "source_monitor", monitor.ID, "monitors", len(mirror))
	return mirror, nil
}

// validDate normalises the nullable date columns: nil and the zero time both
// mean "no date".
func validDate(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// sameDate reports whether two nullable dates hold the same instant.
func sameDate(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// publishDomainSiblings records the mirrored monitors in the synchronisation
// outbox, so a federated cluster converges on the same date.
func (s *MonitorService) publishDomainSiblings(ctx context.Context, tx *gorm.DB, ids []uint) error {
	if s.emit == nil || len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if err := s.emit.EmitMonitor(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// SetDomainService injects the domain expiration service. The write path asks it
// to apply a manual date to the stored observation of the whole domain, so the
// badge is right the moment the date is saved instead of at the next daily run.
func (s *MonitorService) SetDomainService(d *DomainService) { s.domains = d }

// applyManualDomainDate materialises the manual date of a monitor's domain.
//
// It runs AFTER the transaction commits, never inside it: an observation written
// for a date the transaction rolled back would claim a truth that was never
// stored.
func (s *MonitorService) applyManualDomainDate(ctx context.Context, monitor *models.Monitor, previous *time.Time) {
	if s.domains == nil || monitor == nil {
		return
	}
	// Only a save that touched a date can change what the observation must say.
	// An inherit counts as touching one: resolveDomainExpiresAt already stamped
	// the value on the monitor before this runs. A bulk import of watchers
	// without a date therefore stays off this path, instead of scanning the
	// domain once per row.
	if validDate(monitor.DomainExpiresAt) == nil && previous == nil {
		return
	}
	domain := expiry.DomainName(monitor)
	if domain == "" {
		return
	}
	if _, err := s.domains.ApplyManual(ctx, domain); err != nil {
		s.log.Warn("could not apply the manual domain expiration date",
			"monitor_id", monitor.ID, "domain", domain, "error", err)
	}
}

// Create inserts a monitor, links the notification channels and the group and
// creates the aggregated state row.
//
// groupID is the single group the monitor joins: nil and 0 both mean "no group"
// (a create has no current group to keep), any other id is the group it belongs to.
func (s *MonitorService) Create(ctx context.Context, monitor *models.Monitor, notificationIDs []uint, groupID *uint) error {
	if err := s.Validate(monitor); err != nil {
		return err
	}
	// The identity is stamped here rather than by a later update, so the row, the
	// payload and the response all name the same creator from the start.
	monitor.OriginNodeID = s.cfg.NodeID
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(monitor).Error; err != nil {
			return err
		}
		// A manual domain date typed here belongs to the whole domain: mirror it
		// before the links are written, so everything lands in one transaction.
		siblings, mirrorErr := s.resolveDomainExpiresAt(tx, monitor, nil)
		if mirrorErr != nil {
			return mirrorErr
		}
		if err := s.publishDomainSiblings(ctx, tx, siblings); err != nil {
			return err
		}
		if err := replaceMonitorNotifications(tx, monitor.ID, notificationIDs); err != nil {
			return err
		}
		if err := setMonitorGroup(tx, monitor.ID, groupID); err != nil {
			return err
		}
		state := models.MonitorState{
			MonitorID: monitor.ID,
			Status:    models.AggregateUnknown,
			ChangedAt: time.Now().UTC(),
		}
		if err := tx.Create(&state).Error; err != nil {
			return err
		}
		// A created monitor starts with no relation, so everything it links to is
		// new (and gets published as such).
		return s.publishMonitor(ctx, tx, monitor.ID, EmptyMonitorLinks())
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("creating monitor: %w", err))
	}
	// The date the operator typed is applied to the stored observation now: the
	// monitor (and every other watcher of the domain) shows it without waiting for
	// the next run of the daily job.
	s.applyManualDomainDate(ctx, monitor, nil)
	// A monitor created with the web performance switch on gets its first score
	// straight away (asynchronously and paced by the API limiter).
	s.applyLighthouse(ctx, monitor, nil)
	s.log.Info("monitor created", "id", monitor.ID, "name", monitor.Name, "type", monitor.Type)
	s.publish("monitor.created", monitor)
	return nil
}

// Update saves the editable fields of a monitor. A nil notificationIDs/groupID
// keeps the current links, exactly like the update endpoint always did.
func (s *MonitorService) Update(ctx context.Context, monitor *models.Monitor, notificationIDs []uint, groupID *uint) error {
	if err := s.Validate(monitor); err != nil {
		return err
	}
	// The Config column is written as a marshalled JSON string: GORM only
	// applies the "serializer:json" tag to model fields, not to raw map values.
	configJSON, err := json.Marshal(monitor.Config)
	if err != nil {
		return ErrInternal(fmt.Errorf("encoding the monitor configuration: %w", err))
	}
	updates := map[string]any{
		"name":                     monitor.Name,
		"type":                     monitor.Type,
		"description":              monitor.Description,
		"active":                   monitor.Active,
		"interval_seconds":         monitor.IntervalSeconds,
		"retries":                  monitor.Retries,
		"retries_interval_seconds": monitor.RetriesIntervalSeconds,
		"timeout_seconds":          monitor.TimeoutSeconds,
		"resend_interval_seconds":  monitor.ResendIntervalSeconds,
		"upside_down":              monitor.UpsideDown,
		"run_on":                   monitor.RunOn,
		"node_id":                  monitor.NodeID,
		"tags":                     monitor.Tags,
		"cert_watch":               monitor.CertWatch,
		"cert_notify":              monitor.CertNotify,
		"cert_warn_days":           monitor.CertWarnDays,
		"lighthouse_enabled":       monitor.LighthouseEnabled,
		"domain_watch":             monitor.DomainWatch,
		"domain_notify":            monitor.DomainNotify,
		"domain_warn_days":         monitor.DomainWarnDays,
		"domain_expires_at":        monitor.DomainExpiresAt,
		"template_uuid":            monitor.TemplateUUID,
		"config":                   string(configJSON),
		// Every edit advances the revision: it is the primary component of the
		// merge order, so a wrong clock cannot make an old edit win. It is bumped
		// by the database so two concurrent edits cannot both write the same
		// number.
		"revision":   gorm.Expr("revision + 1"),
		"updated_at": time.Now().UTC(),
	}

	// The stored value of the date before this edit: it tells an explicit clear
	// from an unrelated edit, and it is what the post-commit pass needs.
	var previous *time.Time
	// The stored web performance state before this edit: the post-commit pass
	// needs it to tell "the switch was just turned on" (or the page changed)
	// from an unrelated edit.
	var previousLighthouse *models.Monitor
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Captured before the write, so the relation diff knows what moved.
		before, err := s.snapshotLinks(ctx, tx, monitor.ID)
		if err != nil {
			return err
		}
		// The previous manual date tells an explicit clear from an unrelated
		// edit: only the former is mirrored to the siblings (see
		// resolveDomainExpiresAt).
		var existing struct {
			DomainExpiresAt   *time.Time `gorm:"column:domain_expires_at"`
			LighthouseEnabled bool       `gorm:"column:lighthouse_enabled"`
			Config            []byte     `gorm:"column:config"`
		}
		if err := tx.Model(&models.Monitor{}).
			Select("domain_expires_at", "lighthouse_enabled", "config").
			Where("id = ?", monitor.ID).
			Take(&existing).Error; err != nil {
			return err
		}
		previous = existing.DomainExpiresAt
		previousLighthouse = &models.Monitor{
			LighthouseEnabled: existing.LighthouseEnabled,
			Config:            decodeMonitorConfig(existing.Config),
		}
		result := tx.Model(&models.Monitor{}).Where("id = ?", monitor.ID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if notificationIDs != nil {
			if err := replaceMonitorNotifications(tx, monitor.ID, notificationIDs); err != nil {
				return err
			}
		}
		if groupID != nil {
			if err := setMonitorGroup(tx, monitor.ID, groupID); err != nil {
				return err
			}
		}
		// A manual date edited (or cleared) here belongs to the whole domain:
		// mirror it to the siblings in the same transaction.
		siblings, mirrorErr := s.resolveDomainExpiresAt(tx, monitor, previous)
		if mirrorErr != nil {
			return mirrorErr
		}
		if err := s.publishDomainSiblings(ctx, tx, siblings); err != nil {
			return err
		}
		return s.publishMonitor(ctx, tx, monitor.ID, before)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeMonitorNotFound, fmt.Sprintf("monitor %d does not exist", monitor.ID))
	}
	if err != nil {
		return ErrInternal(fmt.Errorf("updating monitor %d: %w", monitor.ID, err))
	}
	// A date that was typed, changed or cleared is applied to the stored
	// observation now, so the badge never waits for the daily job.
	s.applyManualDomainDate(ctx, monitor, previous)
	// The web performance switch (and the page or strategy it measures) decides
	// whether a first run is queued, or whether a stale score must be forgotten.
	s.applyLighthouse(ctx, monitor, previousLighthouse)
	s.log.Info("monitor updated", "id", monitor.ID, "name", monitor.Name, "active", monitor.Active)
	s.publish("monitor.updated", monitor)
	return nil
}

// Delete removes a monitor together with its heartbeats, state and links.
//
// The deletion has two phases, and the split is the point.
//
// Phase one is one short transaction: the identity and the links are read (the
// tombstones have to name them), the per monitor rows that are bounded by design
// are deleted - one per node, per group, per channel, per certificate - and the
// monitor row goes with them. The monitor is gone from every listing at once and
// no read can reach the rest of its data.
//
// Phase two is the HISTORY: heartbeats, hourly rollups and web performance runs,
// whose row count grows with the age of the monitor and not with the monitor. It
// used to run inside the same transaction, as a single unbounded
// `DELETE FROM heartbeats WHERE monitor_id = ?`: hundreds of thousands of rows in
// one statement, minutes of undo log, one lock that the probes (and every
// concurrent dashboard read) queued behind. The HTTP request waited for it and
// died at the driver's 30 s readTimeout, so the operator saw a 500 while the
// server kept deleting.
//
// It now runs after the commit, in chunks (see purgeMonitorHistory), so no
// transaction outlives a chunk and the statement that timed out is gone. No
// foreign keys tie the two phases together (the schema has none), and the chunked
// deletion is resumable: rows that survive a crash belong to a monitor that no
// longer exists, and the retention policy collects them like any other old row.
func (s *MonitorService) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The identity and the relations are read BEFORE the cascade: once the rows
		// are gone they cannot be read, and the tombstones have to name them.
		monitorUUID, err := s.monitorUUID(ctx, tx, id)
		if err != nil {
			return err
		}
		before, err := s.snapshotLinks(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := deleteMonitorRow(tx, id); err != nil {
			return err
		}
		return s.tombstoneMonitor(ctx, tx, monitorUUID, before)
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("deleting monitor %d: %w", id, err))
	}
	s.log.Info("monitor deleted", "id", id)
	// The response does not wait for the history: it is the part that used to
	// exceed every timeout involved, and it is invisible to the API either way.
	go s.purgeMonitorHistory(ctx, id)
	s.publish("monitor.deleted", map[string]any{"id": id})
	return nil
}

// monitorLinkModels are the per monitor rows whose count is bounded by design:
// one per cluster node, per notification channel, per group, per status page item,
// per certificate, per domain, per lighthouse strategy. Deleting them costs the
// same on a monitor created a minute ago and on one created a year ago, so they
// stay inside the transaction of the deletion.
var monitorLinkModels = []any{
	&models.MonitorNodeState{},
	&models.MonitorState{},
	&models.MonitorNotification{},
	&models.MonitorGroupMember{},
	&models.MonitorCertificate{},
	&models.MonitorDomain{},
	&models.MonitorLighthouse{},
	&models.NotificationLock{},
	&models.StatusPageMonitor{},
}

// monitorHistoryModels are the per monitor rows whose count grows with the AGE of
// the monitor: one heartbeat per check, one rollup per hour, one lighthouse run per
// day. They are never deleted inside the transaction of a request - see
// MonitorService.Delete and purgeMonitorHistory.
var monitorHistoryModels = []any{
	&models.Heartbeat{},
	&models.HeartbeatRollup{},
	&models.LighthouseRun{},
}

// monitorHistoryChunk is how many history rows one DELETE of a monitor deletion
// removes. It mirrors purgeChunk: the number chosen so a single statement holds
// its lock for milliseconds instead of minutes.
const monitorHistoryChunk = 5000

// monitorHistoryPurgeTimeout bounds the background deletion of a monitor's
// history.
//
// A monitor checked every 60 s for the default 180 days holds ~260 000 heartbeats,
// i.e. ~50 chunks; the timeout is generous enough for a monitor checked every 5 s
// (3 million rows, ~600 chunks) and still bounds a pathological one. When it
// expires the remaining rows are left to the retention policy, which is the same
// `DELETE` in the same chunks with no monitor to wait for.
const monitorHistoryPurgeTimeout = 15 * time.Minute

// purgeMonitorHistory deletes the history of a monitor that no longer exists.
//
// It runs OUTSIDE any transaction, one chunk at a time, so the undo log of each
// statement stays small and the probes of every other monitor keep writing
// between chunks (that is what the single `DELETE` prevented: its locks were held
// for the whole statement).
//
// The request context is deliberately not used as it is: the browser that asked
// for the deletion may close the tab, and a purge killed halfway would leave the
// history behind for no reason. context.WithoutCancel keeps the values (the
// logger, the trace) and drops the cancellation, and the timeout is the only bound
// left.
func (s *MonitorService) purgeMonitorHistory(ctx context.Context, id uint) {
	purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), monitorHistoryPurgeTimeout)
	defer cancel()

	began := time.Now()
	deleted := int64(0)
	for _, model := range monitorHistoryModels {
		count, err := deleteInChunks(purgeCtx, s.db, model, monitorHistoryChunk, "monitor_id = ?", id)
		deleted += count
		if err != nil {
			// Not a failed deletion: the monitor is already gone from every listing.
			// The rows that are left belong to a monitor id nobody references, and the
			// retention purge collects them with the rest of the old rows.
			s.log.Warn("could not finish deleting the history of a deleted monitor: the retention job will collect it",
				"monitor_id", id, "deleted", deleted, "error", err)
			return
		}
	}
	s.log.Info("monitor history deleted",
		"id", id, "rows", deleted, "took_ms", time.Since(began).Milliseconds())
}

// deleteMonitorRow removes the monitor and the per monitor rows that are bounded
// by design, inside the caller's transaction.
func deleteMonitorRow(tx *gorm.DB, id uint) error {
	for _, model := range monitorLinkModels {
		if err := tx.Where("monitor_id = ?", id).Delete(model).Error; err != nil {
			return err
		}
	}
	return tx.Delete(&models.Monitor{}, id).Error
}

// deleteMonitorCascade removes everything that belongs to a monitor, history
// included, inside the caller's transaction.
//
// It is used by the synchronisation apply path, and only by it: the apply cannot
// call the service instead (the service opens its own transaction on a different
// connection, which would break the atomicity between the applied change and the
// cursor that records it), and it must not defer the history either, because the
// cursor advances in the same transaction. A delete pulled from a peer therefore
// still pays for the history of that monitor in one statement - the difference is
// that it happens once per peer deletion instead of once per operator click, and
// that no HTTP client is waiting for it.
func deleteMonitorCascade(tx *gorm.DB, id uint) error {
	for _, model := range monitorHistoryModels {
		if err := tx.Where("monitor_id = ?", id).Delete(model).Error; err != nil {
			return err
		}
	}
	return deleteMonitorRow(tx, id)
}

// applyLighthouse starts (or clears) the web performance side of a monitor
// write.
//
// Turning the switch on — or changing the page it measures or the strategy —
// asks for one asynchronous run, so the operator sees a score within seconds
// instead of waiting for the daily instant. Turning it off forgets the stored
// observation (the history stays; the retention owns it), and narrowing the
// strategy forgets the runs the monitor no longer measures.
//
// previous is the stored state before the write (nil on a creation), which is
// what tells "the switch was just turned on" from an unrelated edit: an edit
// that changes neither the page nor the strategy must not spend an API call.
func (s *MonitorService) applyLighthouse(ctx context.Context, monitor *models.Monitor, previous *models.Monitor) {
	if s.lighthouse == nil {
		return
	}
	if !monitor.LighthouseEnabled {
		if previous != nil && previous.LighthouseEnabled {
			if err := s.lighthouse.Clear(ctx, []uint{monitor.ID}); err != nil {
				s.log.Warn("could not clear the stored lighthouse result",
					"monitor_id", monitor.ID, "error", err)
			}
		}
		return
	}
	strategyChanged := previous != nil && previous.Config.LighthouseStrategy != monitor.Config.LighthouseStrategy
	changed := previous == nil ||
		!previous.LighthouseEnabled ||
		previous.Config.URL != monitor.Config.URL ||
		strategyChanged
	if changed {
		s.lighthouse.Enqueue(monitor.ID)
	}
	if strategyChanged {
		// The monitor may have narrowed the runs it measures ("both" to
		// "mobile"): the score of the run it dropped would never be refreshed,
		// so it is forgotten instead of decorating the monitor forever.
		keep := models.LighthouseStrategyTargets(monitor.Config.LighthouseStrategy)
		if err := s.lighthouse.ClearExcept(ctx, monitor.ID, keep); err != nil {
			s.log.Warn("could not clear the lighthouse runs the monitor dropped",
				"monitor_id", monitor.ID, "error", err)
		}
	}
}

// decodeMonitorConfig reads a stored JSON configuration (the column is written
// as a marshalled string, so a read returns the raw text). An unreadable value
// reads as an empty configuration: the caller only compares it with the new one.
func decodeMonitorConfig(raw []byte) models.MonitorConfig {
	var config models.MonitorConfig
	if len(raw) == 0 {
		return config
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return models.MonitorConfig{}
	}
	return config
}

// SetActive pauses or resumes a monitor.
func (s *MonitorService) SetActive(ctx context.Context, id uint, active bool) (*models.Monitor, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The links do not move here, but the same before/after diff is taken so
		// that the monitor row is published and its relations are left alone.
		before, err := s.snapshotLinks(ctx, tx, id)
		if err != nil {
			return err
		}
		result := tx.Model(&models.Monitor{}).Where("id = ?", id).
			Updates(map[string]any{
				"active":     active,
				"revision":   gorm.Expr("revision + 1"),
				"updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return s.publishMonitor(ctx, tx, id, before)
	})
	if err == gorm.ErrRecordNotFound {
		return nil, ErrNotFound(i18n.CodeMonitorNotFound, fmt.Sprintf("monitor %d does not exist", id))
	}
	if err != nil {
		return nil, ErrInternal(err)
	}
	monitor, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.log.Info("monitor activation changed", "id", id, "active", active)
	s.publish("monitor.updated", monitor)
	return monitor, nil
}

// replaceMonitorNotifications rewrites the monitor <-> notification links.
func replaceMonitorNotifications(tx *gorm.DB, monitorID uint, notificationIDs []uint) error {
	if err := tx.Where("monitor_id = ?", monitorID).Delete(&models.MonitorNotification{}).Error; err != nil {
		return err
	}
	seen := map[uint]bool{}
	for _, id := range notificationIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		link := models.MonitorNotification{MonitorID: monitorID, NotificationID: id, OnDown: true, OnUp: true}
		if err := tx.Create(&link).Error; err != nil {
			return err
		}
	}
	return nil
}

// setMonitorGroup sets the single group of a monitor (the reverse of
// replaceMonitorGroupMembers: here the monitor is the fixed side).
//
// The pointer carries the whole contract of the notification links: nil means
// "leave the current group alone" (an update that does not mention it), 0 means
// "no group" and any other id is the group the monitor joins.
//
// The membership is replaced rather than updated in place, and the caller publishes
// the before/after diff (publishMonitor), so a peer sees exactly what moved.
func setMonitorGroup(tx *gorm.DB, monitorID uint, groupID *uint) error {
	if groupID == nil {
		return nil
	}
	id := *groupID
	if id != 0 {
		if err := validateGroupID(tx, id); err != nil {
			return err
		}
	}
	order := 1
	if id != 0 {
		// A monitor that stays in its group keeps its position inside it; one that
		// moves goes to the END of the group it joins, so a save never silently
		// reorders a group the operator did not touch.
		var existing models.MonitorGroupMember
		switch err := tx.Where("monitor_id = ? AND group_id = ?", monitorID, id).
			Take(&existing).Error; {
		case err == nil:
			order = existing.SortOrder
		case err == gorm.ErrRecordNotFound:
			var last int
			if err := tx.Model(&models.MonitorGroupMember{}).
				Where("group_id = ?", id).
				Select("COALESCE(MAX(sort_order), 0)").
				Scan(&last).Error; err != nil {
				return err
			}
			order = last + 1
		default:
			return err
		}
	}
	if err := tx.Where("monitor_id = ?", monitorID).Delete(&models.MonitorGroupMember{}).Error; err != nil {
		return err
	}
	if id == 0 {
		return nil
	}
	member := models.MonitorGroupMember{GroupID: id, MonitorID: monitorID, SortOrder: order}
	return tx.Create(&member).Error
}
