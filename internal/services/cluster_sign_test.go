package services

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCanonicalPeerRequest pins the exact bytes covered by the signature. The
// sender and the receiver must agree byte for byte, so the layout is written out
// literally here: changing the order or the separators is a protocol break.
func TestCanonicalPeerRequest(t *testing.T) {
	got := CanonicalPeerRequest(PeerSignatureInput{
		Method:    "GET",
		Target:    "/api/cluster/sync/ping",
		Body:      nil,
		Timestamp: "1767225600",
		Nonce:     "abc123",
		NodeID:    "up-node-1",
	})
	// The third field is the SHA-256 of the empty body.
	want := strings.Join([]string{
		"GET",
		"/api/cluster/sync/ping",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"1767225600",
		"abc123",
		"up-node-1",
	}, "\n")
	if got != want {
		t.Fatalf("canonical request changed:\n got %q\nwant %q", got, want)
	}
}

// TestCanonicalPeerRequestBody proves the body is part of the signature: a POST
// payload cannot be swapped without invalidating the request.
func TestCanonicalPeerRequestBody(t *testing.T) {
	base := PeerSignatureInput{Method: "POST", Target: "/api/cluster/sync/now", Nonce: "n", NodeID: "a", Timestamp: "1"}
	withBody := base
	withBody.Body = []byte(`{"hello":"world"}`)
	if CanonicalPeerRequest(base) == CanonicalPeerRequest(withBody) {
		t.Fatal("the body must change the canonical request")
	}
}

// peerInput is a helper building a well formed request for a fixed clock.
func peerInput(now time.Time, nonce string) PeerSignatureInput {
	return PeerSignatureInput{
		Method:    "GET",
		Target:    "/api/cluster/sync/ping",
		Timestamp: strconv.FormatInt(now.Unix(), 10),
		Nonce:     nonce,
		NodeID:    "up-node-2",
	}
}

func TestPeerAuthAcceptsAValidRequest(t *testing.T) {
	key := "cluster-key"
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	in := peerInput(now, "nonce-1")

	if err := NewPeerAuth().Verify(key, in, SignPeerRequest(key, in), now); err != nil {
		t.Fatalf("a valid request was rejected: %v", err)
	}
}

func TestPeerAuthRejectsBadRequests(t *testing.T) {
	key := "cluster-key"
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	valid := peerInput(now, "nonce-1")
	validSignature := SignPeerRequest(key, valid)

	tamperedTarget := valid
	tamperedTarget.Target = "/api/cluster/sync/changes?since=999"

	tamperedBody := valid
	tamperedBody.Method = "POST"
	tamperedBody.Body = []byte(`{"evil":true}`)

	impostor := valid
	impostor.NodeID = "up-node-9"

	stale := valid
	stale.Timestamp = strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10)

	future := valid
	future.Timestamp = strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)

	cases := []struct {
		name       string
		key        string
		in         PeerSignatureInput
		signature  string
		wantSubstr string
	}{
		{name: "wrong key", key: "another-key", in: valid, signature: validSignature, wantSubstr: "signature mismatch"},
		{name: "no signature", key: key, in: valid, signature: "", wantSubstr: HeaderPeerSignature},
		{name: "tampered target", key: key, in: tamperedTarget, signature: validSignature, wantSubstr: "signature mismatch"},
		{name: "tampered body", key: key, in: tamperedBody, signature: validSignature, wantSubstr: "signature mismatch"},
		{name: "impostor node id", key: key, in: impostor, signature: validSignature, wantSubstr: "signature mismatch"},
		{name: "stale timestamp", key: key, in: stale, signature: SignPeerRequest(key, stale), wantSubstr: "window"},
		{name: "future timestamp", key: key, in: future, signature: SignPeerRequest(key, future), wantSubstr: "window"},
		{name: "missing node id", key: key, in: PeerSignatureInput{Method: "GET", Target: "/x", Timestamp: valid.Timestamp, Nonce: "n"}, signature: "x", wantSubstr: HeaderPeerNode},
		{name: "missing nonce", key: key, in: PeerSignatureInput{Method: "GET", Target: "/x", Timestamp: valid.Timestamp, NodeID: "a"}, signature: "x", wantSubstr: HeaderPeerNonce},
		{name: "unparsable timestamp", key: key, in: PeerSignatureInput{Method: "GET", Target: "/x", Timestamp: "yesterday", Nonce: "n", NodeID: "a"}, signature: "x", wantSubstr: "invalid"},
		{name: "no cluster key configured", key: "", in: valid, signature: validSignature, wantSubstr: "no cluster key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewPeerAuth().Verify(tc.key, tc.in, tc.signature, now)
			if err == nil {
				t.Fatalf("expected a rejection")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestPeerAuthRejectsAReplayedNonce is the property a captured request must not
// survive: the same signed request may be used exactly once.
func TestPeerAuthRejectsAReplayedNonce(t *testing.T) {
	key := "cluster-key"
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	in := peerInput(now, "once-only")
	signature := SignPeerRequest(key, in)

	auth := NewPeerAuth()
	if err := auth.Verify(key, in, signature, now); err != nil {
		t.Fatalf("first use should be accepted: %v", err)
	}
	err := auth.Verify(key, in, signature, now)
	if err == nil || !strings.Contains(err.Error(), "replayed nonce") {
		t.Fatalf("second use must be rejected as a replay, got: %v", err)
	}
}

// TestPeerNonceCacheExpires checks the cache does not grow forever: after the
// window a nonce is forgotten (the timestamp check rejects the request anyway).
func TestPeerNonceCacheExpires(t *testing.T) {
	cache := newPeerNonceCache()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if cache.remember("n1", now) {
		t.Fatal("a fresh nonce is not a replay")
	}
	if !cache.remember("n1", now) {
		t.Fatal("the second use of a nonce is a replay")
	}
	if cache.remember("n1", now.Add(peerNonceRetention+time.Minute)) {
		t.Fatal("after the retention the nonce must be forgotten")
	}
}
