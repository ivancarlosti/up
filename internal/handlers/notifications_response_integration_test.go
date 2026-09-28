// This file holds an OPT-IN integration check of the two channel endpoints that
// write: POST and PUT /api/notifications (see notifications.go and docs/api.md).
//
// It is the regression test of a bug that shipped. The dialog never sends the
// monitor links (they are made from the monitor editor), so the handlers used to
// answer with the very request they had just bound - a payload without
// `monitor_ids`, which the JSON tag of a nil slice writes as null. The card of
// Admin > Notifications reads that field unguarded, so `null.length` threw inside
// its render and Vue answered a failed render with an empty placeholder: editing a
// channel made its box disappear from the page, delivery history included, and the
// channel looked deleted.
//
// It is skipped unless UP_SCRATCH_DSN points at a MariaDB/MySQL database. Every row
// it writes lives inside a transaction that is rolled back at the end, and the
// tables it needs are created when they are missing. The usual way to run it:
//
//	docker run --rm -d --name up-channel-response-verify -p 127.0.0.1:3310:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up mariadb:11
//	UP_SCRATCH_DSN='root:verify@tcp(127.0.0.1:3310)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/handlers/ -run TestNotificationResponseMonitorIDsLive -v
//	docker rm -f up-channel-response-verify
//
// Why it needs a real engine and the real handlers: the answer is only right when
// the handler reads the channel back, which is a database round trip, and what is
// asserted is the JSON of that answer - the bytes the page consumes - rather than
// the call behind it.
package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// TestNotificationResponseMonitorIDsLive pins what the two writing endpoints
// answer: the stored channel, with `monitor_ids` as an array - `[]` for a channel
// nobody listens to, the linked monitors otherwise - and the persisted timestamps,
// since the dialog swaps that object into its list as-is.
func TestNotificationResponseMonitorIDsLive(t *testing.T) {
	dsn := os.Getenv("UP_SCRATCH_DSN")
	if dsn == "" {
		t.Skip("UP_SCRATCH_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		NowFunc:                func() time.Time { return time.Now().UTC() },
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.AutoMigrate(&models.Notification{}, &models.Monitor{}, &models.MonitorNotification{}); err != nil {
		t.Fatalf("migrating the scratch database: %v", err)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("starting the transaction: %v", tx.Error)
	}
	defer func() {
		if err := tx.Rollback().Error; err != nil {
			t.Errorf("rolling back: %v", err)
		}
	}()

	gin.SetMode(gin.TestMode)
	cfg := &config.Config{NodeID: "verify"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	container := &Container{
		Cfg:           cfg,
		Log:           log,
		Notifications: services.NewNotificationService(tx, cfg, log, nil),
	}

	suffix := time.Now().UTC().Format("20060102150405.000000")
	// The payload of the dialog for a webhook channel, monitor_ids left out on
	// purpose: making up for that absence is the whole job of the handlers.
	payload := fmt.Sprintf(`{"name":"zz-verify-response-%s","type":"webhook","active":true,`+
		`"resend_interval_seconds":300,"config":{"webhook":{"url":"https://hooks.example.com/up"}}}`, suffix)

	status, created := channelResponse(t, container.createNotification, http.MethodPost, "", payload)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/notifications answered %d: %v", status, created)
	}
	// Before the fix this field was null: the handler echoed the request.
	assertMonitorIDsWire(t, "POST /api/notifications", created, nil)
	if !hasTimestamp(t, created, "created_at") {
		t.Errorf("POST /api/notifications answered created_at=%s, want the stored instant",
			string(created["created_at"]))
	}
	var id uint
	if err := json.Unmarshal(created["id"], &id); err != nil || id == 0 {
		t.Fatalf("POST /api/notifications answered id=%s: %v", string(created["id"]), err)
	}

	// The links are made from the monitor editor, as a row of monitor_notifications.
	monitor := &models.Monitor{Name: "zz-verify-response-monitor-" + suffix, Type: models.MonitorTypeHTTP}
	if err := tx.Create(monitor).Error; err != nil {
		t.Fatalf("creating the monitor: %v", err)
	}
	link := map[string]any{
		"monitor_id":      monitor.ID,
		"notification_id": id,
		"on_down":         true,
		"on_up":           true,
	}
	if err := tx.Model(&models.MonitorNotification{}).Create(link).Error; err != nil {
		t.Fatalf("linking the channel: %v", err)
	}

	renamed := fmt.Sprintf(`{"name":"zz-verify-response-renamed-%s","type":"webhook","active":true,`+
		`"resend_interval_seconds":300,"config":{"webhook":{"url":"https://hooks.example.com/up"}}}`, suffix)
	status, saved := channelResponse(t, container.updateNotification, http.MethodPut,
		strconv.FormatUint(uint64(id), 10), renamed)
	if status != http.StatusOK {
		t.Fatalf("PUT /api/notifications/%d answered %d: %v", id, status, saved)
	}
	// The edit is the case that regressed on screen: the box of the channel left the
	// page and only the delivery history stayed.
	assertMonitorIDsWire(t, fmt.Sprintf("PUT /api/notifications/%d", id), saved, []uint{monitor.ID})
	var name string
	if err := json.Unmarshal(saved["name"], &name); err != nil {
		t.Fatalf("decoding the name of the answer: %v", err)
	}
	if name != "zz-verify-response-renamed-"+suffix {
		t.Errorf("PUT answered name=%q, want the new one", name)
	}
	if !hasTimestamp(t, saved, "created_at") || !hasTimestamp(t, saved, "updated_at") {
		t.Errorf("PUT answered created_at=%s updated_at=%s, want the stored instants",
			string(saved["created_at"]), string(saved["updated_at"]))
	}
	// The links belong to the monitor side: the write must not have dropped them.
	var rows int64
	if err := tx.Model(&models.MonitorNotification{}).
		Where("notification_id = ?", id).Count(&rows).Error; err != nil {
		t.Fatalf("counting the links: %v", err)
	}
	if rows != 1 {
		t.Errorf("the channel has %d links left, want 1", rows)
	}
}

// channelResponse runs a writing channel handler against a JSON body and returns
// the status and the decoded answer, the way the API writes it. id is the :id path
// parameter, empty for a create.
func channelResponse(t *testing.T, handler gin.HandlerFunc, method, id, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, "/api/notifications", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	if id != "" {
		ctx.Params = gin.Params{{Key: "id", Value: id}}
	}
	handler(ctx)

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s answered %d with %q, which is not a JSON object: %v",
			method, recorder.Code, recorder.Body.String(), err)
	}
	return recorder.Code, decoded
}

