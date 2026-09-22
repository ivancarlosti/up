package services

import (
	"testing"

	"github.com/ivancarlosti/up/internal/models"
)

// vote is a tiny helper to build a node vote.
func vote(nodeID string, status models.AggregateStatus, online bool) models.NodeVote {
	return models.NodeVote{NodeID: nodeID, NodeName: nodeID, Status: status, Online: online}
}

// TestAggregateVotes covers every failure strategy plus the node unavailable
// rules: this is the heart of the cluster voting, so it is tested exhaustively
// with table driven cases instead of relying on the integration environment.
func TestAggregateVotes(t *testing.T) {
	allUp := []models.NodeVote{vote("a", models.AggregateUp, true), vote("b", models.AggregateUp, true), vote("c", models.AggregateUp, true)}
	oneDown := []models.NodeVote{vote("a", models.AggregateDown, true), vote("b", models.AggregateUp, true), vote("c", models.AggregateUp, true)}
	twoDown := []models.NodeVote{vote("a", models.AggregateDown, true), vote("b", models.AggregateDown, true), vote("c", models.AggregateUp, true)}
	allDown := []models.NodeVote{vote("a", models.AggregateDown, true), vote("b", models.AggregateDown, true), vote("c", models.AggregateDown, true)}
	twoOnlineOneOffline := []models.NodeVote{vote("a", models.AggregateUp, true), vote("b", models.AggregateUp, true), vote("c", models.AggregateDown, false)}
	allOffline := []models.NodeVote{vote("a", models.AggregateDown, false), vote("b", models.AggregateDown, false)}
	pending := []models.NodeVote{vote("a", models.AggregateUnknown, true), vote("b", models.AggregateUnknown, true)}

	cases := []struct {
		name        string
		votes       []models.NodeVote
		strategy    models.FailureStrategy
		unavailable models.NodeUnavailableStrategy
		active      bool
		want        models.AggregateStatus
	}{
		{"inactive monitor is maintenance", allUp, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, false, models.AggregateMaintenance},
		{"no vote yet is pending", nil, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregatePending},
		{"unknown votes are pending", pending, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregatePending},

		{"all up => up", allUp, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregateUp},
		{"all_nodes_fail ignores a single failure", oneDown, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregateUp},
		{"all_nodes_fail is down when all fail", allDown, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregateDown},

		{"any_node_fails is down on a single failure", oneDown, models.FailureStrategyAnyNodeFails, models.NodeUnavailableIgnore, true, models.AggregateDown},
		{"any_node_fails stays up when all are up", allUp, models.FailureStrategyAnyNodeFails, models.NodeUnavailableIgnore, true, models.AggregateUp},

		{"quorum majority failure is down", twoDown, models.FailureStrategyQuorum, models.NodeUnavailableIgnore, true, models.AggregateDown},
		{"quorum partial failure is degraded", oneDown, models.FailureStrategyQuorum, models.NodeUnavailableIgnore, true, models.AggregateDegraded},
		{"quorum without failures is up", allUp, models.FailureStrategyQuorum, models.NodeUnavailableIgnore, true, models.AggregateUp},

		{"offline nodes do not vote", twoOnlineOneOffline, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregateUp},
		{"all nodes offline falls back to pending", allOffline, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregatePending},
		{"ignore keeps the status silent", twoOnlineOneOffline, models.FailureStrategyAllNodesFail, models.NodeUnavailableIgnore, true, models.AggregateUp},
		{"mark_degraded warns about an offline node", twoOnlineOneOffline, models.FailureStrategyAllNodesFail, models.NodeUnavailableMarkDegraded, true, models.AggregateDegraded},
		{"mark_degraded still reports a real outage", allDown, models.FailureStrategyAllNodesFail, models.NodeUnavailableMarkDegraded, true, models.AggregateDown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AggregateVotes(tc.votes, tc.strategy, tc.unavailable, tc.active)
			if got != tc.want {
				t.Fatalf("AggregateVotes() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVoteWindow makes sure slow monitors get a proportionally larger window.
func TestVoteWindow(t *testing.T) {
	cases := []struct {
		interval int
		want     int // seconds
	}{
		{10, 120},
		{60, 120},
		{120, 240},
		{300, 600},
	}
	for _, tc := range cases {
		monitor := &models.Monitor{IntervalSeconds: tc.interval}
		if got := int(voteWindow(monitor).Seconds()); got != tc.want {
			t.Errorf("voteWindow(%ds) = %ds, want %ds", tc.interval, got, tc.want)
		}
	}
}

// TestParticipatesInVoting documents the run_on semantics.
func TestParticipatesInVoting(t *testing.T) {
	primary := models.Node{NodeID: "n1", IsPrimary: true}
	secondary := models.Node{NodeID: "n2"}

	cases := []struct {
		name    string
		monitor models.Monitor
		node    models.Node
		want    bool
	}{
		{"all runs everywhere (primary)", models.Monitor{RunOn: "all"}, primary, true},
		{"all runs everywhere (secondary)", models.Monitor{RunOn: "all"}, secondary, true},
		{"primary only on the primary", models.Monitor{RunOn: "primary"}, primary, true},
		{"primary not on secondary", models.Monitor{RunOn: "primary"}, secondary, false},
		{"node pin matches", models.Monitor{RunOn: "node", NodeID: "n2"}, secondary, true},
		{"node pin mismatch", models.Monitor{RunOn: "node", NodeID: "n9"}, secondary, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := participatesInVoting(&tc.monitor, tc.node); got != tc.want {
				t.Fatalf("participatesInVoting() = %v, want %v", got, tc.want)
			}
		})
	}
}
