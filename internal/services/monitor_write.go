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

// Create inserts a monitor, links the notification channels and the groups and
// creates the aggregated state row.
func (s *MonitorService) Create(ctx context.Context, monitor *models.Monitor, notificationIDs, groupIDs []uint) error {
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
		if err := replaceMonitorGroups(tx, monitor.ID, groupIDs); err != nil {
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
	s.log.Info("monitor created", "id", monitor.ID, "name", monitor.Name, "type", monitor.Type)
	s.publish("monitor.created", monitor)
	return nil
}

// Update saves the editable fields of a monitor. A nil notificationIDs/groupIDs
// keeps the current links, exactly like the update endpoint always did.
func (s *MonitorService) Update(ctx context.Context, monitor *models.Monitor, notificationIDs, groupIDs []uint) error {
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
			DomainExpiresAt *time.Time `gorm:"column:domain_expires_at"`
		}
		if err := tx.Model(&models.Monitor{}).
			Select("domain_expires_at").
			Where("id = ?", monitor.ID).
			Take(&existing).Error; err != nil {
			return err
		}
		previous = existing.DomainExpiresAt
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
		if groupIDs != nil {
			if err := replaceMonitorGroups(tx, monitor.ID, groupIDs); err != nil {
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
	s.log.Info("monitor updated", "id", monitor.ID, "name", monitor.Name, "active", monitor.Active)
	s.publish("monitor.updated", monitor)
	return nil
}

// Delete removes a monitor together with its heartbeats, state and links.
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
		if err := deleteMonitorCascade(tx, id); err != nil {
			return err
		}
		return s.tombstoneMonitor(ctx, tx, monitorUUID, before)
	})
	if err != nil {
		return ErrInternal(fmt.Errorf("deleting monitor %d: %w", id, err))
	}
	s.log.Info("monitor deleted", "id", id)
	s.publish("monitor.deleted", map[string]any{"id": id})
	return nil
}

// deleteMonitorCascade removes everything that belongs to a monitor.
//
// It is shared by MonitorService.Delete and by the synchronisation apply path.
// The apply cannot call the service instead: the service opens its own transaction
// on a different connection, which would break the atomicity between the applied
// change and the cursor that records it.
func deleteMonitorCascade(tx *gorm.DB, id uint) error {
	for _, model := range []any{
		&models.Heartbeat{},
		&models.MonitorState{},
		&models.MonitorNotification{},
		&models.MonitorGroupMember{},
		&models.MonitorCertificate{},
		&models.MonitorDomain{},
		&models.NotificationLock{},
		&models.StatusPageMonitor{},
	} {
		if err := tx.Where("monitor_id = ?", id).Delete(model).Error; err != nil {
			return err
		}
	}
	return tx.Delete(&models.Monitor{}, id).Error
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

// replaceMonitorGroups rewrites the groups a monitor belongs to (the reverse of
// replaceMonitorGroupMembers: here the monitor is the fixed side).
func replaceMonitorGroups(tx *gorm.DB, monitorID uint, groupIDs []uint) error {
	if err := validateGroupIDs(tx, groupIDs); err != nil {
		return err
	}
	if err := tx.Where("monitor_id = ?", monitorID).Delete(&models.MonitorGroupMember{}).Error; err != nil {
		return err
	}
	seen := map[uint]bool{}
	order := 0
	for _, id := range groupIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		order++
		member := models.MonitorGroupMember{GroupID: id, MonitorID: monitorID, SortOrder: order}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
	}
	return nil
}
