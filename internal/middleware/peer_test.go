package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ivancarlosti/up/internal/services"
)

// stubPeerAuth wraps the real verifier with a fixed key, so the middleware can be
// exercised without a database (the service reads the key from the settings
// table).
type stubPeerAuth struct {
	key  string
	auth *services.PeerAuth
	last services.PeerSignatureInput
}

func newStubPeerAuth(key string) *stubPeerAuth {
	return &stubPeerAuth{key: key, auth: services.NewPeerAuth()}
}

func (s *stubPeerAuth) AuthenticatePeer(_ context.Context, in services.PeerSignatureInput, presented string) error {
	s.last = in
	return s.auth.Verify(s.key, in, presented, time.Now().UTC())
}

// peerTestRouter mounts the middleware in front of a handler that reports the
// authenticated peer and the body it received.
func peerTestRouter(auth PeerAuthenticator) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/cluster/sync", RequirePeerKey(auth))
	group.Any("/ping", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.JSON(http.StatusOK, gin.H{"peer": PeerNode(c), "body": string(body)})
	})
	return engine
}

// signedPeerRequest builds a request carrying a valid signature.
func signedPeerRequest(t *testing.T, key, method, target string, body []byte, nonce, nodeID string, at time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	signed := services.PeerSignatureInput{
		Method:    method,
		Target:    target,
		Body:      body,
		Timestamp: strconv.FormatInt(at.Unix(), 10),
		Nonce:     nonce,
		NodeID:    nodeID,
	}
	req.Header.Set(services.HeaderPeerNode, nodeID)
	req.Header.Set(services.HeaderPeerTimestamp, signed.Timestamp)
	req.Header.Set(services.HeaderPeerNonce, nonce)
	req.Header.Set(services.HeaderPeerSignature, services.SignPeerRequest(key, signed))
	return req
}

func TestRequirePeerKeyAcceptsASignedRequest(t *testing.T) {
	const key = "cluster-key"
	engine := peerTestRouter(newStubPeerAuth(key))

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, signedPeerRequest(t, key, http.MethodGet, "/api/cluster/sync/ping", nil, "n-1", "up-node-2", time.Now().UTC()))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("up-node-2")) {
		t.Fatalf("the handler did not receive the authenticated peer: %s", body)
	}
}

// TestRequirePeerKeyPreservesTheBody checks the buffering: the body has to be
// hashed for the signature and still be readable by the handler.
func TestRequirePeerKeyPreservesTheBody(t *testing.T) {
	const key = "cluster-key"
	payload := []byte(`{"since":42}`)
	engine := peerTestRouter(newStubPeerAuth(key))

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, signedPeerRequest(t, key, http.MethodPost, "/api/cluster/sync/ping", payload, "n-2", "up-node-2", time.Now().UTC()))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}
	// Decoded rather than matched against the raw JSON: the quotes are escaped
	// in the response body.
	var got struct {
		Peer string `json:"peer"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("unreadable handler response %q: %v", recorder.Body.String(), err)
	}
	if got.Body != string(payload) {
		t.Fatalf("the handler saw %q, want %q", got.Body, payload)
	}
}

func TestRequirePeerKeyRejectsUnsignedAndTamperedRequests(t *testing.T) {
	const key = "cluster-key"
	now := time.Now().UTC()

	cases := []struct {
		name    string
		request func(t *testing.T) *http.Request
	}{
		{
			name: "no headers at all",
			request: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/cluster/sync/ping", nil)
			},
		},
		{
			name: "headers without a signature",
			request: func(t *testing.T) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/cluster/sync/ping", nil)
				req.Header.Set(services.HeaderPeerNode, "up-node-2")
				req.Header.Set(services.HeaderPeerTimestamp, strconv.FormatInt(now.Unix(), 10))
				req.Header.Set(services.HeaderPeerNonce, "n-3")
				return req
			},
		},
		{
			name: "signed with another key",
			request: func(t *testing.T) *http.Request {
				return signedPeerRequest(t, "the-wrong-key", http.MethodGet, "/api/cluster/sync/ping", nil, "n-4", "up-node-2", now)
			},
		},
		{
			name: "signature reused for a different query",
			request: func(t *testing.T) *http.Request {
				req := signedPeerRequest(t, key, http.MethodGet, "/api/cluster/sync/ping", nil, "n-5", "up-node-2", now)
				req.URL.RawQuery = "since=9999"
				return req
			},
		},
		{
			name: "stale timestamp",
			request: func(t *testing.T) *http.Request {
				return signedPeerRequest(t, key, http.MethodGet, "/api/cluster/sync/ping", nil, "n-6", "up-node-2", now.Add(-30*time.Minute))
			},
		},
		{
			name: "timestamp in the future",
			request: func(t *testing.T) *http.Request {
				return signedPeerRequest(t, key, http.MethodGet, "/api/cluster/sync/ping", nil, "n-7", "up-node-2", now.Add(30*time.Minute))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := peerTestRouter(newStubPeerAuth(key))
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, tc.request(t))
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", recorder.Code)
			}
		})
	}
}

// TestRequirePeerKeyRejectsAReplay is the property a captured request must not be
// usable twice.
func TestRequirePeerKeyRejectsAReplay(t *testing.T) {
	const key = "cluster-key"
	engine := peerTestRouter(newStubPeerAuth(key))
	now := time.Now().UTC()

	first := httptest.NewRecorder()
	engine.ServeHTTP(first, signedPeerRequest(t, key, http.MethodGet, "/api/cluster/sync/ping", nil, "replay-me", "up-node-2", now))
	if first.Code != http.StatusOK {
		t.Fatalf("the first use should be accepted, got %d", first.Code)
	}

	second := httptest.NewRecorder()
	engine.ServeHTTP(second, signedPeerRequest(t, key, http.MethodGet, "/api/cluster/sync/ping", nil, "replay-me", "up-node-2", now))
	if second.Code != http.StatusForbidden {
		t.Fatalf("the replay must be rejected, got %d", second.Code)
	}
}
