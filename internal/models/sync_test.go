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

// TestCursorExpiredPinsTheOffByOne guards the boundary the healing pass depends on.
//
// A cursor exactly one row before the oldest kept row is still usable (the next pull
// continues from it), so only a bigger gap means the changes in between are gone. An
// off-by-one here either re-heals a peer on every cycle or silently skips the pruned
// changes — the divergence the manifest exists to repair.
func TestCursorExpiredPinsTheOffByOne(t *testing.T) {
	cases := []struct {
		name     string
		oldestID int64
		since    int64
		want     bool
	}{
		{name: "no cursor yet is never expired", oldestID: 10, since: 0, want: false},
		{name: "an empty outbox cannot expire anything", oldestID: 0, since: 5, want: false},
		{name: "cursor at the oldest row", oldestID: 10, since: 10, want: false},
		{name: "cursor one before the oldest row", oldestID: 10, since: 9, want: false},
		{name: "cursor two before the oldest row", oldestID: 10, since: 8, want: true},
		{name: "cursor far before the oldest row", oldestID: 500, since: 3, want: true},
		{name: "cursor after the oldest row", oldestID: 10, since: 40, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CursorExpired(tc.oldestID, tc.since); got != tc.want {
				t.Fatalf("CursorExpired(%d, %d) = %t, want %t", tc.oldestID, tc.since, got, tc.want)
			}
		})
	}
}

