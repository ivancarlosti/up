package services

import (
	"strings"
	"testing"
	"time"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// TestMetricFromSamplePrefersBytes pins the conversion the charts depend on.
//
// Every agent since 0.18 sends the memory twice: the byte sized MemBytes array and
// the deprecated GB fields. The byte sized ones must win (they carry the precision),
// and the GB ones are the fallback that keeps an older agent readable. Getting this
// backwards shows a 16 GiB machine as 32 GB, or a 5 GiB usage as 10.
func TestMetricFromSamplePrefersBytes(t *testing.T) {
	at := time.Date(2026, 2, 11, 10, 0, 0, 0, time.UTC)
	data := &beszel.CombinedData{
		Stats: beszel.Stats{
			CPU:       12.5,
			Mem:       32, // deprecated, must be ignored when MemBytes exists
			MemUsed:   10, // deprecated, must be ignored
			MemPct:    31.25,
			MemBytes:  [4]uint64{16 << 30, 5 << 30, 0, 0},
			SwapBytes: [2]uint64{2 << 30, 1 << 30},
			DiskTotal: 100,
			DiskUsed:  40,
			DiskPct:   40,
			Bandwidth: [2]uint64{1500, 2500},
			DiskIO:    [2]uint64{300, 700},
			LoadAvg:   [3]float64{1.5, 1.2, 1.0},
			Temperatures: map[string]float64{
				"coretemp": 61,
				"nvme":     44,
			},
		},
		Info:       beszel.Info{Uptime: 3600, AgentVersion: "0.21.1"},
		Containers: []*beszel.Container{{Name: "web", Status: "running"}},
	}

	metric := metricFromSample(7, data, at)
	if metric.SystemID != 7 || !metric.CreatedAt.Equal(at) {
		t.Fatalf("identity = %d %s", metric.SystemID, metric.CreatedAt)
	}
	if metric.MemTotal != 16<<30 || metric.MemUsed != 5<<30 {
		t.Fatalf("memory = %d/%d, want the byte sized values", metric.MemUsed, metric.MemTotal)
	}
	if metric.MemPct != 31.25 {
		t.Fatalf("mem pct = %v", metric.MemPct)
	}
	if metric.SwapPct != 50 {
		t.Fatalf("swap pct = %v, want 50", metric.SwapPct)
	}
	if metric.DiskTotal != 100<<30 || metric.DiskUsed != 40<<30 {
		t.Fatalf("disk = %d/%d, want the GB fields in bytes", metric.DiskUsed, metric.DiskTotal)
	}
	if metric.NetSent != 1500 || metric.NetRecv != 2500 {
		t.Fatalf("bandwidth = %d/%d, want the byte counters", metric.NetSent, metric.NetRecv)
	}
	if metric.DiskRead != 300 || metric.DiskWrite != 700 {
		t.Fatalf("disk io = %d/%d", metric.DiskRead, metric.DiskWrite)
	}
	if metric.LoadAvg != 1.5 {
		t.Fatalf("load avg = %v", metric.LoadAvg)
	}
	if metric.Temperature != 61 {
		t.Fatalf("temperature = %v, want the hottest sensor", metric.Temperature)
	}
	if metric.Containers != 1 {
		t.Fatalf("containers = %d", metric.Containers)
	}
	if metric.Uptime != 3600 {
		t.Fatalf("uptime = %d", metric.Uptime)
	}
}

// TestMetricFromSampleFallsBackToLegacyFields keeps an 0.13-era payload readable:
// no MemBytes, no Bandwidth, only the deprecated GB/MB fields and a LoadAvg in Info.
func TestMetricFromSampleFallsBackToLegacyFields(t *testing.T) {
	data := &beszel.CombinedData{
		Stats: beszel.Stats{
			CPU:         5,
			Mem:         8,
			MemUsed:     2,
			DiskTotal:   50,
			DiskUsed:    10,
			DiskPct:     20,
			NetworkSent: 2, // MB/s
			NetworkRecv: 1,
		},
		Info: beszel.Info{LoadAvg: [3]float64{0.75, 0.5, 0.25}},
	}
	metric := metricFromSample(1, data, time.Now())
	if metric.MemTotal != 8<<30 || metric.MemUsed != 2<<30 {
		t.Fatalf("memory = %d/%d, want the GB fields", metric.MemUsed, metric.MemTotal)
	}
	if metric.MemPct != 25 {
		t.Fatalf("mem pct = %v, want the derived 25", metric.MemPct)
	}
	if metric.NetSent != 2<<20 || metric.NetRecv != 1<<20 {
		t.Fatalf("bandwidth = %d/%d, want the MB fields in bytes", metric.NetSent, metric.NetRecv)
	}
	if metric.LoadAvg != 0.75 {
		t.Fatalf("load avg = %v, want the Info fallback", metric.LoadAvg)
	}
}

// TestSeriesStepKeepsOnePointPerPixel checks the downsampling of every window: a
// chart must never render one point per stored row (43 000 of them for a 30 day
// window), and a one hour chart must not be smoothed below the sample rate.
func TestSeriesStepKeepsOnePointPerPixel(t *testing.T) {
	cases := map[int]int{
		1:   models.SystemSampleInterval,
		6:   90,
		24:  360,
		168: 2520,
		720: 10800,
	}
	for hours, want := range cases {
		if got := seriesStep(hours); got != want {
			t.Errorf("seriesStep(%d) = %d, want %d", hours, got, want)
		}
		if points := hours * 3600 / seriesStep(hours); points > seriesPoints+1 {
			t.Errorf("a %d hour chart would return %d points", hours, points)
		}
	}
}

// TestNormalizeSystemChartWindow pins the window the API accepts, so a hand edited
// `?hours=9999` cannot ask the database for a decade of rows.
func TestNormalizeSystemChartWindow(t *testing.T) {
	cases := map[int]int{0: 24, -5: 24, 1: 1, 5: 6, 24: 24, 100: 168, 500: 720, 9999: 720}
	for input, want := range cases {
		if got := models.NormalizeSystemChartWindow(input); got != want {
			t.Errorf("NormalizeSystemChartWindow(%d) = %d, want %d", input, got, want)
		}
	}
}

// TestHashAgentTokenNeverStoresThePlaintext is the credential contract: the row
// keeps a hash, and the hash is what a presented token is looked up by.
func TestHashAgentTokenNeverStoresThePlaintext(t *testing.T) {
	token, hash, err := generateAgentToken()
	if err != nil {
		t.Fatalf("generateAgentToken: %v", err)
	}
	if len(token) != agentTokenLength {
		t.Fatalf("token length = %d, want %d", len(token), agentTokenLength)
	}
	if hash == token || hash == "" {
		t.Fatal("the stored hash must not be the token itself")
	}
	if hashAgentToken(token) != hash {
		t.Fatal("the hash of the generated token does not match the stored one")
	}
	other, _, err := generateAgentToken()
	if err != nil {
		t.Fatalf("generateAgentToken: %v", err)
	}
	if other == token {
		t.Fatal("two generated tokens are identical")
	}
	if tokenPrefix(token) != token[:6] {
		t.Fatalf("prefix = %q, want the first six characters", tokenPrefix(token))
	}
}

// TestDecorateReportsFreshnessAsStatus pins the badge: a system is up only while
// its last sample is recent, and "never sampled" is pending instead of down.
func TestDecorateReportsFreshnessAsStatus(t *testing.T) {
	service := &SystemService{agents: newAgentRegistry()}
	now := time.Now().UTC()
	recent := now.Add(-30 * time.Second)
	stale := now.Add(-models.SystemDownAfter - time.Minute)

	cases := []struct {
		name   string
		system models.System
		want   models.SystemStatus
	}{
		{"fresh sample", models.System{Enabled: true, LastSampleAt: &recent}, models.SystemStatusUp},
		{"stale sample", models.System{Enabled: true, LastSampleAt: &stale}, models.SystemStatusDown},
		{"never sampled", models.System{Enabled: true}, models.SystemStatusPending},
		{"disabled", models.System{Enabled: false, LastSampleAt: &recent}, models.SystemStatusPending},
	}
	for _, test := range cases {
		view := service.decorate(&test.system, systemOwner{NodeID: "up-node-1", NodeName: "one"})
		if view.Status != test.want {
			t.Errorf("%s: status = %q, want %q", test.name, view.Status, test.want)
		}
	}
	view := service.decorate(&models.System{Enabled: true, LastSampleAt: &recent},
		systemOwner{NodeID: "up-node-2", NodeName: "two"})
	if view.OwnerNode != "up-node-2" || view.OwnerNodeName != "two" {
		t.Errorf("owner = %q/%q", view.OwnerNode, view.OwnerNodeName)
	}
	if view.Connected {
		t.Error("a system without a live session must not report itself connected")
	}
}

// TestInstallCommandCarriesTheHandshakeValues keeps the dialog honest: the line an
// operator pastes must contain the token and the hub URL the agent needs, because a
// missing one is a machine that silently never appears.
func TestInstallCommandCarriesTheHandshakeValues(t *testing.T) {
	service := &SystemService{cfg: &config.Config{AppURL: "https://up.example.com"}}
	command := service.InstallCommand("docker-01", "tok-123")
	for _, want := range []string{"henrygd/beszel-agent:", `TOKEN="tok-123"`, `HUB_URL="https://up.example.com"`, "/var/run/docker.sock"} {
		if !strings.Contains(command, want) {
			t.Errorf("install command does not contain %q:\n%s", want, command)
		}
	}
}

// TestContainerRunningReadsTheAgentsStatus pins the status the agent actually
// sends.
//
// Docker's /containers/json reports a human string, not the state: "Up 2 days",
// "Exited (0) 3 hours ago", "Restarting (1) 5 seconds ago". Comparing against the
// literal "running" classified every container as stopped and no container history
// was written at all - the run against a real agent 0.21.1 is what caught it.
func TestContainerRunningReadsTheAgentsStatus(t *testing.T) {
	cases := map[string]bool{
		"Up 2 days":                       true,
		"up 5 minutes":                    true,
		"Up Less than a second (healthy)": true,
		"running":                         true,
		"Exited (0) 3 hours ago":          false,
		"Restarting (1) 5 seconds ago":    false,
		"Created":                         false,
		"":                                false,
	}
	for status, want := range cases {
		if got := ContainerRunning(status); got != want {
			t.Errorf("ContainerRunning(%q) = %v, want %v", status, got, want)
		}
	}
}
