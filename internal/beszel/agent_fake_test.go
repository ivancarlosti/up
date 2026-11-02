package beszel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

// fakeAgent is a minimal agent: it upgrades the connection and answers the two
// actions this package sends, exactly the way the real agent frames them.
//
// The test asserts against what the REAL agent does (the envelope with the id in
// key 0, the sample in key 1, the SMART disks in key 5), so a change to the
// structs here cannot pass while the wire format is broken.
type fakeAgent struct {
	t        *testing.T
	token    string
	verifier ssh.PublicKey
	// sample is answered to ActionGetData.
	sample *CombinedData
	// refused, when set, is answered as the agent's error field.
	refused string
	// seen records the actions in the order they arrived.
	seen []uint8
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		f.t.Errorf("upgrade failed: %v", err)
		return
	}
	defer ws.Close()
	for {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
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
			f.t.Errorf("the hub sent an undecodable request: %v", err)
			return
		}
		f.seen = append(f.seen, request.Action)

		response := AgentResponse{ID: request.ID}
		switch request.Action {
		case ActionCheckFingerprint:
			var fingerprint FingerprintRequest
			if err := cbor.Unmarshal(request.Data, &fingerprint); err != nil {
				f.t.Errorf("unreadable fingerprint request: %v", err)
				return
			}
			signature := &ssh.Signature{Format: f.verifier.Type(), Blob: fingerprint.Signature}
			if err := f.verifier.Verify([]byte(f.token), signature); err != nil {
				// This is what the real agent answers when KEY does not match.
				response.Error = "invalid signature - check KEY value"
			} else {
				response.Fingerprnt = &FingerprintMessage{
					Fingerprint: "3f2a1c",
					Hostname:    "docker-01",
					Port:        "45876",
				}
			}
		case ActionGetData:
			if f.refused != "" {
				response.Error = f.refused
			} else {
				response.SystemData = f.sample
			}
		case ActionGetSmartData:
			response.SmartData = map[string]Smart{
				"sda": {ModelName: "Samsung SSD 870", Status: "PASSED", Temperature: 31},
			}
			response.SmartComplete = true
		default:
			response.Error = "unknown action"
		}
		encoded, err := cbor.Marshal(response)
		if err != nil {
			f.t.Errorf("could not encode the answer: %v", err)
			return
		}
		if err := ws.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
			return
		}
	}
}

// dial starts the fake agent and returns a Conn talking to it, with the host
// signer the fake agent will verify.
func dial(t *testing.T, agent *fakeAgent) (*Conn, ssh.Signer) {
	t.Helper()
	server := httptest.NewServer(agent)
	t.Cleanup(server.Close)

	signer, err := HostSigner("cluster-key-for-tests")
	if err != nil {
		t.Fatalf("HostSigner: %v", err)
	}
	agent.verifier = signer.PublicKey()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn := NewConn(ws, testLogger())
	t.Cleanup(func() { conn.Close("test finished") })
	return conn, signer
}
