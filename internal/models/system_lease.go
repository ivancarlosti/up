package models

import "time"

// SystemLease is the ownership of one agent connection.
//
// Exactly one node may talk to an agent at a time: two nodes polling the same
// machine would double every sample, and the second one would overwrite the
// first one's charts with its own copy. The lease is what makes the owner
// explicit instead of "whoever connected first":
//
//   - the node that accepted the agent connection renews it every 30 s;
//   - a lease that expired, or whose node stopped reporting heartbeats (the
//     same NodeOfflineSeconds rule the rest of the cluster uses), belongs to
//     nobody and the next node that sees the agent takes it;
//   - a node that shuts down cleanly releases it, so the handover is immediate
//     instead of waiting for the expiry.
//
// The agent is dialled by the node it connects to, so in practice the lease
// follows the socket. Keeping it in the database is what lets the listing show
// which node owns a system, and what stops a node that lost its connection from
// continuing to write samples behind the new owner's back.
type SystemLease struct {
	SystemID  uint      `gorm:"primaryKey;autoIncrement:false" json:"system_id"`
	NodeID    string    `gorm:"size:64;not null;index" json:"node_id"`
	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name stable.
func (SystemLease) TableName() string { return "system_leases" }

// SystemLeaseSeconds is how long a lease stays valid after its last renewal:
// three renewals of slack, so a slow database or a long collection does not
// hand a healthy system to another node.
const SystemLeaseSeconds = 90

// SystemLeaseRenewSeconds is the renewal cadence (a third of the validity).
const SystemLeaseRenewSeconds = SystemLeaseSeconds / 3

// LeaseTTL is SystemLeaseSeconds as a duration.
func LeaseTTL() time.Duration { return SystemLeaseSeconds * time.Second }

// LeaseRenewEvery is SystemLeaseRenewSeconds as a duration.
func LeaseRenewEvery() time.Duration { return SystemLeaseRenewSeconds * time.Second }
