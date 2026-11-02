// This file holds an OPT-IN integration check of the MANUAL ORDER of a status page
// (StatusPageService.SetOrder, see statuspage_order.go and docs/status-pages.md):
//
//   - the positions are per section: the explicit monitors are numbered from zero and
//     every group section from zero again, so the setting never moves a monitor out of
//     its group;
//   - a group member gets an ORDER-ONLY row, which stores its position without
//     publishing it (the public payload still renders it inside its group, once);
//   - the public payload follows the saved order, and its `monitors` list keeps the
//     order of the sections;
//   - "alphabetical" and "type" order inside the section and fall back to the manual
//     order, so switching the mode back to manual loses nothing;
//   - an id that is not on the page refuses the whole request, and the page keeps the
//     order it had;
//   - every save is a new version of the page (revision + outbox), and a clone carries
//     the positions with it.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every row it
// writes lives inside a transaction that is rolled back at the end. The database must
// already be migrated (the application creates the schema on boot). The usual way to
// run it:
//
//	docker run --rm -d --name up-page-order-verify -p 127.0.0.1:3309:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestStatusPageOrderLive -v
//	docker rm -f up-page-order-verify
//
// Why it needs a real engine: the ORDER-ONLY row collides with the explicit one on the
// unique index (status_page_id, monitor_id) when a monitor becomes an explicit
// selection, the two `default:true` columns are filled by the ENGINE, and "the page
// keeps the version it had" is a statement about rows no pure helper can show.
package services

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// TestStatusPageOrderLive pins the manual order of a status page end to end.
func TestStatusPageOrderLive(t *testing.T) {
	tx := scratchTx(t)
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102150405.000000")
	slugStamp := strings.ReplaceAll(stamp, ".", "-")
	cfg := &config.Config{NodeID: "verify", ClusterMode: config.ClusterModeFederated}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	pages := NewStatusPageService(tx, cfg, log)
	pages.SetSyncEmitter(NewSyncEmitter(tx, cfg, log))
	monitors := NewMonitorService(tx, cfg, log, NewStatsService(tx))
	pages.SetMonitorService(monitors)
	groups := NewMonitorGroupService(tx, cfg, log)

	// Three real monitors with two types: "alpha" is the explicit selection and
	// "zulu"/"mike" are the members of the group, so both sections have something to
	// order and "by type" has something to group.
	createMonitor := func(name string, monitorType models.MonitorType) *models.Monitor {
		monitor := &models.Monitor{
			Name: "zz-order-" + name + "-" + stamp, Type: monitorType,
			IntervalSeconds: 120, TimeoutSeconds: 20,
			Config: models.MonitorConfig{URL: "http://127.0.0.1:9/" + name, Method: "HEAD"},
		}
		if monitorType == models.MonitorTypeTCP {
			monitor.Config = models.MonitorConfig{Host: "127.0.0.1", Port: 9}
		}
		if err := monitors.Create(ctx, monitor, nil, nil); err != nil {
			t.Fatalf("creating monitor %q: %v", monitor.Name, err)
		}
		return monitor
	}
	alpha := createMonitor("alpha", models.MonitorTypeHTTP)
	zulu := createMonitor("zulu", models.MonitorTypeTCP)
	mike := createMonitor("mike", models.MonitorTypeHTTP)

	group := &models.MonitorGroup{Name: "zz-order-edge-" + stamp, Description: "the ordered group"}
	if err := groups.Create(ctx, group, []uint{zulu.ID, mike.ID}); err != nil {
		t.Fatalf("creating the group: %v", err)
	}

	page := &models.StatusPage{
		Slug: "zz-order-page-" + slugStamp, Title: "ZZ order page " + stamp,
		IsPublic: true, ShowUptime: true, ShowCharts: true,
	}
	if err := pages.Create(ctx, page, []uint{alpha.ID}); err != nil {
		t.Fatalf("creating the page: %v", err)
	}
	if err := pages.SetGroups(ctx, page.ID,
		[]models.StatusPageGroupLink{{GroupID: group.ID, DisplayName: "Edge"}}); err != nil {
		t.Fatalf("linking the group: %v", err)
	}
	if page.MonitorOrder != models.MonitorOrderManual {
		t.Fatalf("a new page must start in the manual order, got %q", page.MonitorOrder)
	}

	// namesOf renders the sections of the public payload as comma separated names, which
	// is what an operator reads on the page; flatOf does the same for `page.monitors`.
	namesOf := func(page *models.StatusPage) []string {
		out := make([]string, 0, len(page.Groups))
		for _, section := range page.Groups {
			names := make([]string, 0, len(section.Monitors))
			for _, monitor := range section.Monitors {
				names = append(names, monitor.Name)
			}
			out = append(out, strings.Join(names, ","))
		}
		return out
	}
	flatOf := func(page *models.StatusPage) string {
		names := make([]string, 0, len(page.Monitors))
		for _, monitor := range page.Monitors {
			names = append(names, monitor.Name)
		}
		return strings.Join(names, ",")
	}
	// payload rebuilds the part of the public payload this test is about — the sections
	// in page order — from the rows in the database.
	//
	// The decorated payload itself (status, uptime, bars) cannot be read here: the
	// decoration queries concurrently and a transaction of the MySQL driver is bound to
	// one connection. The ordering is asserted on exactly what the payload renders:
	// statusPageSections (the plan) and applyStatusPageOrder (the mode), which are the
	// two calls PublicPayload makes.
	renderPage := func(pageID uint) *models.StatusPage {
		t.Helper()
		loaded, err := pages.Get(ctx, pageID)
		if err != nil {
			t.Fatalf("reading the page: %v", err)
		}
		sections, err := pages.statusPageSections(ctx, pageID)
		if err != nil {
			t.Fatalf("reading the plan: %v", err)
		}
		items, err := pages.Items(ctx, pageID)
		if err != nil {
			t.Fatalf("reading the selection: %v", err)
		}
		ranks := make(map[uint]int, len(items))
		for _, item := range items {
			ranks[item.MonitorID] = item.SortOrder
		}
		// The monitors to load are the ones the PLAN renders, not the ones with a row:
		// a group member has no row until someone positions it.
		ids := make([]uint, 0, len(items))
		for _, section := range sections {
			ids = append(ids, section.MonitorIDs...)
		}
		var rows []*models.Monitor
		if err := tx.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
			t.Fatalf("reading the monitors: %v", err)
		}
		monitors := make(map[uint]*models.Monitor, len(rows))
		for _, monitor := range rows {
			monitors[monitor.ID] = monitor
		}
		sections, flat := applyStatusPageOrder(sections, monitors, ranks, loaded.MonitorOrder)
		rendered := &models.StatusPage{MonitorOrder: loaded.MonitorOrder}
		for _, id := range flat {
			if monitor, ok := monitors[id]; ok {
				rendered.Monitors = append(rendered.Monitors, monitor)
			}
		}
		for _, section := range sections {
			group := models.StatusPageGroup{Name: section.Name, Monitors: []*models.Monitor{}}
			for _, id := range section.MonitorIDs {
				if monitor, ok := monitors[id]; ok {
					group.Monitors = append(group.Monitors, monitor)
				}
			}
			rendered.Groups = append(rendered.Groups, group)
		}
		return rendered
	}
	payload := func() *models.StatusPage { return renderPage(page.ID) }

	// --- before anybody drags anything ---------------------------------------
	// The group renders in the order of its membership (zulu then mike) and the
	// explicit section first: no ORDER-ONLY row exists yet.
	if got := namesOf(payload()); len(got) != 2 || got[0] != alpha.Name ||
		got[1] != zulu.Name+","+mike.Name {
		t.Fatalf("the initial order must be the plan, got %v", got)
	}

	// --- the order an operator dragged ---------------------------------------
	// "mike" goes above "zulu" inside the group: neither of them is an explicit
	// selection, so both positions are ORDER-ONLY rows.
	before, err := pages.Get(ctx, page.ID)
	if err != nil {
		t.Fatalf("reading the page: %v", err)
	}
	outboxBefore := countRows(t, tx, &models.SyncOutbox{}, "entity = ? AND uuid = ?",
		models.EntityStatusPage, page.UUID)
	if err := pages.SetOrder(ctx, page.ID, []uint{alpha.ID, mike.ID, zulu.ID}); err != nil {
		t.Fatalf("saving the order: %v", err)
	}
	after, err := pages.Get(ctx, page.ID)
	if err != nil {
		t.Fatalf("re-reading the page: %v", err)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("saving the order must be a new version: %d -> %d", before.Revision, after.Revision)
	}
	if got := countRows(t, tx, &models.SyncOutbox{}, "entity = ? AND uuid = ?",
		models.EntityStatusPage, page.UUID); got != outboxBefore+1 {
		t.Fatalf("the new version must reach the outbox once, got %d (was %d)", got, outboxBefore)
	}

	items, err := pages.Items(ctx, page.ID)
	if err != nil {
		t.Fatalf("reading the selection: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 rows (1 explicit + 2 order-only), got %+v", items)
	}
	byMonitor := map[uint]models.StatusPageMonitor{}
	for _, item := range items {
		byMonitor[item.MonitorID] = item
	}
	if row := byMonitor[alpha.ID]; row.OrderOnly || row.SortOrder != 0 {
		t.Fatalf("the explicit monitor must keep its selection row: %+v", row)
	}
	if row := byMonitor[mike.ID]; !row.OrderOnly || row.SortOrder != 0 {
		t.Fatalf("the first group member must be an order-only row at 0: %+v", row)
	}
	if row := byMonitor[zulu.ID]; !row.OrderOnly || row.SortOrder != 1 {
		t.Fatalf("the second group member must be an order-only row at 1: %+v", row)
	}

	dragged := payload()
	if want := alpha.Name + "," + mike.Name + "," + zulu.Name; flatOf(dragged) != want {
		t.Fatalf("the flat list must follow the saved order, got %q want %q", flatOf(dragged), want)
	}
	if got := namesOf(dragged); got[1] != mike.Name+","+zulu.Name {
		t.Fatalf("the group section must follow the saved order, got %v", got)
	}

	// --- the other two modes ------------------------------------------------
	// "zulu" goes above "mike", so the three modes can be told apart: manual keeps the
	// drag, "alphabetical" sorts by the name the page shows and "type" puts the http
	// monitor before the TCP one.
	if err := pages.SetOrder(ctx, page.ID, []uint{alpha.ID, zulu.ID, mike.ID}); err != nil {
		t.Fatalf("reordering the group: %v", err)
	}
	if got := namesOf(payload()); got[1] != zulu.Name+","+mike.Name {
		t.Fatalf("the manual order must keep the drag, got %v", got)
	}
	setMode := func(mode string) {
		t.Helper()
		loaded, err := pages.Get(ctx, page.ID)
		if err != nil {
			t.Fatalf("reading the page: %v", err)
		}
		loaded.MonitorOrder = mode
		if err := pages.Update(ctx, loaded); err != nil {
			t.Fatalf("saving the mode %q: %v", mode, err)
		}
	}
	setMode(models.MonitorOrderAlphabetical)
	alphabetical := payload()
	if alphabetical.MonitorOrder != models.MonitorOrderAlphabetical {
		t.Fatalf("the mode must be stored, got %q", alphabetical.MonitorOrder)
	}
	if got := namesOf(alphabetical); got[1] != mike.Name+","+zulu.Name {
		t.Fatalf("alphabetical must sort the section by name, got %v", got)
	}
	setMode(models.MonitorOrderType)
	if got := namesOf(payload()); got[1] != mike.Name+","+zulu.Name {
		t.Fatalf("by type must put the http monitor first, got %v", got)
	}
	// Back to manual: the drag is still there, because the other two modes never
	// overwrote it.
	setMode(models.MonitorOrderManual)
	if got := namesOf(payload()); got[1] != zulu.Name+","+mike.Name {
		t.Fatalf("the manual order must survive the other modes, got %v", got)
	}

	// --- an id that is not on the page refuses the whole request -------------
	if err := pages.SetOrder(ctx, page.ID, []uint{alpha.ID, zulu.ID, mike.ID, mike.ID + 100000}); err == nil {
		t.Fatal("an id that is not on the page must be refused")
	}
	if got := namesOf(payload()); got[1] != zulu.Name+","+mike.Name {
		t.Fatalf("a refused order must change nothing, got %v", got)
	}

	// --- a clone carries the positions ---------------------------------------
	clone, err := pages.Clone(ctx, page.ID, StatusPageCloneOptions{CopyMonitors: true, CopyGroups: true})
	if err != nil {
		t.Fatalf("cloning the page: %v", err)
	}
	copied := renderPage(clone.ID)
	if got, want := strings.Join(namesOf(copied), " | "), strings.Join(namesOf(payload()), " | "); got != want {
		t.Fatalf("the copy must render the same order: got %q want %q", got, want)
	}
	if got := flatOf(copied); got != flatOf(payload()) {
		t.Fatalf("the copy flat list must match the source, got %q", got)
	}
	if cloned := countRows(t, tx, &models.StatusPageMonitor{},
		"status_page_id = ? AND order_only = ?", clone.ID, true); cloned != 2 {
		t.Fatalf("the copy must keep the two order-only rows, got %d", cloned)
	}

	// --- the selection no longer wipes the positions -------------------------
	// Replacing the explicit selection speaks about the explicit monitors only: the
	// ORDER-ONLY rows of the group must survive it.
	if err := pages.SetMonitors(ctx, page.ID, []uint{alpha.ID}); err != nil {
		t.Fatalf("replacing the selection: %v", err)
	}
	if got := countRows(t, tx, &models.StatusPageMonitor{},
		"status_page_id = ? AND order_only = ?", page.ID, true); got != 2 {
		t.Fatalf("the selection must not delete the order-only rows, got %d", got)
	}
	if got := namesOf(payload()); got[1] != zulu.Name+","+mike.Name {
		t.Fatalf("the order must survive a selection edit, got %v", got)
	}

	// A member that becomes an explicit selection takes over the row: the unique index
	// (status_page_id, monitor_id) is what would blow up if it did not.
	if err := pages.SetMonitors(ctx, page.ID, []uint{alpha.ID, mike.ID}); err != nil {
		t.Fatalf("publishing a group member explicitly: %v", err)
	}
	if row := countRows(t, tx, &models.StatusPageMonitor{},
		"status_page_id = ? AND monitor_id = ? AND order_only = ?", page.ID, mike.ID, true); row != 0 {
		t.Fatalf("the order-only row of an explicit monitor must be gone, got %d", row)
	}
	published := payload()
	if flat := flatOf(published); !strings.HasPrefix(flat, alpha.Name+","+mike.Name) {
		t.Fatalf("the explicit section must hold the two selections, got %q", flat)
	}
	if got := namesOf(published); got[1] != zulu.Name {
		t.Fatalf("the group must keep only its remaining member, got %v", got)
	}
}
