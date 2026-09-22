package services

import (
	"context"
	"time"

	"github.com/ivancarlosti/up/internal/models"
)

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
// monitor (run_on: all | primary | node).
func participatesInVoting(monitor *models.Monitor, node models.Node) bool {
	switch monitor.RunOn {
	case "primary":
		return node.IsPrimary
	case "node":
		return node.NodeID == monitor.NodeID
	default:
		return true
	}
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
	heartbeats, err := s.stats.LatestPerNode(ctx, ids)
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
