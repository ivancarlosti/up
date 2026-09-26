package services

import (
	"context"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

// peerVotes reads the verdicts fetched from the other nodes, for the merge in
// EvaluateAll.
//
// The READ lives here and the WRITE in the sync service on purpose: the evaluator is
// the only consumer, so the aggregation does not have to know anything about the peer
// protocol — and the sync service keeps the table bounded (its retention prune), while
// freshness is decided by the monitor's window right below.
func (s *ClusterService) peerVotes(ctx context.Context) ([]models.PeerVote, error) {
	var votes []models.PeerVote
	if err := s.db.WithContext(ctx).Find(&votes).Error; err != nil {
		return nil, ErrInternal(err)
	}
	return votes, nil
}

// voteWindow is how long a heartbeat is considered valid for the voting. It
// grows with the monitor interval so slow monitors are not marked stale.
func voteWindow(monitor *models.Monitor) time.Duration {
	window := time.Duration(monitor.IntervalSeconds*2) * time.Second
	if window < 2*time.Minute {
		window = 2 * time.Minute
	}
	return window
}

// participatesInVoting tells whether a given node is expected to vote for a
// monitor (run_on: all | primary | node | some).
//
// It is the SAME rule the scheduler uses to decide who probes (models.MonitorRunsOn),
// asked about another node: if the two answers could differ, a node would probe a monitor
// whose verdict it is not allowed to publish.
func participatesInVoting(monitor *models.Monitor, node models.Node) bool {
	return models.MonitorRunsOn(monitor, node.NodeID, node.IsPrimary)
}

// EvaluateAll implements VoteProvider: it merges the latest heartbeat of every
// node into a single status per monitor.
func (s *ClusterService) EvaluateAll(ctx context.Context, monitors []*models.Monitor) (map[uint][]models.NodeVote, map[uint]models.AggregateStatus, error) {
	votesByMonitor := map[uint][]models.NodeVote{}
	statusByMonitor := map[uint]models.AggregateStatus{}
	if len(monitors) == 0 {
		return votesByMonitor, statusByMonitor, nil
	}

	nodes, err := s.Nodes(ctx)
	if err != nil {
		return nil, nil, err
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}

	ids := make([]uint, 0, len(monitors))
	for _, m := range monitors {
		ids = append(ids, m.ID)
	}
	// Only a heartbeat inside the monitor's vote window can produce a valid verdict,
	// so the lookup is bounded by the widest window of the batch instead of scanning
	// the whole history: everything older is dropped right below anyway.
	maxWindow := 2 * time.Minute
	for _, monitor := range monitors {
		if window := voteWindow(monitor); window > maxWindow {
			maxWindow = window
		}
	}
	heartbeats, err := s.stats.LatestPerNode(ctx, ids, time.Now().UTC().Add(-maxWindow))
	if err != nil {
		return nil, nil, err
	}
	latest := map[uint]map[string]models.Heartbeat{}
	for _, hb := range heartbeats {
		if latest[hb.MonitorID] == nil {
			latest[hb.MonitorID] = map[string]models.Heartbeat{}
		}
		latest[hb.MonitorID][hb.NodeID] = hb
	}

	now := time.Now().UTC()

	// In federated mode the peers' verdicts are NOT in the heartbeats table: they are
	// fetched into peer_votes, keyed by the monitor UUID because a peer's local ids are
	// its own. Merging them here is the only change the mode needs — every rule below
	// (participation, the freshness window, the aggregation) is untouched, so a shared
	// and a federated cluster decide with the same code and the same tests.
	if s.Federated() {
		peerVotes, err := s.peerVotes(ctx)
		if err != nil {
			return nil, nil, err
		}
		byUUID := make(map[string]uint, len(monitors))
		for _, monitor := range monitors {
			byUUID[monitor.UUID] = monitor.ID
		}
		for _, vote := range peerVotes {
			localID, ok := byUUID[vote.MonitorUUID]
			if !ok {
				// A verdict for a monitor this node does not have (yet): it will be used
				// as soon as the monitor arrives.
				continue
			}
			if latest[localID] == nil {
				latest[localID] = map[string]models.Heartbeat{}
			}
			if _, measured := latest[localID][vote.NodeID]; measured {
				// This node's own heartbeat wins: it is the same measurement, only fresher.
				continue
			}
			latest[localID][vote.NodeID] = models.Heartbeat{
				MonitorID: localID,
				NodeID:    vote.NodeID,
				Status:    vote.Status,
				LatencyMS: vote.LatencyMS,
				Message:   vote.Message,
				Important: vote.Important,
				CreatedAt: vote.CheckedAt,
			}
		}
	}

	for _, monitor := range monitors {
		window := voteWindow(monitor)
		votes := make([]models.NodeVote, 0, len(nodes))
		for _, node := range nodes {
			if !participatesInVoting(monitor, node) && node.NodeID != monitor.NodeID {
				continue
			}
			vote := models.NodeVote{
				NodeID:   node.NodeID,
				NodeName: node.Name,
				Status:   models.AggregateUnknown,
				Online:   node.Status != models.NodeStatusOffline,
			}
			if hb, ok := latest[monitor.ID][node.NodeID]; ok {
				checked := hb.CreatedAt
				vote.CheckedAt = &checked
				vote.LatencyMS = hb.LatencyMS
				vote.Message = hb.Message
				if now.Sub(hb.CreatedAt) <= window {
					vote.Status = heartbeatToAggregate(hb.Status)
				}
			}
			votes = append(votes, vote)
		}
		votesByMonitor[monitor.ID] = votes
		statusByMonitor[monitor.ID] = AggregateVotes(votes, settings.FailureStrategy,
			settings.NodeUnavailableStrategy, monitor.Active)
	}
	return votesByMonitor, statusByMonitor, nil
}

// Evaluate computes the aggregated status of a single monitor.
func (s *ClusterService) Evaluate(ctx context.Context, monitor *models.Monitor) (models.AggregateStatus, []models.NodeVote, error) {
	votesByMonitor, statusByMonitor, err := s.EvaluateAll(ctx, []*models.Monitor{monitor})
	if err != nil {
		return models.AggregateUnknown, nil, err
	}
	return statusByMonitor[monitor.ID], votesByMonitor[monitor.ID], nil
}

// AggregateVotes is the pure core of the failure strategies:
//
//	ANY_NODE_FAILS : down as soon as one online node fails.
//	ALL_NODES_FAIL : down only when every online node fails (default).
//	QUORUM         : down when the majority fails, degraded on a partial failure.
//
// Offline nodes never vote; with MARK_DEGRADED their absence is surfaced as a
// degraded state instead of being silently ignored.
func AggregateVotes(votes []models.NodeVote, strategy models.FailureStrategy,
	unavailable models.NodeUnavailableStrategy, active bool) models.AggregateStatus {

	if !active {
		return models.AggregateMaintenance
	}

	total, down := 0, 0
	offline := 0
	for _, vote := range votes {
		if !vote.Online {
			offline++
			continue
		}
		switch vote.Status {
		case models.AggregateUp, models.AggregateDown, models.AggregatePending:
			total++
			if vote.Status == models.AggregateDown {
				down++
			}
		}
	}

	if total == 0 {
		return models.AggregatePending
	}

	switch strategy {
	case models.FailureStrategyAnyNodeFails:
		if down > 0 {
			return models.AggregateDown
		}
	case models.FailureStrategyQuorum:
		if down*2 > total {
			return models.AggregateDown
		}
		if down > 0 {
			return models.AggregateDegraded
		}
	default: // FailureStrategyAllNodesFail
		if down == total {
			return models.AggregateDown
		}
	}

	if unavailable == models.NodeUnavailableMarkDegraded && offline > 0 {
		return models.AggregateDegraded
	}
	return models.AggregateUp
}

// heartbeatToAggregate maps a single heartbeat result to the aggregated scale.
func heartbeatToAggregate(status models.HeartbeatStatus) models.AggregateStatus {
	switch status {
	case models.StatusUp:
		return models.AggregateUp
	case models.StatusDown:
		return models.AggregateDown
	case models.StatusPending:
		return models.AggregatePending
	case models.StatusMaintenance:
		return models.AggregateMaintenance
	}
	return models.AggregateUnknown
}
