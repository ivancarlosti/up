// Package models contains every persisted entity of Up plus the enums and
// configuration structs shared by the scheduler, the checkers, the notifier
// and the HTTP handlers.
package models

import "fmt"

// ---------------------------------------------------------------------------
// Monitor types
// ---------------------------------------------------------------------------

// MonitorType is the probe implementation used by a monitor.
type MonitorType string

// Supported monitor types.
const (
	MonitorTypeHTTP    MonitorType = "http"    // HTTP(s) request + accepted status codes
	MonitorTypeKeyword MonitorType = "keyword" // HTTP(s) request + keyword match
	MonitorTypeTCP     MonitorType = "tcp"     // TCP connect, optional send/expect
	MonitorTypeDNS     MonitorType = "dns"     // DNS query through a specific resolver
)

// AllMonitorTypes lists every supported monitor type (validation + UI).
func AllMonitorTypes() []MonitorType {
	return []MonitorType{MonitorTypeHTTP, MonitorTypeKeyword, MonitorTypeTCP, MonitorTypeDNS}
}

// Valid reports whether the type is known.
func (t MonitorType) Valid() bool {
	switch t {
	case MonitorTypeHTTP, MonitorTypeKeyword, MonitorTypeTCP, MonitorTypeDNS:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Heartbeat status (the outcome of a single check)
// ---------------------------------------------------------------------------

// HeartbeatStatus is the outcome of one heartbeat execution. The numeric
// values are part of the persisted schema and of the public API contract.
type HeartbeatStatus int

// Heartbeat statuses.
const (
	StatusDown        HeartbeatStatus = 0
	StatusUp          HeartbeatStatus = 1
	StatusPending     HeartbeatStatus = 2
	StatusMaintenance HeartbeatStatus = 3
)

// String returns the stable lower case name used in JSON payloads.
func (s HeartbeatStatus) String() string {
	switch s {
	case StatusUp:
		return "up"
	case StatusPending:
		return "pending"
	case StatusMaintenance:
		return "maintenance"
	default:
		return "down"
	}
}

// ParseHeartbeatStatus converts the JSON representation back to the enum.
func ParseHeartbeatStatus(raw string) (HeartbeatStatus, error) {
	switch raw {
	case "0", "down":
		return StatusDown, nil
	case "1", "up":
		return StatusUp, nil
	case "2", "pending":
		return StatusPending, nil
	case "3", "maintenance":
		return StatusMaintenance, nil
	}
	return StatusDown, fmt.Errorf("invalid heartbeat status %q", raw)
}

// ---------------------------------------------------------------------------
// Aggregated monitor state (result of the cluster failure strategies)
// ---------------------------------------------------------------------------

// AggregateStatus is the monitor state after merging every node vote.
type AggregateStatus string

// Aggregated states exposed by the dashboard, the status pages and the API.
const (
	AggregateUp          AggregateStatus = "up"
	AggregateDown        AggregateStatus = "down"
	AggregateDegraded    AggregateStatus = "degraded"
	AggregatePending     AggregateStatus = "pending"
	AggregateUnknown     AggregateStatus = "unknown"
	AggregateMaintenance AggregateStatus = "maintenance"
)
