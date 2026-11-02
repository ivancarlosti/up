// This file holds an OPT-IN end to end check of the hardware monitoring path: a
// simulated Beszel agent dials the real handler, authenticates with the token and
// the host key, is polled, and its sample ends up in the history the dashboard
// reads.
//
// It is skipped unless UP_SCRATCH_DSN points at a migrated MariaDB/MySQL database
// (the application creates the schema on boot):
//
//	docker run --rm -d --name up-agent-verify -p 127.0.0.1:3307:3306 \
//	  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
//	  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
//	# boot the server once against it, so the schema exists
//	UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC' \
//	  go test ./internal/handlers/ -run TestAgentEndToEnd -v
//
// Why it is worth the database: the pieces that fail in production are the ones a
// unit test cannot reach - the upgrade, the CBOR handshake over a real socket, the
// lease, the first poll and the write of the sample in one transaction.
package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/services"
)

// scratchHarness is the minimum wiring the agent path needs: the settings (the
// host key is derived from the cluster key they hold), the cluster (liveness and
// the settle rule) and the systems service itself.
type scratchHarness struct {
	db      *gorm.DB
	cfg     *config.Config
	systems *services.SystemService
	server  *httptest.Server
}

// newScratchHarness opens the scratch database and starts the agent endpoint.
func newScratchHarness(t *testing.T) *scratchHarness {
	t.Helper()
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
	if !db.Migrator().HasTable(&models.System{}) {
		t.Skip("the scratch database is not migrated: boot the server against it once")
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{NodeID: "up-agent-e2e", NodeName: "e2e", AppURL: "http://127.0.0.1:0"}
	settings := services.NewSettingService(db, cfg, log)
	cluster := services.NewClusterService(db, cfg, log, settings, services.NewStatsService(db))
	systems := services.NewSystemService(db, cfg, log)
	systems.SetSettingService(settings)
	systems.SetClusterService(cluster)

	// A node row is what the lease consults for liveness.
	now := time.Now().UTC()
	if err := db.Where("node_id = ?", cfg.NodeID).Delete(&models.Node{}).Error; err != nil {
		t.Fatalf("cleaning the test node: %v", err)
	}
	if err := db.Create(&models.Node{
		NodeID: cfg.NodeID, Name: cfg.NodeName, Status: models.NodeStatusOnline, LastHeartbeat: &now,
	}).Error; err != nil {
		t.Fatalf("creating the test node: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	container := &Container{Cfg: cfg, Log: log, DB: db, Settings: settings, Systems: systems}
	engine.GET("/api/beszel/agent-connect", container.agentConnect)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)

	harness := &scratchHarness{db: db, cfg: cfg, systems: systems, server: server}
	t.Cleanup(func() {
		systems.CloseAgents("test finished")
		db.Where("node_id = ?", cfg.NodeID).Delete(&models.Node{})
	})
	return harness
}

// createSystem stores a system through the service (the HTTP layer needs a
// session, which is not what this test is about).
func (h *scratchHarness) createSystem(t *testing.T, name string) *services.SystemView {
	t.Helper()
	view, err := h.systems.Create(context.Background(), services.SystemInput{Name: name, Host: "docker-01"})
	if err != nil {
		t.Fatalf("creating the system: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.systems.Delete(ctx, view.ID); err != nil {
			t.Logf("cleaning up system %d: %v", view.ID, err)
		}
	})
	return view
}

// dialAgent opens the WebSocket the way the real agent does and answers the two
// requests the hub sends. The returned channel signals that a sample was asked
// for, which is what proves the session is polling.
func (h *scratchHarness) dialAgent(t *testing.T, token string, sample *beszel.CombinedData, fingerprint string) chan struct{} {
	t.Helper()
	signer, err := h.systems.Signer(context.Background())
	if err != nil {
		t.Fatalf("reading the host key: %v", err)
	}
	header := http.Header{}
	header.Set("X-Token", token)
	header.Set("X-Beszel", "0.21.1")
	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/api/beszel/agent-connect"
	ws, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatalf("dialing the agent endpoint: %v", err)
	}

	sampled := make(chan struct{}, 1)
	go func() {
		defer ws.Close()
		for {
			_ = ws.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, payload, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var request struct {
				Action uint8           `cbor:"0,keyasint"`
				Data   cbor.RawMessage `cbor:"1,keyasint"`
				ID     *uint32         `cbor:"2,keyasint"`
			}
			if err := cbor.Unmarshal(payload, &request); err != nil {
				return
			}
			response := beszel.AgentResponse{ID: request.ID}
			switch request.Action {
			case beszel.ActionCheckFingerprint:
				var challenge struct {
					Signature []byte `cbor:"0,keyasint"`
				}
				if err := cbor.Unmarshal(request.Data, &challenge); err != nil {
					return
				}
				signature := &ssh.Signature{Format: signer.PublicKey().Type(), Blob: challenge.Signature}
				if err := signer.PublicKey().Verify([]byte(token), signature); err != nil {
					response.Error = "invalid signature - check KEY value"
				} else {
					response.Fingerprnt = &beszel.FingerprintMessage{Fingerprint: fingerprint, Hostname: "docker-01"}
				}
			case beszel.ActionGetData:
				response.SystemData = sample
				select {
				case sampled <- struct{}{}:
				default:
				}
			default:
				response.SmartData = map[string]beszel.Smart{}
			}
			encoded, err := cbor.Marshal(response)
			if err != nil {
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
				return
			}
		}
	}()
	return sampled
}

