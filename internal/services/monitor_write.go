package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

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
		"config":                   string(configJSON),
		// Every edit advances the revision: it is the primary component of the
		// merge order, so a wrong clock cannot make an old edit win. It is bumped
		// by the database so two concurrent edits cannot both write the same
		// number.
		"revision":   gorm.Expr("revision + 1"),
		"updated_at": time.Now().UTC(),
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Captured before the write, so the relation diff knows what moved.
		before, err := s.snapshotLinks(ctx, tx, monitor.ID)
		if err != nil {
			return err
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
		if groupIDs != nil {
			if err := replaceMonitorGroups(tx, monitor.ID, groupIDs); err != nil {
				return err
			}
		}
		return s.publishMonitor(ctx, tx, monitor.ID, before)
	})
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound(i18n.CodeMonitorNotFound, fmt.Sprintf("monitor %d does not exist", monitor.ID))
	}
	if err != nil {
		return ErrInternal(fmt.Errorf("updating monitor %d: %w", monitor.ID, err))
	}
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
