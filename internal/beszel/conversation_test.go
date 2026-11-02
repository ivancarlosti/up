package beszel

import (
	"context"
	"strings"
	"testing"
)

// TestFingerprintProvesTheToken is the handshake the whole feature depends on.
//
// The agent only trusts a hub that can sign its token with the KEY it was given,
// so the signature has to verify with the public half of the derived host key. A
// wrong derivation (a different domain string on two nodes, a random key per
// process) would still produce a signature - just one no agent accepts.
func TestFingerprintProvesTheToken(t *testing.T) {
	agent := &fakeAgent{token: "token-abc"}
	conn, signer := dial(t, agent)

	message, err := conn.Fingerprint(context.Background(), "token-abc", signer)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if message.Fingerprint != "3f2a1c" {
		t.Fatalf("fingerprint = %q, want 3f2a1c", message.Fingerprint)
	}
	if message.Hostname != "docker-01" {
		t.Fatalf("hostname = %q, want docker-01", message.Hostname)
	}
	if len(agent.seen) != 1 || agent.seen[0] != ActionCheckFingerprint {
		t.Fatalf("actions = %v, want [%d]", agent.seen, ActionCheckFingerprint)
	}
}

// TestFingerprintRejectsAForeignKey pins the failure the operator actually hits:
// an agent started with the KEY of another installation.
func TestFingerprintRejectsAForeignKey(t *testing.T) {
	agent := &fakeAgent{token: "token-abc"}
	agent.verifier = mustSignerFor(t, "another-installation").PublicKey()
	server := httptestServer(t, agent)

	conn := newTestConn(t, server)
	_, err := conn.Fingerprint(context.Background(), "token-abc", mustSigner(t))
	if err == nil {
		t.Fatal("the handshake succeeded with a signature the agent cannot verify")
	}
	if !strings.Contains(err.Error(), "invalid signature") {
		t.Fatalf("error = %v, want the agent's signature complaint", err)
	}
}

// TestSampleDecodesCounters checks the numbers the charts are built from: the
// byte sized fields win over the deprecated GB ones, and the containers arrive.
func TestSampleDecodesCounters(t *testing.T) {
	agent := &fakeAgent{
		token: "token-abc",
		sample: &CombinedData{
			Stats: Stats{
				CPU:       41.5,
				MemPct:    63.25,
				DiskPct:   77,
				MemBytes:  [4]uint64{16 << 30, 10 << 30, 1 << 30, 0},
				DiskIO:    [2]uint64{1024, 2048},
				Bandwidth: [2]uint64{512, 256},
				LoadAvg:   [3]float64{0.5, 0.4, 0.3},
				Temperatures: map[string]float64{
					"coretemp": 55,
				},
			},
			Info: Info{Uptime: 90000, AgentVersion: "0.21.1", BandwidthBytes: 768},
			Containers: []*Container{
				{Name: "web", Cpu: 3.5, Mem: 120, Status: "running", Health: HealthHealthy, Bandwidth: [2]uint64{100, 200}},
			},
			Details: &SystemDetails{Hostname: "docker-01", Cores: 8, MemoryTotal: 16 << 30},
		},
	}
	conn, signer := dial(t, agent)

	if _, err := conn.Fingerprint(context.Background(), "token-abc", signer); err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	// The same connection has to keep working: the reader routes by request id.
	data, err := conn.Sample(context.Background(), DataRequestOptions{CacheTimeMs: 60000, IncludeDetails: true})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if data.Stats.CPU != 41.5 || data.Stats.MemPct != 63.25 {
		t.Fatalf("unexpected stats: %+v", data.Stats)
	}
	if data.Stats.MemBytes[0] != 16<<30 {
		t.Fatalf("MemBytes total = %d", data.Stats.MemBytes[0])
	}
	if len(data.Containers) != 1 || data.Containers[0].Name != "web" {
		t.Fatalf("containers = %+v", data.Containers)
	}
	if data.Details == nil || data.Details.Cores != 8 {
		t.Fatalf("details = %+v", data.Details)
	}
}

// TestSmartReturnsDisks checks the SMART answer (legacy typed field, key 5).
func TestSmartReturnsDisks(t *testing.T) {
	agent := &fakeAgent{token: "token"}
	conn, _ := dial(t, agent)

	report, err := conn.Smart(context.Background())
	if err != nil {
		t.Fatalf("Smart: %v", err)
	}
	if !report.Complete {
		t.Fatal("report should be complete")
	}
	if disk, ok := report.Disks["sda"]; !ok || disk.Status != "PASSED" {
		t.Fatalf("disks = %+v", report.Disks)
	}
}

// TestAgentErrorIsReportedNotSwallowed keeps a refusal readable to the operator.
func TestAgentErrorIsReportedNotSwallowed(t *testing.T) {
	agent := &fakeAgent{token: "token", refused: "no docker socket"}
	conn, _ := dial(t, agent)

	_, err := conn.Sample(context.Background(), DataRequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "no docker socket") {
		t.Fatalf("error = %v, want the agent's message", err)
	}
}
