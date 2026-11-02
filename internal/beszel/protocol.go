// Package beszel speaks the Beszel agent protocol.
//
// Up does not embed a collector: the operator runs the regular
// github.com/henrygd/beszel agent next to the machine, and this package is the
// hub side of the conversation the agent opens (docs/hardware-monitoring.md).
//
// The wire format is CBOR over a WebSocket, and the structs below mirror the
// ones the agent marshals. They are written here field by field instead of
// importing the agent: the agent is not a library, and pinning one upstream
// struct would drag the whole collector (cgo, docker, smartctl) into the server.
//
// The cbor integer keys are the contract, not the field names. They come from
// beszel (MIT, github.com/henrygd/beszel) and must never be renumbered: an agent
// older than a renumbering would decode the wrong shape and report nonsense
// instead of failing.
package beszel

// WebSocketAction is the action of a hub request (beszel internal/common).
type WebSocketAction = uint8

// Actions this build sends. Requests are answered in order by the agent, and
// every one of them carries an optional id so the response can be matched.
const (
	// ActionGetData samples the machine (and its containers).
	ActionGetData WebSocketAction = iota
	// ActionCheckFingerprint authenticates the hub: the agent verifies a
	// signature over the token with the public key it was given, and answers
	// with its own fingerprint.
	ActionCheckFingerprint
	// The remaining actions exist upstream and are not used here. Their numbers
	// are kept aligned with the agent's iota so ActionGetSmartData stays right.
	_ // ActionGetContainerLogs
	_ // ActionGetContainerInfo
	// ActionGetSmartData returns the cached SMART report of every disk.
	ActionGetSmartData
	_ // ActionGetSystemdInfo
	_ // ActionGetZfsData
	_ // ActionSyncNetworkMonitors
	_ // ActionGetPackageUpdates
	_ // ActionGetSystemdLogs
)

// HubRequest is what the hub sends to the agent.
type HubRequest[T any] struct {
	Action WebSocketAction `cbor:"0,keyasint"`
	Data   T               `cbor:"1,keyasint,omitempty,omitzero"`
	ID     *uint32         `cbor:"2,keyasint,omitempty"`
}

// AgentResponse is what the agent answers. Only the fields this build reads are
// declared: the decoder ignores the ones it does not know about, which is what
// keeps a newer agent readable.
type AgentResponse struct {
	ID         *uint32             `cbor:"0,keyasint,omitempty"`
	SystemData *CombinedData       `cbor:"1,keyasint,omitempty,omitzero"`
	Fingerprnt *FingerprintMessage `cbor:"2,keyasint,omitempty,omitzero"`
	Error      string              `cbor:"3,keyasint,omitempty,omitzero"`
	SmartData  map[string]Smart    `cbor:"5,keyasint,omitzero"`
	// Data is the generic payload of the 0.18+ endpoints (unused here: GetData
	// and CheckFingerprint still answer in the typed fields above).
	Data          []byte `cbor:"7,keyasint,omitempty,omitzero"`
	SmartComplete bool   `cbor:"8,keyasint,omitempty,omitzero"`
}

// DataRequestOptions is the payload of ActionGetData.
//
// CacheTimeMs is how long the agent may reuse the sample it already has; the
// poll interval goes here, so two hubs sharing one agent do not make it collect
// twice. IncludeDetails additionally samples the machine identity (hostname,
// cores, memory total), which never changes and is asked for once.
type DataRequestOptions struct {
	CacheTimeMs    uint16 `cbor:"0,keyasint"`
	IncludeDetails bool   `cbor:"1,keyasint"`
}

// FingerprintRequest is the payload of ActionCheckFingerprint: the signature
// over the token, and whether the agent should add its host information to the
// answer.
type FingerprintRequest struct {
	Signature   []byte `cbor:"0,keyasint"`
	NeedSysInfo bool   `cbor:"1,keyasint"`
}

// FingerprintMessage is the agent's answer to ActionCheckFingerprint.
type FingerprintMessage struct {
	Fingerprint string `cbor:"0,keyasint"`
	Hostname    string `cbor:"1,keyasint,omitzero"`
	Port        string `cbor:"2,keyasint,omitzero"`
	Name        string `cbor:"3,keyasint,omitzero"`
}

// CombinedData is the sample of one machine: the counters, the identity and the
// containers running on it.
type CombinedData struct {
	Stats      Stats          `cbor:"0,keyasint"`
	Info       Info           `cbor:"1,keyasint"`
	Containers []*Container   `cbor:"2,keyasint"`
	Details    *SystemDetails `cbor:"4,keyasint,omitempty"`
}