// TestAgentEndToEnd drives the whole chain: connect, authenticate, sample, store.
func TestAgentEndToEnd(t *testing.T) {
	harness := newScratchHarness(t)
	ctx := context.Background()
	view := harness.createSystem(t, "e2e-"+time.Now().UTC().Format("150405.000000"))
	sample := &beszel.CombinedData{
		Stats: beszel.Stats{
			CPU:       23.5,
			MemPct:    47,
			MemBytes:  [4]uint64{8 << 30, 4 << 30, 0, 0},
			DiskTotal: 40,
			DiskUsed:  10,
			DiskPct:   25,
			Bandwidth: [2]uint64{1000, 2000},
		},
		Info: beszel.Info{Uptime: 1234, AgentVersion: "0.21.1"},
		Containers: []*beszel.Container{
			{Name: "web", Cpu: 1.5, Mem: 64, Status: "running", Health: beszel.HealthHealthy, Bandwidth: [2]uint64{10, 20}},
			{Name: "db", Cpu: 0.5, Mem: 256, Status: "running"},
		},
		Details: &beszel.SystemDetails{Hostname: "docker-01", Cores: 4, Threads: 8, MemoryTotal: 8 << 30, OSName: "Debian"},
	}

	sampled := harness.dialAgent(t, view.Token, sample, "fp-e2e")
	select {
	case <-sampled:
	case <-time.After(20 * time.Second):
		t.Fatal("the hub never asked for a sample")
	}

	// The sample has to be stored, and the fingerprint of the machine bound.
	deadline := time.Now().Add(20 * time.Second)
	var stored models.SystemMetric
	for {
		err := harness.db.Where("system_id = ?", view.ID).Order("id DESC").First(&stored).Error
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sample was never stored: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if stored.CPU != 23.5 || stored.MemTotal != 8<<30 {
		t.Fatalf("stored sample = %+v", stored)
	}
	if stored.Containers != 2 {
		t.Fatalf("stored container count = %d", stored.Containers)
	}

	// The containers and the machine identity come from the same sample.
	containers, err := harness.systems.Containers(ctx, view.ID)
	if err != nil {
		t.Fatalf("reading the containers: %v", err)
	}
	if len(containers) != 2 {
		t.Fatalf("containers = %d, want 2", len(containers))
	}
	byName := map[string]models.SystemContainer{}
	for _, container := range containers {
		byName[container.Name] = container
	}
	if byName["web"].Health != "healthy" {
		t.Fatalf("the health of the web container was not recorded: %+v", byName["web"])
	}
	if byName["db"].NetSent != 0 || byName["web"].NetSent != 10 {
		t.Fatalf("the container counters were not recorded: %+v", byName)
	}

	// The history the dashboard reads is assembled from the same rows.
	history, err := harness.systems.History(ctx, view.ID, 1)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if len(history.Metrics) == 0 {
		t.Fatal("the history is empty after a sample")
	}
	if history.System.Fingerprint != "fp-e2e" {
		t.Fatalf("fingerprint = %q, want fp-e2e", history.System.Fingerprint)
	}
	if history.System.Hostname != "docker-01" || history.System.Cores != 4 {
		t.Fatalf("identity = %+v", history.System)
	}
	if history.System.Status != models.SystemStatusUp {
		t.Fatalf("status = %q, want up", history.System.Status)
	}
}

// TestAgentRejectsAnUnknownToken makes sure the endpoint does not keep a socket
// open for a token that belongs to nothing.
func TestAgentRejectsAnUnknownToken(t *testing.T) {
	harness := newScratchHarness(t)
	header := http.Header{}
	header.Set("X-Token", "not-a-real-token")
	header.Set("X-Beszel", "0.21.1")
	url := "ws" + strings.TrimPrefix(harness.server.URL, "http") + "/api/beszel/agent-connect"
	ws, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		// The server may refuse the upgrade outright, which is also a pass.
		return
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("the server answered a request on a connection it should have closed")
	}
}
