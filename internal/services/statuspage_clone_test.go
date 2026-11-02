package services

import (
	"strings"
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// cloneSource is the page the guards below copy: every setting away from its
// default, so a field the clone forgets shows up as a mismatch instead of a
// coincidence.
func cloneSource() *models.StatusPage {
	return &models.StatusPage{
		ID:                       9,
		UUID:                     "source-uuid",
		OriginNodeID:             "other-node",
		Revision:                 4,
		Slug:                     "main",
		Title:                    "Service status",
		Description:              "what we run",
		FooterText:               "all times are UTC",
		Theme:                    "dark",
		IsPublic:                 false,
		ShowUptime:               false,
		ShowCharts:               true,
		ShowTags:                 true,
		ShowCertExpiry:           true,
		ShowDomainExpiry:         false,
		UptimeWindowHours:        168,
		CustomCSS:                ".brand { color: red }",
		SubscriptionEnabled:      true,
		SubscriptionSMTPUUID:     "smtp-channel-uuid",
		SubscriptionDelayMinutes: 30,
	}
}

// TestStatusPageClone is the guard of the clone: every setting travels (switches
// included, so a private page is not copied public), the identity does not, and the
// subscription configuration travels INERT — the channel and the delay are kept, the
// switch is not.
func TestStatusPageClone(t *testing.T) {
	source := cloneSource()

	clone := statusPageClone(source, "Service status (copy)", "main-copy")
	if clone.ID != 0 || clone.UUID != "" || clone.Revision != 0 || clone.OriginNodeID != "" ||
		!clone.CreatedAt.IsZero() || !clone.UpdatedAt.IsZero() {
		t.Fatalf("the identity must be left to BeforeCreate: %+v", clone)
	}
	if clone.Slug != "main-copy" || clone.Title != "Service status (copy)" {
		t.Fatalf("the copy must take the settled name, got %q / %q", clone.Title, clone.Slug)
	}
	if clone.Description != source.Description || clone.FooterText != source.FooterText ||
		clone.Theme != source.Theme || clone.CustomCSS != source.CustomCSS ||
		clone.UptimeWindowHours != source.UptimeWindowHours {
		t.Fatalf("the settings did not travel: %+v", clone)
	}
	// The three `default:true` booleans and the two opt-in badges: a false must stay
	// false, or a private page would be copied public.
	if clone.IsPublic || clone.ShowUptime || !clone.ShowCharts || !clone.ShowTags ||
		!clone.ShowCertExpiry || clone.ShowDomainExpiry {
		t.Fatalf("the switches did not travel: %+v", clone)
	}
	if clone.SubscriptionEnabled {
		t.Fatal("a copy must never start with subscriptions on: it would mail the subscribers of another page")
	}
	if clone.SubscriptionSMTPUUID != source.SubscriptionSMTPUUID ||
		clone.SubscriptionDelayMinutes != source.SubscriptionDelayMinutes {
		t.Fatalf("the subscription configuration must travel inert: %q / %d",
			clone.SubscriptionSMTPUUID, clone.SubscriptionDelayMinutes)
	}
	if clone.ID == source.ID || clone.UUID == source.UUID {
		t.Fatal("the copy must not share the identity of the source")
	}
}

// TestStatusPageCloneIdentity pins the two names of a copy: the defaults derived from
// the source, the caller's own values, and the room the suffix must keep in the two
// columns (the suffix is cut out of the title and the slug, never the other way
// round, so a source at the limit still clones instead of failing on an insert).
func TestStatusPageCloneIdentity(t *testing.T) {
	source := cloneSource()

	cases := []struct {
		name      string
		source    *models.StatusPage
		opts      StatusPageCloneOptions
		wantTitle string
		wantSlug  string
	}{
		{
			name:      "derived from the source",
			source:    source,
			wantTitle: "Service status (copy)",
			wantSlug:  "main-copy",
		},
		{
			name:      "the caller names the copy",
			source:    source,
			opts:      StatusPageCloneOptions{Name: "  Status of the API  ", Slug: "  API-Status  "},
			wantTitle: "Status of the API",
			wantSlug:  "api-status",
		},
		{
			name: "the title is cut, the suffix is kept",
			source: &models.StatusPage{
				Slug:  "main",
				Title: strings.Repeat("t", statusPageTitleMax+20),
			},
			wantTitle: strings.Repeat("t", statusPageTitleMax-len(statusPageCloneSuffix)) + statusPageCloneSuffix,
			wantSlug:  "main-copy",
		},
		{
			name: "the slug is cut, the suffix is kept",
			source: &models.StatusPage{
				Slug:  strings.Repeat("s", statusPageSlugMax+20),
				Title: "T",
			},
			wantTitle: "T (copy)",
			wantSlug:  strings.Repeat("s", statusPageSlugMax-len(statusPageCloneSlugSuffix)) + statusPageCloneSlugSuffix,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, slug := statusPageCloneIdentity(tc.source, tc.opts)
			if title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", title, tc.wantTitle)
			}
			if slug != tc.wantSlug {
				t.Fatalf("slug = %q, want %q", slug, tc.wantSlug)
			}
			// Whatever the input, the two names fit their column.
			if len([]rune(title)) > statusPageTitleMax {
				t.Fatalf("the title must fit %d runes, got %d", statusPageTitleMax, len([]rune(title)))
			}
			if len(slug) > statusPageSlugMax {
				t.Fatalf("the slug must fit %d characters, got %d", statusPageSlugMax, len(slug))
			}
			if tc.opts.Slug == "" && !strings.HasSuffix(slug, statusPageCloneSlugSuffix) {
				t.Fatalf("the derived slug must keep the suffix, got %q", slug)
			}
		})
	}

	// An explicit name is never given the default suffix, and an explicit slug that
	// is only a counter never keeps a trailing dash (it would not match slugPattern).
	if title, slug := statusPageCloneIdentity(source, StatusPageCloneOptions{Name: "Copy", Slug: "copy-"}); title != "Copy" || slug != "copy" {
		t.Fatalf("the caller's names must be used as they are, got %q / %q", title, slug)
	}
}

