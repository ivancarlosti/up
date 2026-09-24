package models

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"time"
)

// SyncCandidate is the ordering key of one change: the two components of the
// merge rule plus the tie-break.
type SyncCandidate struct {
	Revision     int64
	UpdatedAt    time.Time
	OriginNodeID string
}

// Newer reports whether this change wins over other.
//
// The order is total and identical on every node, which is what lets each node
// reach the same verdict without coordinating:
//
//  1. the higher revision wins. Revision comes FIRST on purpose: a wrong clock
//     can then only affect the tie-break, never make an old edit win;
//  2. then the later updated_at;
//  3. then the lexicographically greater origin_node_id, purely so the order is
//     total (two nodes editing in the same second with the same revision would
//     otherwise both "win", leaving the outcome to network order).
//
// An exactly equal triple is NOT newer, which is what makes a re-delivered
// batch — a node that pulls the same cursor again after a crash — a no-op.
func (c SyncCandidate) Newer(other SyncCandidate) bool {
	if c.Revision != other.Revision {
		return c.Revision > other.Revision
	}
	if !c.UpdatedAt.Equal(other.UpdatedAt) {
		return c.UpdatedAt.After(other.UpdatedAt)
	}
	return c.OriginNodeID > other.OriginNodeID
}

// Candidate of a stored row, so the merge compares like with like.
func (o *SyncObject) Candidate() SyncCandidate {
	if o == nil {
		return SyncCandidate{}
	}
	return SyncCandidate{Revision: o.Revision, UpdatedAt: o.UpdatedAt, OriginNodeID: o.OriginNodeID}
}

// ApplyDecision is what a node must do with an incoming change.
type ApplyDecision int

const (
	// ApplySkip: the incoming change is not newer. Nothing to write (and no
	// conflict: losing to an older change is not a conflict, it is a re-delivery
	// or a stale pull).
	ApplySkip ApplyDecision = iota
	// ApplyUpsert: write the payload into the local row.
	ApplyUpsert
	// ApplyDelete: remove the local row, keeping the tombstone.
	ApplyDelete
)

// Decide is the merge rule itself: given an incoming change and the row this node
// already applied (nil when it never saw the uuid), it returns what to do.
//
// A tombstone is a change like any other, so it is compared by the same total
// order. That falls out correctly in both directions: a delete emitted after an
// edit has the higher revision and wins, and an edit that arrives late but was
// made BEFORE the delete loses to it — so a tombstone is never undone by a
// re-delivered old payload.
func Decide(incoming SyncCandidate, incomingDeleted bool, current *SyncObject) ApplyDecision {
	if current == nil {
		// Never seen: accept it. A delete for an unknown uuid is still recorded,
		// as a tombstone, so a late upsert cannot create the row afterwards.
		if incomingDeleted {
			return ApplyDelete
		}
		return ApplyUpsert
	}
	if !incoming.Newer(current.Candidate()) {
		return ApplySkip
	}
	if incomingDeleted {
		return ApplyDelete
	}
	return ApplyUpsert
}

// DiffSets compares the published state of a relation set with the desired one.
//
// It returns what has to be published as an upsert (added) and what has to be
// tombstoned (removed), each sorted so the outbox order is deterministic — two
// nodes running the same edit must produce the same sequence.
//
// A set that did not change returns nothing, which is what keeps a re-save with
// the same links from writing outbox rows for relations that never moved.
func DiffSets(before, now map[string]bool) (added, removed []string) {
	for key := range now {
		if !before[key] {
			added = append(added, key)
		}
	}
	for key := range before {
		if !now[key] {
			removed = append(removed, key)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// IsConflict reports whether a change that lost the merge was a concurrent edit
// rather than a stale re-delivery.
//
// The distinction is what makes the conflict log useful: two nodes editing the
// same record inside one sync interval is worth telling the operator about, while
// the same batch arriving twice is not.
func IsConflict(incoming SyncCandidate, current *SyncObject) bool {
	if current == nil {
		return false
	}
	// The same revision from a different node means both edits started from the
	// same base, so one of them had to lose.
	return incoming.Revision == current.Revision && incoming.OriginNodeID != current.OriginNodeID
}

// ConflictSides names the winner and the loser of a concurrent edit, whichever
// way the merge went.
//
// Two edits that share a revision have no "incoming" and "local" side: one of them
// simply wins the tie-break. Reporting only the case where the incoming change
// loses would make the conflict log miss half of the concurrent edits, and the
// design promises that the losing value is never silently dropped.
func ConflictSides(incoming SyncCandidate, current *SyncObject, incomingWon bool) (keptOrigin string, keptRevision int64, lostOrigin string, lostRevision int64) {
	if current == nil {
		return incoming.OriginNodeID, incoming.Revision, "", 0
	}
	if incomingWon {
		return incoming.OriginNodeID, incoming.Revision, current.OriginNodeID, current.Revision
	}
	return current.OriginNodeID, current.Revision, incoming.OriginNodeID, incoming.Revision
}

// EntityIdentity is one entry of a manifest checksum: the global identity of a
// row plus the revision this node holds.
type EntityIdentity struct {
	UUID     string
	Revision int64
}

// EntityChecksum hashes the identity of a whole entity ("every live row I have,
// and at which revision"). Two nodes that agree on it have the same set of rows
// at the same revisions, which is what the manifest pass compares.
//
// It is a pure function so the healing path can be tested exhaustively without a
// database: a checksum that quietly ignores a difference would turn the
// self-healing promise into "eventually maybe".
func EntityChecksum(rows []EntityIdentity) string {
	sorted := make([]EntityIdentity, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].UUID != sorted[j].UUID {
			return sorted[i].UUID < sorted[j].UUID
		}
		return sorted[i].Revision < sorted[j].Revision
	})

	digest := sha256.New()
	for _, row := range sorted {
		digest.Write([]byte(row.UUID))
		digest.Write([]byte{':'})
		digest.Write([]byte(strconv.FormatInt(row.Revision, 10)))
		digest.Write([]byte{'\n'})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
