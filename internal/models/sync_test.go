package models

import (
	"testing"
	"time"

	"github.com/google/uuid"
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

// TestRendezvousOwnerIsStableWhenTheSetChanges is the property that makes the
// notification election usable.
//
// With a modulo rule (`fnv1a(key) % len(candidates)`) a single peer joining or leaving
// reassigns roughly half of the monitors, so a blip would move the duty for most
// monitors at once — and while two nodes each believe they own the same monitor, one
// incident produces two alerts. Rendezvous only moves the keys of the node that
// actually changed.
func TestRendezvousOwnerIsStableWhenTheSetChanges(t *testing.T) {
	first := []string{"up-node-1", "up-node-2", "up-node-3"}
	keys := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		keys = append(keys, uuid.NewString())
	}

	before := map[string]string{}
	for _, key := range keys {
		before[key] = RendezvousOwner(key, first)
	}

	// A node leaves: only ITS keys may move.
	after := map[string]string{}
	for _, key := range keys {
		after[key] = RendezvousOwner(key, []string{"up-node-1", "up-node-3"})
	}
	moved, owned := 0, 0
	for _, key := range keys {
		if before[key] == "" {
			continue
		}
		if before[key] == "up-node-2" {
			continue
		}
		owned++
		if before[key] != after[key] {
			moved++
		}
	}
	if moved != 0 {
		t.Fatalf("%d of the %d monitors owned by the surviving nodes changed owner after an unrelated node left", moved, owned)
	}
}

// TestRendezvousOwnerIsDeterministicAndOrderFree pins the two properties the election
// depends on: every node must compute the same owner, and the answer must not depend on
// the order the candidates arrive in (the peers are read from a table).
func TestRendezvousOwnerIsDeterministicAndOrderFree(t *testing.T) {
	candidates := []string{"up-node-3", "up-node-1", "up-node-2"}
	shuffled := []string{"up-node-2", "up-node-3", "up-node-1"}
	key := "11111111-1111-4111-8111-111111111111"

	owner := RendezvousOwner(key, candidates)
	if owner == "" {
		t.Fatal("a non-empty candidate set must elect someone")
	}
	for i := 0; i < 10; i++ {
		if RendezvousOwner(key, candidates) != owner {
			t.Fatal("the owner must be stable for the same key and candidates")
		}
	}
	if RendezvousOwner(key, shuffled) != owner {
		t.Fatal("the owner must not depend on the order of the candidates")
	}
	if owner != RendezvousOwner(key, append([]string{}, candidates...)) {
		t.Fatal("the owner must not depend on the slice identity")
	}
}

// TestRendezvousOwnerEmpty guards the degenerate cases the caller relies on.
func TestRendezvousOwnerEmpty(t *testing.T) {
	if got := RendezvousOwner("key", nil); got != "" {
		t.Fatalf("no candidates must yield no owner, got %q", got)
	}
	if got := RendezvousOwner("key", []string{"", ""}); got != "" {
		t.Fatalf("empty candidate names must be skipped, got %q", got)
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
