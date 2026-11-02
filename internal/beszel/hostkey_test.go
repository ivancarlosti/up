package beszel

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// httptestServer starts the fake agent and returns its base URL.
func httptestServer(t *testing.T, agent *fakeAgent) string {
	t.Helper()
	server := httptest.NewServer(agent)
	t.Cleanup(server.Close)
	return server.URL
}

// TestHostSignerIsStableAcrossProcesses is what makes the handover between the
// nodes of a cluster work: the same cluster key must yield the same host key on
// every node, and a different key must not.
func TestHostSignerIsStableAcrossProcesses(t *testing.T) {
	first := mustSignerFor(t, "shared-cluster-key")
	second := mustSignerFor(t, "shared-cluster-key")
	if HostPublicKey(first) != HostPublicKey(second) {
		t.Fatal("the same cluster key produced two host keys")
	}
	other := mustSignerFor(t, "another-cluster-key")
	if HostPublicKey(first) == HostPublicKey(other) {
		t.Fatal("two different cluster keys produced the same host key")
	}
	if !strings.HasPrefix(HostPublicKey(first), "ssh-ed25519 ") {
		t.Fatalf("unexpected public key format: %q", HostPublicKey(first))
	}
	if !strings.HasPrefix(HostPublicKeyFingerprint(first), "SHA256:") {
		t.Fatalf("unexpected fingerprint: %q", HostPublicKeyFingerprint(first))
	}
	if _, err := HostSigner(""); err == nil {
		t.Fatal("an empty cluster key must not produce a host key")
	}
}

// TestVersionGate pins the >= 0.18 decision: an agent older than the envelope
// this package speaks would be misread, so it is refused with a message.
func TestVersionGate(t *testing.T) {
	cases := []struct {
		raw   string
		ok    bool
		label string
	}{
		{"0.21.1", true, "current agent"},
		{"0.18.0", true, "the minimum"},
		{"0.17.9", false, "too old"},
		{"0.9.0", false, "far too old"},
		{"beszel-agent-0.20.0", true, "prefixed"},
		{"0.22.0-rc1", true, "pre-release of a supported minor"},
		{"", false, "missing"},
		{"garbage", false, "unreadable"},
	}
	for _, test := range cases {
		_, err := Supported(test.raw)
		if test.ok && err != nil {
			t.Errorf("%s (%q): unexpected error %v", test.label, test.raw, err)
		}
		if !test.ok && err == nil {
			t.Errorf("%s (%q): expected a refusal", test.label, test.raw)
		}
	}
}

// TestCloseUnblocksWaiters checks that a connection closed mid-request reports it
// instead of holding the caller until the timeout.
func TestCloseUnblocksWaiters(t *testing.T) {
	agent := &fakeAgent{token: "token"}
	conn, _ := dial(t, agent)
	conn.Close("the node is shutting down")
	if _, err := conn.Sample(context.Background(), DataRequestOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("error = %v, want ErrClosed", err)
	}
	select {
	case <-conn.Done():
	default:
		t.Fatal("Done() must be closed after Close")
	}
	if conn.CloseReason() != "the node is shutting down" {
		t.Fatalf("reason = %q", conn.CloseReason())
	}
}

// TestRequestTimesOut keeps a silent agent from holding the poll loop forever.
func TestRequestTimesOut(t *testing.T) {
	conn := NewConn(mustWebsocketPair(t), testLogger())
	// Nothing answers on this socket, so the request has to give up on its own.
	start := time.Now()
	_, err := conn.request(context.Background(), ActionGetData, DataRequestOptions{}, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the timeout took %s", elapsed)
	}
}
