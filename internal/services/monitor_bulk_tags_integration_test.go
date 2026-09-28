// This file holds an OPT-IN integration check of the bulk tag operations
// (MonitorService.BulkUpdateTags and MonitorService.ListTags, see
// monitor_bulk_tags.go and docs/monitors.md): the selection resolves to exactly
// the rows the preview showed, a row goes through the ordinary monitor write path
// (so the link, the groups and the revision behave), a row that already carries
// the tags is not rewritten, and nothing else in the row moves.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database and it
// runs inside a transaction that is rolled back at the end. The usual way to run
// it:
//
//	docker run --rm -d --name up-scope-verify -p 127.0.0.1:3309:3306 \
//	  -e MARIADB_ROOT_PASSWORD=root -e MARIADB_DATABASE=up mariadb:11
//	UP_SCRATCH_DSN='root:root@tcp(127.0.0.1:3309)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/services/ -run TestBulkTagsLive -v
//	docker rm -f up-scope-verify
//
// Why it needs a real engine: the selection is ORed SQL over three indexes (ids,
// a subquery on the group members, a LIKE on the tag column) and the exact tag
// decision happens in Go after it, so only a real engine shows whether the union
// and the LIKE pre-filter really select the rows the report claims.
package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// TestBulkTagsLive pins the bulk tag edit end to end.
func TestBulkTagsLive(t *testing.T) {
	tx := scratchTx(t)
	ctx := context.Background()
	suffix := time.Now().UTC().Format("20060102150405.000000")
	monitors := NewMonitorService(tx, &config.Config{NodeID: "verify"},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	group := &models.MonitorGroup{Name: "zz-tags-group-" + suffix}
	if err := tx.Create(group).Error; err != nil {
		t.Fatalf("creating the group: %v", err)
	}
	// The tags are unique to this run: a shared scratch database cannot widen a
	// selection that names them.
	prod := "zz-prod-" + suffix
	makeMonitor := func(label, tags, templateUUID string) *models.Monitor {
		monitor := &models.Monitor{
			Name:            "zz-tags-" + label + "-" + suffix,
			Type:            models.MonitorTypeHTTP,
			Active:          false,
			IntervalSeconds: 300,
			TimeoutSeconds:  10,
			Tags:            tags,
			TemplateUUID:    templateUUID,
			Config:          models.MonitorConfig{URL: "http://127.0.0.1:9/" + label, Method: "GET"},
		}
		if err := monitors.Create(ctx, monitor, nil, nil); err != nil {
			t.Fatalf("creating the monitor %s: %v", label, err)
		}
		return monitor
	}
	tagged := makeMonitor("tagged", prod+",web", "zz-template-"+suffix)
	similar := makeMonitor("similar", prod+"x", "") // only LOOKS like the tag
	grouped := makeMonitor("grouped", "ops", "")    // untagged by the prod family
	plain := makeMonitor("plain", "", "")           // no tag at all
	if err := tx.Create(&models.MonitorGroupMember{GroupID: group.ID, MonitorID: grouped.ID}).Error; err != nil {
		t.Fatalf("linking the group member: %v", err)
	}

	// --- the selection: the tag is a whole tag, not a substring ---------------
	preview, changed, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{
		Tag: prod, Add: []string{"staging"}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("the dry run reported written ids: %v", changed)
	}
	if preview.Selected != 1 || preview.Updated != 1 || preview.Unchanged != 0 || preview.Failed != 0 {
		t.Fatalf("dry run = %+v, want only the monitor carrying the whole tag", preview)
	}
	if len(preview.Changes) != 1 || preview.Changes[0].Status != "dry_run" ||
		preview.Changes[0].Before != prod+",web" || preview.Changes[0].After != prod+",web,staging" {
		t.Fatalf("dry run change = %+v, want the before/after of the tagged monitor", preview.Changes)
	}
	if got := rowOf(t, tx, tagged.ID); got.Tags != prod+",web" {
		t.Errorf("the dry run wrote the tags: %q", got.Tags)
	}
	if got := rowOf(t, tx, similar.ID); got.Tags != prod+"x" {
		t.Errorf("the substring neighbour was touched: %q", got.Tags)
	}

	// --- the run --------------------------------------------------------------
	report, changed, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{Tag: prod, Add: []string{"staging"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Selected != 1 || report.Updated != 1 || report.Failed != 0 || len(changed) != 1 || changed[0] != tagged.ID {
		t.Fatalf("run = %+v (changed %v)", report, changed)
	}
	after := rowOf(t, tx, tagged.ID)
	if after.Tags != prod+",web,staging" {
		t.Fatalf("tags = %q, want the added tag appended", after.Tags)
	}
	// The tag edit is an ordinary monitor edit: everything else survives it, and
	// the revision advances (it is the merge order of the cluster, so a reader on
	// another node has to notice).
	if after.Name != tagged.Name || after.Config.URL != tagged.Config.URL || after.TemplateUUID != tagged.TemplateUUID {
		t.Fatalf("the tag edit touched more than the tags: %+v", after)
	}
	if after.Revision <= tagged.Revision {
		t.Fatalf("the tag edit did not advance the revision: %d -> %d", tagged.Revision, after.Revision)
	}

	// --- a rename, and the row that needs no write ----------------------------
	// Renaming a tag is the remove and the add of one request, selected by the tag
	// being renamed. Running it twice must not rewrite the rows the second time:
	// an untouched monitor must not advance its revision on every save.
	rename := BulkTagOptions{Tag: prod, Remove: []string{prod}, Add: []string{"production"}}
	first, _, err := monitors.BulkUpdateTags(ctx, rename)
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if first.Updated != 1 || first.Unchanged != 0 {
		t.Fatalf("rename = %+v", first)
	}
	if got := rowOf(t, tx, tagged.ID); got.Tags != "web,staging,production" {
		t.Fatalf("rename produced %q", got.Tags)
	}
	renamed := rowOf(t, tx, tagged.ID)
	// The same edit again, this time by id (the tag it renamed is gone from the
	// row, so only an explicit selection can name it now): the second run has
	// nothing left to do, so the row is NOT rewritten.
	second, changed, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{
		MonitorIDs: []uint{tagged.ID}, Remove: []string{prod}, Add: []string{"production"},
	})
	if err != nil {
		t.Fatalf("second rename: %v", err)
	}
	if second.Unchanged != 1 || second.Updated != 0 || len(changed) != 0 {
		t.Fatalf("the second rename = %+v (changed %v), want nothing left to do", second, changed)
	}
	if got := rowOf(t, tx, tagged.ID); got.Revision != renamed.Revision {
		t.Fatalf("an unchanged row was rewritten: revision %d -> %d", renamed.Revision, got.Revision)
	}
	// --- the union of the selectors, and the guards ---------------------------
	// Groups and explicit ids widen the selection the same way the tag does, and
	// the monitor of the group keeps its own tags while gaining the new one.
	byGroup, _, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{
		GroupIDs: []uint{group.ID}, Add: []string{"grouped"},
	})
	if err != nil {
		t.Fatalf("group run: %v", err)
	}
	if byGroup.Selected != 1 || byGroup.Updated != 1 {
		t.Fatalf("group run = %+v", byGroup)
	}
	if got := rowOf(t, tx, grouped.ID); got.Tags != "ops,grouped" {
		t.Fatalf("grouped tags = %q", got.Tags)
	}
	byID, _, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{
		MonitorIDs: []uint{plain.ID, plain.ID}, Add: []string{"manual"},
	})
	if err != nil {
		t.Fatalf("id run: %v", err)
	}
	// The same id twice is one monitor: the selection is a set.
	if byID.Selected != 1 || byID.Updated != 1 {
		t.Fatalf("id run = %+v", byID)
	}
	if got := rowOf(t, tx, plain.ID); got.Tags != "manual" {
		t.Fatalf("plain tags = %q", got.Tags)
	}
	if !memberOf(t, tx, group.ID, grouped.ID) {
		t.Fatal("the tag edit dropped the groups of the monitor")
	}

	// An empty selection is refused: a request that forgot a field must not retag
	// the whole installation, and a run without a tag to apply is a no-op that
	// would still count as a success.
	if _, _, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{Add: []string{"prod"}}); !isBadRequest(err) {
		t.Fatalf("a run without a selection = %v, want a 400", err)
	}
	if _, _, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{Tag: prod}); !isBadRequest(err) {
		t.Fatalf("a run without a tag to apply = %v, want a 400", err)
	}

	// --- the column limit, reported per row -----------------------------------
	// The tags live in a 255 character column: a row that would overflow it fails
	// alone, with a message the operator can place, and it is reported by the dry
	// run too (the preview must not promise an edit that then fails).
	long := makeMonitor("long", strings.Repeat("z", 200)+","+strings.Repeat("y", 30), "")
	overflow, changed, err := monitors.BulkUpdateTags(ctx, BulkTagOptions{
		MonitorIDs: []uint{long.ID}, Add: []string{strings.Repeat("x", 40)}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("overlong dry run: %v", err)
	}
	if overflow.Failed != 1 || overflow.Updated != 0 || len(changed) != 0 {
		t.Fatalf("overlong dry run = %+v (changed %v)", overflow, changed)
	}
	if len(overflow.Changes) != 1 || !strings.Contains(overflow.Changes[0].Error, "255") {
		t.Fatalf("overlong dry run change = %+v, want the column limit", overflow.Changes)
	}
	if got := rowOf(t, tx, long.ID); len(got.Tags) > models.MaxTagsLength {
		t.Fatalf("the row was written past the column limit: %d", len(got.Tags))
	}

	// --- the tag vocabulary ---------------------------------------------------
	usage, err := monitors.ListTags(ctx, string(models.MonitorTypeHTTP))
	if err != nil {
		t.Fatalf("listing the tags: %v", err)
	}
	byTag := make(map[string]int, len(usage))
	for _, item := range usage {
		byTag[item.Tag] = item.Monitors
	}
	if byTag["production"] != 1 || byTag["manual"] != 1 || byTag["grouped"] != 1 {
		t.Fatalf("the tag vocabulary does not report the tags written above: %+v", usage)
	}
	if _, reported := byTag[prod]; reported {
		t.Fatalf("the renamed tag is still reported: %+v", usage)
	}
	if _, reported := byTag["ops"]; !reported {
		t.Fatalf("the tag of the grouped monitor is missing: %+v", usage)
	}
}

// isBadRequest reports whether the error is the 400 the API answers with.
func isBadRequest(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest
}
