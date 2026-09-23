package models

import (
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Cluster: failure strategies
// ---------------------------------------------------------------------------

// FailureStrategy decides how the votes of the online nodes are merged.
type FailureStrategy string

// Failure strategies (Admin > Cluster > failure_strategy).
const (
	// FailureStrategyAnyNodeFails marks the monitor as down as soon as one
	// online node reports a failure.
	FailureStrategyAnyNodeFails FailureStrategy = "ANY_NODE_FAILS"
	// FailureStrategyAllNodesFail marks the monitor as down only when every
	// online node reports a failure (default).
	FailureStrategyAllNodesFail FailureStrategy = "ALL_NODES_FAIL"
	// FailureStrategyQuorum marks the monitor as down when the majority of
	// the online nodes reports a failure; a partial failure is "degraded".
	FailureStrategyQuorum FailureStrategy = "QUORUM"
)

// Valid reports whether the strategy is known.
func (s FailureStrategy) Valid() bool {
	switch s {
	case FailureStrategyAnyNodeFails, FailureStrategyAllNodesFail, FailureStrategyQuorum:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Cluster: node unavailable strategy
// ---------------------------------------------------------------------------

// NodeUnavailableStrategy decides what to do with nodes that stopped
// reporting heartbeats (last_heartbeat older than the offline threshold).
type NodeUnavailableStrategy string

// Node unavailable strategies (Admin > Cluster > node_unavailable_strategy).
const (
	// NodeUnavailableIgnore removes the offline node from the vote and keeps
	// evaluating with the remaining nodes.
	NodeUnavailableIgnore NodeUnavailableStrategy = "IGNORE"
	// NodeUnavailableMarkDegraded keeps evaluating with the remaining nodes
	// but flags the monitor/page as degraded and warns the operator.
	NodeUnavailableMarkDegraded NodeUnavailableStrategy = "MARK_DEGRADED"
)

// Valid reports whether the strategy is known.
func (s NodeUnavailableStrategy) Valid() bool {
	switch s {
	case NodeUnavailableIgnore, NodeUnavailableMarkDegraded:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Cluster: notification sender strategy
// ---------------------------------------------------------------------------

// NotificationSenderStrategy decides which node dispatches notifications.
type NotificationSenderStrategy string

// Notification sender strategies (Admin > Cluster > notification_sender).
const (
	// NotificationSenderPrimaryOnly restricts notifications to the primary
	// node.
	NotificationSenderPrimaryOnly NotificationSenderStrategy = "PRIMARY_ONLY"
	// NotificationSenderAnyWithLock lets any node send, relying on a database
	// lock so that only one node sends per transition.
	NotificationSenderAnyWithLock NotificationSenderStrategy = "ANY_WITH_LOCK"
)

// Valid reports whether the strategy is known.
func (s NotificationSenderStrategy) Valid() bool {
	switch s {
	case NotificationSenderPrimaryOnly, NotificationSenderAnyWithLock:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Cluster: node liveness
// ---------------------------------------------------------------------------

// NodeStatus is the liveness state of a cluster node.
type NodeStatus string

// Node liveness states.
const (
	NodeStatusOnline   NodeStatus = "online"
	NodeStatusDegraded NodeStatus = "degraded"
	NodeStatusOffline  NodeStatus = "offline"
)

// ---------------------------------------------------------------------------
// Notification events
// ---------------------------------------------------------------------------

// NotificationEvent is the transition that triggered a notification.
type NotificationEvent string

// Notification events.
const (
	EventDown NotificationEvent = "down"
	EventUp   NotificationEvent = "up"
	EventTest NotificationEvent = "test"
	// EventCertExpiring is a reminder that a TLS certificate is about to expire
	// (sent on every configured threshold and once a day inside the tightest
	// window).
	EventCertExpiring NotificationEvent = "cert_expiring"
	// EventCertExpired is the certificate being past its NotAfter. It is the only
	// certificate signal for a monitor that ignores TLS errors (whose checks keep
	// succeeding).
	EventCertExpired NotificationEvent = "cert_expired"
)

// ---------------------------------------------------------------------------
// Small helpers shared by the services
// ---------------------------------------------------------------------------

// SplitList splits a comma/space/semicolon separated list, trimming entries.
// It is used for accepted status codes, tags, e-mail recipients and for the
// KEYCLOAK_ACCOUNTS allow list.
func SplitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// JoinList normalizes a list of strings into a deterministic comma separated
// string (the storage format for tags and scopes).
func JoinList(items []string) string {
	cleaned := make([]string, 0, len(items))
	for _, i := range items {
		if v := strings.TrimSpace(i); v != "" {
			cleaned = append(cleaned, v)
		}
	}
	sort.Strings(cleaned)
	return strings.Join(cleaned, ",")
}