// assertMonitorIDsWire pins the wire shape of `monitor_ids`: an array, never the
// null a nil slice marshals to, and the ids of want when one is given.
func assertMonitorIDsWire(t *testing.T, what string, body map[string]json.RawMessage, want []uint) {
	t.Helper()
	raw, ok := body["monitor_ids"]
	if !ok {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("%s answered no monitor_ids at all: %v", what, err)
		}
		t.Fatalf("%s answered no monitor_ids at all: %s", what, encoded)
	}
	if !strings.HasPrefix(string(raw), "[") {
		t.Fatalf("%s answered monitor_ids=%s, want a JSON array (the page reads .length off it)", what, raw)
	}
	var ids []uint
	if err := json.Unmarshal(raw, &ids); err != nil {
		t.Fatalf("%s answered monitor_ids=%s, which is not a list of ids: %v", what, raw, err)
	}
	if len(ids) != len(want) {
		t.Errorf("%s answered monitor_ids=%s, want %v", what, raw, want)
		return
	}
	for index, id := range want {
		if ids[index] != id {
			t.Errorf("%s answered monitor_ids=%s, want %v", what, raw, want)
			return
		}
	}
}

// hasTimestamp reports whether the field carries a real instant, and not the zero
// value of the bound request the handler would echo back.
func hasTimestamp(t *testing.T, body map[string]json.RawMessage, field string) bool {
	t.Helper()
	var instant time.Time
	if err := json.Unmarshal(body[field], &instant); err != nil {
		t.Fatalf("decoding %s=%s: %v", field, string(body[field]), err)
	}
	return !instant.IsZero()
}
