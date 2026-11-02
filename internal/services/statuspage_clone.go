package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// Sizes of the identifying columns of a page (models.StatusPage) and the two
// suffixes a derived name carries. A copy must fit in the columns: the suffix is cut
// out of the title and out of the slug, never the other way round, so a source at the
// limit still clones.
const (
	statusPageTitleMax        = 200
	statusPageSlugMax         = 120
	statusPageCloneSuffix     = " (copy)"
	statusPageCloneSlugSuffix = "-copy"
)

// StatusPageCloneOptions controls a status page clone.
type StatusPageCloneOptions struct {
	// Name is the title of the copy (empty means "<title> (copy)").
	Name string
	// Slug is the identifier of the copy (empty means "<slug>-copy").
	Slug string
	// CopyMonitors keeps the explicit monitor selection: which monitors, in which
	// order, with their section, their display name and their two switches.
	CopyMonitors bool
	// CopyGroups keeps the linked groups, in page order, with their display name.
	CopyGroups bool
}

// Clone duplicates a status page: every setting, every switch and — when the options
// say so — the two selections, with the overrides they carry.
//
// The copy deliberately stays behind:
//
//   - the identity: a fresh uuid, revision 1 and this node as the origin, exactly like
//     any other create, so the peers treat the copy as a new page and never as an edit
//     of the source;
//   - the subscribers: those are other people's addresses, and they opted in to the
//     page they subscribed to;
//   - the incidents and the notices: they describe outages of the source, and a copy
//     must not mail a history nobody lived through.
//
// Subscriptions travel switched OFF, with the SMTP channel and the delay kept as inert
// settings: a copy must never mail the subscribers of another page, and the switch
// being off is also what lets a copy succeed when the channel has not arrived from a
// peer yet (validateSubscription returns early on a disabled page).
func (s *StatusPageService) Clone(ctx context.Context, id uint, opts StatusPageCloneOptions) (*models.StatusPage, error) {
	source, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	title, slug := statusPageCloneIdentity(source, opts)
	slug, err = s.uniqueSlug(ctx, slug)
	if err != nil {
		return nil, err
	}

	var items []models.StatusPageMonitor
	rows, err := s.Items(ctx, id)
	if err != nil {
		return nil, err
	}
	if opts.CopyMonitors {
		items = statusPageCloneItems(rows)
	}
	var links []models.StatusPageGroupLink
	if opts.CopyGroups {
		links, err = s.Groups(ctx, id)
		if err != nil {
			return nil, err
		}
		links = statusPageCloneGroupLinks(links)
	}

	clone := statusPageClone(source, title, slug)
	// One transaction, one revision, one outbox row: the page and its two selections
	// reach the peers as the state the operator asked for, not as a page that is
	// edited twice afterwards.
	if err := s.createWithSelection(ctx, clone, items, links); err != nil {
		return nil, err
	}
	s.log.Info("status page cloned", "source", id, "clone", clone.ID, "slug", clone.Slug,
		"monitors", len(items), "groups", len(links))
	return s.Get(ctx, clone.ID)
}

// statusPageCloneIdentity settles the title and the slug of the copy, deriving them
// from the source when the caller does not name them. It is a pure function so the
// defaults — and the room left for the suffix — can be tested without a database.
func statusPageCloneIdentity(source *models.StatusPage, opts StatusPageCloneOptions) (string, string) {
	title := strings.TrimSpace(opts.Name)
	if title == "" {
		title = statusPageFitText(source.Title, statusPageTitleMax-len(statusPageCloneSuffix)) + statusPageCloneSuffix
	} else {
		title = statusPageFitText(title, statusPageTitleMax)
	}
	slug := strings.ToLower(strings.TrimSpace(opts.Slug))
	if slug == "" {
		base := statusPageFitText(source.Slug, statusPageSlugMax-len(statusPageCloneSlugSuffix))
		if base == "" {
			base = "page"
		}
		slug = strings.TrimRight(base, "-") + statusPageCloneSlugSuffix
	} else {
		// A cut slug that ends on a dash would not match slugPattern.
		slug = strings.TrimRight(statusPageFitText(slug, statusPageSlugMax), "-")
	}
	return title, slug
}

