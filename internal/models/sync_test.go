package models

import (
	"testing"
	"time"
)

// at returns a pointer to the given time (the columns are nullable).
func at(t time.Time) *time.Time { return &t }

// TestSyncedEntitiesCoversEveryEntity pins the list that decides what a node
// SERVES.
//
// An entity missing from it is worse than one missing from the apply switch: the
// change is written to the outbox, never handed to a peer, and the cursor then
// advances past it — so the row is silently lost for good, and neither the manifest
// nor the snapshot covers it either. Measured in the lab: the two relation entities
// were missing here, and every membership and channel link was lost in exactly that
// way.
func TestSyncedEntitiesCoversEveryEntity(t *testing.T) {
	index := map[string]bool{}
	for _, entity := range SyncedEntities() {
		if index[entity] {
			t.Fatalf("%s appears twice in SyncedEntities", entity)
		}
		index[entity] = true
	}
	for _, entity := range []string{
		EntityMonitor,
		EntityMonitorGroup,
		EntityMonitorGroupMember,
		EntityMonitorTemplate,
		EntityStatusPage,
		EntityNotification,
		EntityMonitorNotification,
	} {
		if !index[entity] {
			t.Errorf("%s is not in SyncedEntities: a change of it would be written to the outbox and never served", entity)
		}
	}
}

// TestSyncPeerSettled pins the rule behind the settle time: a peer may not take
// the leader role (or the notification duty) until it has been continuously
// reachable for the configured period, which is what stops a flapping node from
// grabbing it mid-incident.
func TestSyncPeerSettled(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const settle = 60 * time.Second

	cases := []struct {
		name        string
		onlineSince *time.Time
		settle      time.Duration
		want        bool
	}{
		{name: "never reached is never settled", onlineSince: nil, settle: settle, want: false},
		{name: "just came online", onlineSince: at(now), settle: settle, want: false},
		{name: "one second short", onlineSince: at(now.Add(-59 * time.Second)), settle: settle, want: false},
		{name: "exactly at the settle boundary", onlineSince: at(now.Add(-60 * time.Second)), settle: settle, want: true},
		{name: "online for an hour", onlineSince: at(now.Add(-time.Hour)), settle: settle, want: true},
		{name: "a zero settle time trusts immediately", onlineSince: at(now), settle: 0, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := SyncPeer{PeerNodeID: "up-node-2", OnlineSince: tc.onlineSince}
			if got := peer.Settled(now, tc.settle); got != tc.want {
				t.Fatalf("Settled = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestSettledPeers covers the list the leader derivation and the notification
// ownership consume: only the settled peers, always in node_id order so every
// node computes the same winner without coordinating.
func TestSettledPeers(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const settle = 60 * time.Second

	peers := []SyncPeer{
		{PeerNodeID: "up-node-3", OnlineSince: at(now.Add(-time.Hour))},       // settled
		{PeerNodeID: "up-node-1", OnlineSince: at(now)},                       // too recent
		{PeerNodeID: "up-node-2", OnlineSince: at(now.Add(-5 * time.Minute))}, // settled
		{PeerNodeID: "up-node-4", OnlineSince: nil},                           // never seen
	}

	got := SettledPeers(peers, now, settle)
	want := []string{"up-node-2", "up-node-3"}
	if len(got) != len(want) {
		t.Fatalf("got %d settled peers, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].PeerNodeID != id {
			t.Errorf("settled[%d] = %q, want %q", i, got[i].PeerNodeID, id)
		}
	}
}

// TestSettledPeersEmpty guards the degenerate cases the caller relies on.
func TestSettledPeersEmpty(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if got := SettledPeers(nil, now, time.Minute); len(got) != 0 {
		t.Fatalf("expected no settled peers, got %d", len(got))
	}
	only := []SyncPeer{{PeerNodeID: "up-node-2", OnlineSince: at(now.Add(-time.Hour))}}
	if got := SettledPeers(only, now, time.Minute); len(got) != 1 || got[0].PeerNodeID != "up-node-2" {
		t.Fatalf("expected the single settled peer, got %v", got)
	}
}
