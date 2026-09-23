package models

import (
	"testing"
	"time"
)

// testTime builds a fixed timestamp.
func testTime(second int) time.Time {
	return time.Date(2026, 1, 1, 12, 0, second, 0, time.UTC)
}

// TestSyncCandidateNewer pins the total order: revision first (so a wrong clock
// cannot make an old edit win), then the timestamp, then the node id.
//
// Every case is written as "is base (revision 3, node b) newer than other?", so
// the direction is explicit.
func TestSyncCandidateNewer(t *testing.T) {
	base := SyncCandidate{Revision: 3, UpdatedAt: testTime(0), OriginNodeID: "b"}

	cases := []struct {
		name  string
		other SyncCandidate
		want  bool
	}{
		{
			name:  "other has a higher revision, so base is not newer even with a newer clock",
			other: SyncCandidate{Revision: 4, UpdatedAt: testTime(-100), OriginNodeID: "a"},
			want:  false,
		},
		{
			name:  "other has a lower revision, so base wins even with an older clock",
			other: SyncCandidate{Revision: 2, UpdatedAt: testTime(500), OriginNodeID: "z"},
			want:  true,
		},
		{
			name:  "same revision, other is older: base wins",
			other: SyncCandidate{Revision: 3, UpdatedAt: testTime(-1), OriginNodeID: "a"},
			want:  true,
		},
		{
			name:  "same revision, other is newer: base loses",
			other: SyncCandidate{Revision: 3, UpdatedAt: testTime(1), OriginNodeID: "z"},
			want:  false,
		},
		{
			name:  "same revision and time, base has the greater node id: base wins",
			other: SyncCandidate{Revision: 3, UpdatedAt: testTime(0), OriginNodeID: "a"},
			want:  true,
		},
		{
			name:  "same revision and time, base has the smaller node id: base loses",
			other: SyncCandidate{Revision: 3, UpdatedAt: testTime(0), OriginNodeID: "c"},
			want:  false,
		},
		{
			name:  "an identical change is not newer, so a re-delivery is a no-op",
			other: base,
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := base.Newer(tc.other); got != tc.want {
				t.Fatalf("base.Newer(other) = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestSyncCandidateIsATotalOrder checks the property the merge depends on: for any
// two changes, exactly one of "a is newer", "b is newer" or "they are identical"
// holds. Without it two nodes could disagree about the winner.
func TestSyncCandidateIsATotalOrder(t *testing.T) {
	candidates := []SyncCandidate{
		{Revision: 1, UpdatedAt: testTime(0), OriginNodeID: "a"},
		{Revision: 1, UpdatedAt: testTime(0), OriginNodeID: "b"},
		{Revision: 1, UpdatedAt: testTime(1), OriginNodeID: "a"},
		{Revision: 2, UpdatedAt: testTime(-1), OriginNodeID: "a"},
		{Revision: 2, UpdatedAt: testTime(0), OriginNodeID: "a"},
	}

	for _, a := range candidates {
		for _, b := range candidates {
			ab, ba := a.Newer(b), b.Newer(a)
			switch {
			case ab && ba:
				t.Fatalf("both %+v and %+v claim to be newer", a, b)
			case !ab && !ba && a != b:
				t.Fatalf("%+v and %+v are neither newer nor identical", a, b)
			case !ab && !ba && a == b:
				// The only correct way for both to be false.
			}
		}
	}
}

// TestDecide covers the merge rule over the cases that actually matter: a new
// uuid, a stale re-delivery, an edit beating an older edit, and the tombstone
// behaviour in both directions.
func TestDecide(t *testing.T) {
	const uuid = "11111111-1111-4111-8111-111111111111"
	applied := &SyncObject{
		UUID: uuid, Entity: EntityMonitor, LocalID: 7,
		OriginNodeID: "up-node-1", Revision: 5, UpdatedAt: testTime(0),
	}
	other := SyncCandidate{Revision: 5, UpdatedAt: testTime(0), OriginNodeID: "up-node-2"}

	cases := []struct {
		name     string
		incoming SyncCandidate
		deleted  bool
		current  *SyncObject
		want     ApplyDecision
	}{
		{
			name:     "an unknown uuid is accepted",
			incoming: SyncCandidate{Revision: 1, UpdatedAt: testTime(0), OriginNodeID: "up-node-2"},
			current:  nil,
			want:     ApplyUpsert,
		},
		{
			name:     "a delete for an unknown uuid is still recorded",
			incoming: SyncCandidate{Revision: 1, UpdatedAt: testTime(0), OriginNodeID: "up-node-2"},
			deleted:  true,
			current:  nil,
			want:     ApplyDelete,
		},
		{
			name:     "a re-delivered change is a no-op",
			incoming: SyncCandidate{Revision: 5, UpdatedAt: testTime(0), OriginNodeID: "up-node-1"},
			current:  applied,
			want:     ApplySkip,
		},
		{
			name:     "an older change never overwrites a newer one",
			incoming: SyncCandidate{Revision: 4, UpdatedAt: testTime(600), OriginNodeID: "up-node-2"},
			current:  applied,
			want:     ApplySkip,
		},
		{
			name:     "a newer edit is applied",
			incoming: SyncCandidate{Revision: 6, UpdatedAt: testTime(-600), OriginNodeID: "up-node-2"},
			current:  applied,
			want:     ApplyUpsert,
		},
		{
			name:     "a same-revision edit from the higher id wins",
			incoming: other,
			current:  applied,
			want:     ApplyUpsert,
		},
		{
			name:     "a newer delete removes the row",
			incoming: SyncCandidate{Revision: 6, UpdatedAt: testTime(0), OriginNodeID: "up-node-2"},
			deleted:  true,
			current:  applied,
			want:     ApplyDelete,
		},
		{
			name:     "an old edit cannot resurrect a deleted row",
			incoming: SyncCandidate{Revision: 4, UpdatedAt: testTime(900), OriginNodeID: "up-node-2"},
			deleted:  false,
			current:  &SyncObject{UUID: uuid, Revision: 5, UpdatedAt: testTime(0), OriginNodeID: "up-node-1", DeletedAt: &[]time.Time{testTime(0)}[0]},
			want:     ApplySkip,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.incoming, tc.deleted, tc.current); got != tc.want {
				t.Fatalf("Decide = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEntityChecksum covers the healing trigger: it must be order independent,
// must change when a revision changes, and must change when a row appears or
// disappears.
func TestEntityChecksum(t *testing.T) {
	a := EntityIdentity{UUID: "aaa", Revision: 1}
	b := EntityIdentity{UUID: "bbb", Revision: 2}
	c := EntityIdentity{UUID: "ccc", Revision: 1}

	base := EntityChecksum([]EntityIdentity{a, b, c})
	if base != EntityChecksum([]EntityIdentity{c, a, b}) {
		t.Fatal("the checksum must not depend on the input order")
	}
	if base == EntityChecksum([]EntityIdentity{}) {
		t.Fatal("an empty entity cannot share the checksum of a populated one")
	}
	if base == EntityChecksum([]EntityIdentity{a, b}) {
		t.Fatal("a missing row must change the checksum")
	}
	if base == EntityChecksum([]EntityIdentity{a, EntityIdentity{UUID: "bbb", Revision: 3}, c}) {
		t.Fatal("a different revision must change the checksum")
	}
	if base != EntityChecksum([]EntityIdentity{a, b, c}) {
		t.Fatal("the checksum must be stable for the same input")
	}
}

// TestEntityChecksumEmptyIsStable guards the "nothing to sync yet" case: two
// fresh nodes must agree.
func TestEntityChecksumEmptyIsStable(t *testing.T) {
	if EntityChecksum(nil) != EntityChecksum([]EntityIdentity{}) {
		t.Fatal("nil and empty must hash the same")
	}
}