// statusPageFitText cuts a value to max runes, so a suffix still fits in its column.
// It cuts by runes and never by bytes: a title in a non-latin script must land on a
// character boundary.
func statusPageFitText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return strings.TrimSpace(string(runes[:max]))
}

// uniqueSlug appends a counter until the slug is free (used by the clone). Slugs are
// compared exactly, like the create does, because every slug written on the admin
// path is lower case already.
func (s *StatusPageService) uniqueSlug(ctx context.Context, base string) (string, error) {
	slug := base
	for attempt := 2; attempt < 100; attempt++ {
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.StatusPage{}).
			Where("slug = ?", slug).Count(&count).Error; err != nil {
			return "", ErrInternal(fmt.Errorf("checking the status page slug %q: %w", slug, err))
		}
		if count == 0 {
			return slug, nil
		}
		suffix := fmt.Sprintf("-%d", attempt)
		slug = strings.TrimRight(statusPageFitText(base, statusPageSlugMax-len(suffix)), "-") + suffix
	}
	return "", ErrConflict(i18n.CodeSlugTaken, fmt.Sprintf("could not find a free slug for %q", base))
}

// statusPageClone renders the row of a copy. It is a pure function so the one decision
// that matters (what travels and what does not) can be tested without a database:
// every setting travels, the identity is left blank for BeforeCreate, and the two
// selections are written by the caller.
func statusPageClone(source *models.StatusPage, title, slug string) *models.StatusPage {
	return &models.StatusPage{
		Slug:                     slug,
		Title:                    title,
		Description:              source.Description,
		FooterText:               source.FooterText,
		Theme:                    source.Theme,
		IsPublic:                 source.IsPublic,
		ShowUptime:               source.ShowUptime,
		ShowCharts:               source.ShowCharts,
		ShowTags:                 source.ShowTags,
		ShowCertExpiry:           source.ShowCertExpiry,
		ShowDomainExpiry:         source.ShowDomainExpiry,
		UptimeWindowHours:        source.UptimeWindowHours,
		CustomCSS:                source.CustomCSS,
		SubscriptionEnabled:      false,
		SubscriptionSMTPUUID:     source.SubscriptionSMTPUUID,
		SubscriptionDelayMinutes: source.SubscriptionDelayMinutes,
	}
}

// statusPageCloneItems renders the selection rows of the copy: the same monitors, in
// the same order, with the overrides of the page (display name, section and the two
// switches). The row identity and the link to the source page stay behind.
func statusPageCloneItems(rows []models.StatusPageMonitor) []models.StatusPageMonitor {
	items := make([]models.StatusPageMonitor, 0, len(rows))
	for _, row := range rows {
		if row.MonitorID == 0 {
			continue
		}
		items = append(items, models.StatusPageMonitor{
			MonitorID:   row.MonitorID,
			DisplayName: row.DisplayName,
			GroupName:   row.GroupName,
			ShowUptime:  row.ShowUptime,
			ShowChart:   row.ShowChart,
		})
	}
	return items
}

// statusPageCloneGroupLinks renders the group links of the copy: the same groups, in
// page order, with the display names the source gave them.
func statusPageCloneGroupLinks(rows []models.StatusPageGroupLink) []models.StatusPageGroupLink {
	links := make([]models.StatusPageGroupLink, 0, len(rows))
	for _, row := range rows {
		if row.GroupID == 0 {
			continue
		}
		links = append(links, models.StatusPageGroupLink{
			GroupID:     row.GroupID,
			DisplayName: row.DisplayName,
		})
	}
	return links
}
