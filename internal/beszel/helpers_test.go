package beszel

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// testLogger silences the package while the tests run.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mustSigner derives the host signer of the test cluster key.
func mustSigner(t *testing.T) ssh.Signer {
	t.Helper()
	return mustSignerFor(t, "cluster-key-for-tests")
}

// mustSignerFor derives the host signer of an explicit cluster key.
func mustSignerFor(t *testing.T, clusterKey string) ssh.Signer {
	t.Helper()
	signer, err := HostSigner(clusterKey)
	if err != nil {
		t.Fatalf("HostSigner(%q): %v", clusterKey, err)
	}
	return signer
}

// newTestConn dials an HTTP test server and wraps the client side in a Conn.
func newTestConn(t *testing.T, serverURL string) *Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(serverURL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	conn := NewConn(ws, testLogger())
	t.Cleanup(func() { conn.Close("test finished") })
	return conn
}

// mustWebsocketPair returns the hub side of a connection whose peer never
// answers: the reader stays blocked until the connection is closed.
func mustWebsocketPair(t *testing.T) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(httpHandler(func(ws *websocket.Conn) {
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(time.Minute))
	return ws
}