// TestStatusPageCloneSelection pins what the two selections keep: the order, the
// per-monitor overrides (name, section and the two switches) and the display name of
// a group. The row identity and the link to the source page stay behind.
func TestStatusPageCloneSelection(t *testing.T) {
	items := statusPageCloneItems([]models.StatusPageMonitor{
		{ID: 11, StatusPageID: 9, MonitorID: 3, DisplayName: "API", GroupName: "edge", SortOrder: 0, ShowUptime: false, ShowChart: true},
		{ID: 12, StatusPageID: 9, MonitorID: 5, DisplayName: "", GroupName: "", SortOrder: 1, ShowUptime: true, ShowChart: false},
		{ID: 13, StatusPageID: 9, MonitorID: 0, SortOrder: 2},
	})
	if len(items) != 2 {
		t.Fatalf("a row without a monitor must be dropped, got %d rows", len(items))
	}
	if items[0].MonitorID != 3 || items[1].MonitorID != 5 {
		t.Fatalf("the order must travel: %+v", items)
	}
	if items[0].DisplayName != "API" || items[0].GroupName != "edge" || items[0].ShowUptime || !items[0].ShowChart {
		t.Fatalf("the overrides of the first row did not travel: %+v", items[0])
	}
	if !items[1].ShowUptime || items[1].ShowChart {
		t.Fatalf("the switches of the second row did not travel: %+v", items[1])
	}
	for _, item := range items {
		if item.ID != 0 || item.StatusPageID != 0 {
			t.Fatalf("the row identity must be left to the writer: %+v", item)
		}
	}

	links := statusPageCloneGroupLinks([]models.StatusPageGroupLink{
		{ID: 7, StatusPageID: 9, GroupID: 2, DisplayName: "Edge", SortOrder: 0, GroupName: "edge"},
		{ID: 8, StatusPageID: 9, GroupID: 4, SortOrder: 1, GroupName: "core"},
		{ID: 9, StatusPageID: 9, GroupID: 0, SortOrder: 2},
	})
	if len(links) != 2 {
		t.Fatalf("a link without a group must be dropped, got %d links", len(links))
	}
	if links[0].GroupID != 2 || links[0].DisplayName != "Edge" || links[1].GroupID != 4 {
		t.Fatalf("the links must travel in page order, with their display names: %+v", links)
	}
	for _, link := range links {
		if link.ID != 0 || link.StatusPageID != 0 || link.GroupName != "" {
			t.Fatalf("the row identity and the resolved name must be left behind: %+v", link)
		}
	}
}

// TestStatusPageMonitorRows pins the plain id list both the create and the selection
// endpoint write: both switches on, so an id list never depends on the writer's zero
// values (which the `default:true` columns would turn into true anyway).
func TestStatusPageMonitorRows(t *testing.T) {
	rows := statusPageMonitorRows([]uint{3, 5})
	if len(rows) != 2 {
		t.Fatalf("want one row per id, got %d", len(rows))
	}
	for i, row := range rows {
		if row.MonitorID != []uint{3, 5}[i] || !row.ShowUptime || !row.ShowChart {
			t.Fatalf("unexpected row %d: %+v", i, row)
		}
		if row.StatusPageID != 0 || row.SortOrder != 0 {
			t.Fatalf("the writer assigns the page and the order: %+v", row)
		}
	}
}