// TestPickLeader pins the four cases the failover rests on.
func TestPickLeader(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const settle = 60 * time.Second
	settled := at(now.Add(-2 * time.Minute))
	fresh := at(now.Add(-5 * time.Second))

	cases := []struct {
		name        string
		selfID      string
		selfSettled bool
		peers       []SyncPeer
		want        string
	}{
		{
			name:   "a lone node leads immediately, settle or not",
			selfID: "up-node-2", selfSettled: false, peers: nil, want: "up-node-2",
		},
		{
			name:   "the lowest settled id wins over a higher one",
			selfID: "up-node-2", selfSettled: true,
			peers: []SyncPeer{{PeerNodeID: "up-node-1", OnlineSince: settled}},
			want:  "up-node-1",
		},
		{
			name:   "a settled node beats a fresher, lower one",
			selfID: "up-node-2", selfSettled: true,
			peers: []SyncPeer{{PeerNodeID: "up-node-1", OnlineSince: fresh}},
			want:  "up-node-2",
		},
		{
			name:   "nobody settled means nobody leads",
			selfID: "up-node-2", selfSettled: false,
			peers: []SyncPeer{{PeerNodeID: "up-node-1", OnlineSince: fresh}},
			want:  "",
		},
		{
			name:   "an offline peer is never the leader",
			selfID: "up-node-2", selfSettled: true,
			peers: []SyncPeer{{PeerNodeID: "up-node-1"}},
			want:  "up-node-2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PickLeader(tc.selfID, tc.selfSettled, tc.peers, now, settle); got != tc.want {
				t.Fatalf("PickLeader = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNotificationCandidates pins the candidate set, which is what makes the election
// deterministic: every node must derive the same list from its own view, and a node
// that is not settled must not appear as a candidate (it could not be reached to be
// punished for a duplicate).
func TestNotificationCandidates(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const settle = 60 * time.Second
	settled := at(now.Add(-2 * time.Minute))
	fresh := at(now.Add(-5 * time.Second))

	got := NotificationCandidates("up-node-2", true, []SyncPeer{
		{PeerNodeID: "up-node-3", OnlineSince: settled},
		{PeerNodeID: "up-node-1", OnlineSince: fresh},
	}, now, settle)
	want := []string{"up-node-2", "up-node-3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted by node id)", got, want)
		}
	}

	if lone := NotificationCandidates("up-node-9", false, nil, now, settle); len(lone) != 1 || lone[0] != "up-node-9" {
		t.Fatalf("a lone node must be a candidate immediately, got %v", lone)
	}
	if none := NotificationCandidates("up-node-9", false, []SyncPeer{{PeerNodeID: "up-node-1"}}, now, settle); len(none) != 0 {
		t.Fatalf("no settled node means no candidate, got %v", none)
	}
}

// TestMonitorRunsOnPinsTheFourRules is the single place the "who takes part" rule is
// checked.
//
// The scheduler asks it to decide who PROBES, and the evaluator to decide who VOTES. If
// those two answers could differ, a node would probe a monitor whose verdict it is not
// allowed to publish (or the reverse), which shows up as a monitor flapping between
// unknown and its real status.
func TestMonitorRunsOnPinsTheFourRules(t *testing.T) {
	cases := []struct {
		name      string
		monitor   Monitor
		nodeID    string
		isPrimary bool
		want      bool
	}{
		{name: "all: every node takes part", monitor: Monitor{RunOn: "all"}, nodeID: "up-node-2", want: true},
		{name: "empty run_on behaves like all", monitor: Monitor{}, nodeID: "up-node-2", want: true},
		{name: "primary: only the primary", monitor: Monitor{RunOn: "primary"}, nodeID: "up-node-2", isPrimary: true, want: true},
		{name: "primary: not a secondary", monitor: Monitor{RunOn: "primary"}, nodeID: "up-node-2", isPrimary: false, want: false},
		{name: "node: the named node", monitor: Monitor{RunOn: "node", NodeID: "up-node-3"}, nodeID: "up-node-3", want: true},
		{name: "node: another node", monitor: Monitor{RunOn: "node", NodeID: "up-node-3"}, nodeID: "up-node-2", want: false},
		{name: "some: a listed node", monitor: Monitor{RunOn: "some", RunOnNodes: "up-node-2,up-node-3"}, nodeID: "up-node-3", want: true},
		{name: "some: an unlisted node", monitor: Monitor{RunOn: "some", RunOnNodes: "up-node-2,up-node-3"}, nodeID: "up-node-4", want: false},
		{name: "an unknown value takes part (nobody probing is worse)", monitor: Monitor{RunOn: "whatever"}, nodeID: "up-node-2", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			monitor := tc.monitor
			if got := MonitorRunsOn(&monitor, tc.nodeID, tc.isPrimary); got != tc.want {
				t.Fatalf("MonitorRunsOn = %t, want %t", got, tc.want)
			}
		})
	}
	if MonitorRunsOn(nil, "up-node-1", true) {
		t.Fatal("a nil monitor takes part in nothing")
	}
}

// TestMonitorListedOnNodeMatchesWholeIds guards the suffix trap: a substring search would
// match "up-node-1" inside "up-node-11", so a monitor meant for one node would silently be
// probed — and alerted on — by another.
func TestMonitorListedOnNodeMatchesWholeIds(t *testing.T) {
	list := "up-node-1, up-node-11"
	if !MonitorListedOnNode(list, "up-node-11") {
		t.Fatal("a listed id with surrounding spaces must match")
	}
	if MonitorListedOnNode("up-node-11", "up-node-1") {
		t.Fatal("a shorter id must not match a longer one")
	}
	if MonitorListedOnNode(list, "") {
		t.Fatal("an empty node id never matches")
	}
	if MonitorListedOnNode("", "up-node-1") {
		t.Fatal("an empty list matches nothing")
	}
}

// TestNormalizeRunOnNodes pins the cleanup the operator's input gets: the stored list is
// what every node compares against, so it must be deterministic.
func TestNormalizeRunOnNodes(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"   ":                  "",
		" up-node-1 ":          "up-node-1",
		"up-node-2, up-node-1": "up-node-2,up-node-1",
		"up-node-1,,up-node-1": "up-node-1",
		"a,b,a,b,c":            "a,b,c",
		", ,up-node-3, ,":      "up-node-3",
	}
	for input, want := range cases {
		if got := NormalizeRunOnNodes(input); got != want {
			t.Fatalf("NormalizeRunOnNodes(%q) = %q, want %q", input, got, want)
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
