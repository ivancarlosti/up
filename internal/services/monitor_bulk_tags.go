package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// TagUsage is one tag in use and how many monitors carry it.
type TagUsage struct {
	Tag      string `json:"tag"`
	Monitors int    `json:"monitors"`
}

// ListTags returns every tag in use with the number of monitors carrying it,
// optionally narrowed to one monitor type.
//
// The tags live in one comma separated column (Monitor.Tags), not in a join
// table, so the split, the de-duplication and the counting happen here: the SQL
// stays a single read of a single column and the answer reports "Ops" and "ops"
// once, spelled the way the first monitor that used it spelled it. That is
// exactly the list a picker needs, and it is why the pickers cannot be a plain
// `SELECT DISTINCT tags`.
func (s *MonitorService) ListTags(ctx context.Context, monitorType string) ([]TagUsage, error) {
	query := s.db.WithContext(ctx).Model(&models.Monitor{}).Where("tags <> ''")
	if monitorType != "" {
		query = query.Where("type = ?", monitorType)
	}
	var lists []string
	if err := query.Pluck("tags", &lists).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the monitor tags: %w", err))
	}
	spelling := make(map[string]string, len(lists))
	counts := make(map[string]int, len(lists))
	for _, list := range lists {
		for _, tag := range models.NormalizeTags(list) {
			key := strings.ToLower(tag)
			if _, seen := spelling[key]; !seen {
				spelling[key] = tag
			}
			// NormalizeTags never repeats a tag inside one list, so one monitor
			// contributes at most one to each count: this counts monitors.
			counts[key]++
		}
	}
	usage := make([]TagUsage, 0, len(counts))
	for key, count := range counts {
		usage = append(usage, TagUsage{Tag: spelling[key], Monitors: count})
	}
	// Most used first, then alphabetically: the list is stable between requests
	// (map iteration is not) and the popular tags are at the top.
	sort.Slice(usage, func(i, j int) bool {
		if usage[i].Monitors != usage[j].Monitors {
			return usage[i].Monitors > usage[j].Monitors
		}
		return strings.ToLower(usage[i].Tag) < strings.ToLower(usage[j].Tag)
	})
	return usage, nil
}

// BulkTagOptions is the input of a bulk tag edit.
//
// The three selectors are a union: the dashboard can act on the rows it has
// ticked, on whole groups, or on everything carrying a tag (which is what makes a
// rename possible: everything tagged "prod", minus "prod", plus "production").
type BulkTagOptions struct {
	// MonitorIDs is the explicit selection.
	MonitorIDs []uint
	// GroupIDs adds the monitors of these groups to the selection.
	GroupIDs []uint
	// Tag adds the monitors carrying this tag (whole tag, case insensitive) to
	// the selection.
	Tag string
	// Add is the list of tags to add, Remove the list to drop. Both may be given
	// at once: a rename is exactly that.
	Add    []string
	Remove []string
	// DryRun reports the changes without writing them.
	DryRun bool
}

