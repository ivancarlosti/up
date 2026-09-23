package services

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ivancarlosti/up/internal/utils"
)

// Peer API signing (docs/clustering-federated.md, section 11).
//
// Every node to node request is signed with the cluster private key. The key
// itself is never sent on these endpoints: the signature already proves that the
// caller knows it, so transmitting it as well would only add exposure (the join
// endpoint keeps the original X-Cluster-Key behaviour, because that is the
// bootstrap and there is nothing to sign yet).
const (
	// HeaderPeerNode carries the sender NODE_ID. It is part of the signature,
	// so the receiver can record who called without trusting a plain header.
	HeaderPeerNode = "X-Cluster-Node"
	// HeaderPeerTimestamp is the unix timestamp (seconds, UTC) of the request.
	HeaderPeerTimestamp = "X-Cluster-Timestamp"
	// HeaderPeerNonce is a random value unique per request (replay protection).
	HeaderPeerNonce = "X-Cluster-Nonce"
	// HeaderPeerSignature is the base64url HMAC-SHA256 of the canonical string.
	HeaderPeerSignature = "X-Cluster-Signature"
)

// PeerClockSkew is how far a peer timestamp may differ from the local clock. It
// also defines the replay window: a nonce is remembered for this long plus a
// minute of slack.
const PeerClockSkew = 5 * time.Minute

// peerNonceRetention is the lifetime of a nonce in the cache: a request older
// than the window is rejected by the timestamp check anyway, so remembering it
// any longer would only waste memory.
const peerNonceRetention = PeerClockSkew + time.Minute

// PeerSignatureInput is everything the signature covers. Target is the path plus
// the query string: signing only the path would let a captured request be
// replayed with a different `?since=` cursor.
type PeerSignatureInput struct {
	Method    string
	Target    string
	Body      []byte
	Timestamp string
	Nonce     string
	NodeID    string
}

// CanonicalPeerRequest renders the exact bytes covered by the signature. It is a
// pure function on purpose: client and receiver must agree byte for byte, and a
// test pins the layout.
func CanonicalPeerRequest(in PeerSignatureInput) string {
	return strings.Join([]string{
		in.Method,
		in.Target,
		utils.SHA256Hex(string(in.Body)),
		in.Timestamp,
		in.Nonce,
		in.NodeID,
	}, "\n")
}

// SignPeerRequest returns the signature to present for one request.
func SignPeerRequest(key string, in PeerSignatureInput) string {
	return utils.Sign(key, CanonicalPeerRequest(in))
}

// PeerAuth verifies inbound peer requests: signature, timestamp window and
// replay cache. One instance lives on the cluster service.
type PeerAuth struct {
	nonces *peerNonceCache
	skew   time.Duration
}

// NewPeerAuth builds the verifier with the default window.
func NewPeerAuth() *PeerAuth {
	return &PeerAuth{nonces: newPeerNonceCache(), skew: PeerClockSkew}
}

// Verify authenticates one request. Every failure returns an error describing the
// cause; the middleware logs it and answers 403 (never a hint about which part
// failed, beyond the server side log).
func (a *PeerAuth) Verify(key string, in PeerSignatureInput, presented string, now time.Time) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("this node has no cluster key")
	}
	if in.NodeID == "" {
		return fmt.Errorf("missing %s header", HeaderPeerNode)
	}
	if in.Nonce == "" {
		return fmt.Errorf("missing %s header", HeaderPeerNonce)
	}
	if presented == "" {
		return fmt.Errorf("missing %s header", HeaderPeerSignature)
	}

	// The timestamp is checked before the signature so a garbage timestamp is not
	// parsed by the HMAC path, and before the nonce check so a stale request
	// cannot fill the replay cache.
	seconds, err := strconv.ParseInt(in.Timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid %s header %q", HeaderPeerTimestamp, in.Timestamp)
	}
	sent := time.Unix(seconds, 0).UTC()
	if delta := now.UTC().Sub(sent); delta > a.skew || delta < -a.skew {
		return fmt.Errorf("timestamp outside the ±%s window (%s)", a.skew, delta.Truncate(time.Second))
	}

	expected := SignPeerRequest(key, in)
	if !utils.SecureCompare(presented, expected) {
		return fmt.Errorf("signature mismatch from node %q", in.NodeID)
	}

	if a.nonces.remember(in.Nonce, now) {
		return fmt.Errorf("replayed nonce from node %q", in.NodeID)
	}
	return nil
}

// peerNonceCache remembers the nonces seen inside the signing window.
//
// It is in memory rather than the `sync_nonces` table of the design document: the
// window is five minutes and every peer endpoint is either a read or an
// idempotent pull, so losing the cache on restart costs nothing, while a write
// per request would sit on the hot path of every pull.
type peerNonceCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
	lastGC  time.Time
}

func newPeerNonceCache() *peerNonceCache {
	return &peerNonceCache{entries: map[string]time.Time{}, lastGC: time.Now()}
}

// remember stores a nonce and reports whether it had already been seen.
func (c *peerNonceCache) remember(nonce string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if now.Sub(c.lastGC) > time.Minute {
		for value, expiry := range c.entries {
			if now.After(expiry) {
				delete(c.entries, value)
			}
		}
		c.lastGC = now
	}

	if expiry, seen := c.entries[nonce]; seen && now.Before(expiry) {
		return true
	}
	c.entries[nonce] = now.Add(peerNonceRetention)
	return false
}

// size reports how many nonces are remembered (tests and diagnostics).
func (c *peerNonceCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
