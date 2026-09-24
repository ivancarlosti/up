package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// MonitorCloneOptions controls a monitor clone.
type MonitorCloneOptions struct {
	// Name of the copy (empty means "<source> (copy)").
	Name string
	// Active of the copy; nil keeps the value of the source.
	Active *bool
	// CopyNotifications links the copy to the same notification channels.
	CopyNotifications bool
	// CopyGroups puts the copy in the same groups.
	CopyGroups bool
}

// Clone duplicates a monitor: configuration, and optionally the notification
// links and the group memberships. The heartbeat history and the aggregated
// state are deliberately NOT copied, so a copy starts with a clean history and
// does not inherit an old incident.
func (s *MonitorService) Clone(ctx context.Context, id uint, opts MonitorCloneOptions) (*models.Monitor, error) {
	source, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = source.Name + " (copy)"
	}
	unique, err := s.uniqueMonitorName(ctx, name)
	if err != nil {
		return nil, err
	}

	clone := &models.Monitor{
		Name:                   unique,
		Type:                   source.Type,
		Active:                 source.Active,
		Description:            source.Description,
		IntervalSeconds:        source.IntervalSeconds,
		Retries:                source.Retries,
		RetriesIntervalSeconds: source.RetriesIntervalSeconds,
		TimeoutSeconds:         source.TimeoutSeconds,
		ResendIntervalSeconds:  source.ResendIntervalSeconds,
		UpsideDown:             source.UpsideDown,
		RunOn:                  source.RunOn,
		RunOnNodes:             source.RunOnNodes,
		NodeID:                 source.NodeID,
		Tags:                   source.Tags,
		Config:                 source.Config,
	}
	if opts.Active != nil {
		clone.Active = *opts.Active
	}

	var notificationIDs []uint
	if opts.CopyNotifications {
		notificationIDs = source.NotificationIDs
	}
	var groupIDs []uint
	if opts.CopyGroups {
		groupIDs = source.GroupIDs
	}

	if err := s.Create(ctx, clone, notificationIDs, groupIDs); err != nil {
		return nil, err
	}
	s.log.Info("monitor cloned", "source", id, "clone", clone.ID, "name", clone.Name,
		"notifications", opts.CopyNotifications, "groups", opts.CopyGroups)
	return s.Get(ctx, clone.ID)
}

// uniqueMonitorName appends a counter until the name is free. Monitor names are
// not unique in the schema, but a clone that collides with an existing name is
// confusing in the list, so the counter keeps them distinguishable.
func (s *MonitorService) uniqueMonitorName(ctx context.Context, base string) (string, error) {
	name := base
	for attempt := 2; attempt < 100; attempt++ {
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.Monitor{}).
			Where("LOWER(name) = LOWER(?)", name).Count(&count).Error; err != nil {
			return "", ErrInternal(fmt.Errorf("checking the monitor name %q: %w", name, err))
		}
		if count == 0 {
			return name, nil
		}
		name = fmt.Sprintf("%s (%d)", base, attempt)
	}
	return "", ErrConflict(i18n.CodeAlreadyExists, fmt.Sprintf("could not find a free name for %q", base))
}