// BulkTagChange is what a bulk tag run did (or would do) on one monitor.
type BulkTagChange struct {
	MonitorID uint   `json:"monitor_id"`
	Name      string `json:"name"`
	Before    string `json:"tags_before"`
	After     string `json:"tags_after"`
	// Status is dry_run, updated or failed, like a bulk creation row.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// BulkTagReport is the answer of a bulk tag run.
type BulkTagReport struct {
	// Selected is how many monitors the selectors resolved to.
	Selected int `json:"selected"`
	// Updated is how many rows the run writes (a dry run counts them too).
	Updated int `json:"updated"`
	// Unchanged is how many selected rows already carry exactly those tags: they
	// are not rewritten, so the revision of an untouched monitor does not move.
	Unchanged int `json:"unchanged"`
	// Failed is how many rows could not be written.
	Failed int `json:"failed"`
	// DryRun is true when nothing was written: the changes are the preview.
	DryRun bool `json:"dry_run"`
	// Changes lists every selected monitor that changes or fails, so the dialog
	// can show the before/after of the operation it is about to run.
	Changes []BulkTagChange `json:"changes"`
}

// BulkUpdateTags adds and removes tags on a selection of monitors and reports
// what changed, so retagging a whole installation is one gesture instead of one
// form per monitor.
//
// Every row goes through MonitorService.Update, which means a bulk tag edit is an
// ordinary edit: it is validated, the revision advances and the peers are told
// (the tags travel with the monitor, so a node that missed them would keep
// answering the tag-scoped runs of the others wrongly). The rows are written one
// by one for that reason, and a row that cannot be written fails alone.
//
// The returned ids are the monitors that were actually rewritten, for the caller
// to hand to the scheduler, exactly like the bulk creation and the apply paths.
func (s *MonitorService) BulkUpdateTags(ctx context.Context, opts BulkTagOptions) (*BulkTagReport, []uint, error) {
	add := models.NormalizeTags(strings.Join(opts.Add, ","))
	remove := models.NormalizeTags(strings.Join(opts.Remove, ","))
	if len(add) == 0 && len(remove) == 0 {
		return nil, nil, ErrBadRequest(i18n.CodeMonitorBulkInvalid,
			"at least one tag to add or to remove is required")
	}
	monitors, err := s.bulkTagSelection(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	report := &BulkTagReport{Selected: len(monitors), DryRun: opts.DryRun, Changes: []BulkTagChange{}}
	changed := make([]uint, 0, len(monitors))
	for _, monitor := range monitors {
		after := monitor.Tags
		if len(remove) > 0 {
			after = models.RemoveTags(after, remove)
		}
		if len(add) > 0 {
			after = models.AddTags(after, add)
		}
		if after == monitor.Tags {
			report.Unchanged++
			continue
		}
		change := BulkTagChange{
			MonitorID: monitor.ID, Name: monitor.Name,
			Before: monitor.Tags, After: after, Status: "updated",
		}
		// The length is checked here as well as in the write path on purpose: a
		// dry run has to report the rows a real run would refuse instead of
		// previewing an edit that then fails.
		if len(after) > models.MaxTagsLength {
			report.Failed++
			change.Status = "failed"
			change.Error = fmt.Sprintf("the tag list would be longer than %d characters", models.MaxTagsLength)
			report.Changes = append(report.Changes, change)
			continue
		}
		if opts.DryRun {
			report.Updated++
			change.Status = "dry_run"
			report.Changes = append(report.Changes, change)
			continue
		}
		// A copy carrying the new tags: a tag edit never touches the channels or
		// the groups of the monitor (the nil links of Update mean "keep them"),
		// and it never touches the template link either.
		next := *monitor
		next.Tags = after
		if err := s.Update(ctx, &next, nil, nil); err != nil {
			report.Failed++
			change.Status = "failed"
			change.Error = err.Error()
			report.Changes = append(report.Changes, change)
			continue
		}
		report.Updated++
		changed = append(changed, monitor.ID)
		report.Changes = append(report.Changes, change)
	}
	if !opts.DryRun {
		s.log.Info("monitor tags updated in bulk",
			"selected", report.Selected, "updated", report.Updated, "failed", report.Failed)
	}
	return report, changed, nil
}

// bulkTagSelection loads the monitors a bulk tag run applies to.
//
// The selection is resolved once, before any write, so the preview the operator
// confirms is exactly the set of rows the run then touches. An empty selection is
// never "everything": a request that forgot a field must not retag the whole
// installation.
func (s *MonitorService) bulkTagSelection(ctx context.Context, opts BulkTagOptions) ([]*models.Monitor, error) {
	tag := strings.TrimSpace(opts.Tag)
	if len(opts.MonitorIDs) == 0 && len(opts.GroupIDs) == 0 && tag == "" {
		return nil, ErrBadRequest(i18n.CodeMonitorBulkInvalid,
			"select the monitors to retag: monitor_ids, group_ids or a tag")
	}
	query := s.db.WithContext(ctx).Model(&models.Monitor{})
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if len(opts.MonitorIDs) > 0 {
		conditions = append(conditions, "id IN ?")
		args = append(args, opts.MonitorIDs)
	}
	if len(opts.GroupIDs) > 0 {
		conditions = append(conditions, "id IN (?)")
		args = append(args,
			s.db.Model(&models.MonitorGroupMember{}).Select("monitor_id").Where("group_id IN ?", opts.GroupIDs))
	}
	// The tag selector is the tolerant one: the column stores a list, so LIKE
	// finds the candidates and the exact, case insensitive decision is taken
	// below. A substring match alone would retag "production" together with
	// "prod".
	if tag != "" {
		conditions = append(conditions, "tags LIKE ?")
		args = append(args, "%"+tag+"%")
	}
	var monitors []*models.Monitor
	if err := query.Where(strings.Join(conditions, " OR "), args...).
		Order("id ASC").Find(&monitors).Error; err != nil {
		return nil, ErrInternal(fmt.Errorf("listing the monitors to retag: %w", err))
	}
	if tag == "" {
		return monitors, nil
	}
	selected := make([]*models.Monitor, 0, len(monitors))
	for _, monitor := range monitors {
		if models.HasAnyTag(monitor.Tags, []string{tag}) {
			selected = append(selected, monitor)
		}
	}
	return selected, nil
}
