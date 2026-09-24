package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// votesRetention bounds how long a fetched verdict is kept.
//
// It is a STORAGE bound, not a correctness one: freshness is decided by the monitor's
// own freshness window when the vote is merged (see voteWindow), exactly as it is for
// a local heartbeat. The row survives a while longer only so that "this peer stopped
// reporting" can be told apart from "this peer reported this long ago".
const votesRetention = 24 * time.Hour

// ServeVotes answers GET /api/cluster/sync/votes: the verdicts of THIS node only.
//
// Only what is still fresh by this node's own window is published. The receiver
// applies the same rule again, so a vote that goes stale in transit is dropped there,
// and the payload stays proportional to the monitors that matter.
func (s *SyncService) ServeVotes(ctx context.Context) (*models.SyncVotesResponse, error) {
	if !s.Enabled() {
		return nil, ErrForbidden(i18n.CodeClusterDisabled, "synchronisation is not enabled on this node")
	}
	response := &models.SyncVotesResponse{
		NodeID:          s.cfg.NodeID,
		ProtocolVersion: models.ProtocolVersion,
		ServerTime:      time.Now().UTC(),
		Votes:           []models.PeerVotePayload{},
	}

	// The newest heartbeat of THIS node per monitor, in one query: the correlated
	// subquery is the same "latest per key" shape the statistics queries use.
	var rows []struct {
		UUID            string                 `gorm:"column:uuid"`
		IntervalSeconds int                    `gorm:"column:interval_seconds"`
		Status          models.HeartbeatStatus `gorm:"column:status"`
		LatencyMS       int64                  `gorm:"column:latency_ms"`
		Message         string                 `gorm:"column:message"`
		Important       bool                   `gorm:"column:important"`
		CheckedAt       time.Time              `gorm:"column:checked_at"`
	}
	if err := s.db.WithContext(ctx).Raw(`
		SELECT m.uuid AS uuid, m.interval_seconds AS interval_seconds, h.status AS status,
		       h.latency_ms AS latency_ms, h.message AS message, h.important AS important,
		       h.created_at AS checked_at
		  FROM monitors m
		  JOIN heartbeats h ON h.id = (
		       SELECT h2.id FROM heartbeats h2
		        WHERE h2.monitor_id = m.id AND h2.node_id = ?
		        ORDER BY h2.id DESC LIMIT 1)`, s.cfg.NodeID).Scan(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}

	now := time.Now().UTC()
	for _, row := range rows {
		window := voteWindow(&models.Monitor{IntervalSeconds: row.IntervalSeconds})
		if now.Sub(row.CheckedAt) > window {
			continue
		}
		response.Votes = append(response.Votes, models.PeerVotePayload{
			MonitorUUID: row.UUID,
			Status:      row.Status.String(),
			LatencyMS:   row.LatencyMS,
			Message:     row.Message,
			Important:   row.Important,
			CheckedAt:   row.CheckedAt,
		})
	}
	return response, nil
}

// PullVotes fetches every peer's verdicts and stores them.
//
// It runs more often than the configuration pull (half the sync interval) because a
// verdict is only useful while it is fresh: one that arrives after its window closed
// is worth nothing at all.
func (s *SyncService) PullVotes(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	peers, err := s.cluster.peerTargets(ctx)
	if err != nil {
		s.log.Warn("could not list the peers to fetch votes from", "error", err)
		return
	}
	if len(peers) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(peer models.Node) {
			defer wg.Done()
			response, err := s.fetchVotes(ctx, peer)
			if err != nil {
				// A peer that cannot answer the vote endpoint must not stop the pull: its
				// monitors simply count as "no verdict from that node", which is exactly
				// what the unavailable-node strategy is for.
				s.log.Debug("could not fetch the votes of a peer", "peer", peer.NodeID, "error", err)
				return
			}
			if response.ProtocolVersion != models.ProtocolVersion {
				s.log.Warn("a peer answers the votes endpoint with another protocol version",
					"peer", peer.NodeID, "protocol_version", response.ProtocolVersion)
				return
			}
			if err := s.storeVotes(ctx, peer.NodeID, response.Votes); err != nil {
				s.log.Warn("could not store the votes of a peer", "peer", peer.NodeID, "error", err)
			}
		}(peer)
	}
	wg.Wait()
	s.pruneVotes(ctx)
}

// storeVotes replaces this node's stored view of a peer's verdicts.
//
// A verdict is a snapshot, so the row is overwritten rather than appended to. A status
// this build does not understand is skipped with a warning instead of being mapped to
// "unknown": reporting a healthy monitor as unknown because a peer speaks a newer
// vocabulary would be a worse failure than ignoring that one verdict.
func (s *SyncService) storeVotes(ctx context.Context, peerNodeID string, votes []models.PeerVotePayload) error {
	if len(votes) == 0 {
		return nil
	}
	now := time.Now().UTC()
	stored := 0
	for _, vote := range votes {
		if vote.MonitorUUID == "" {
			continue
		}
		status, err := models.ParseHeartbeatStatus(vote.Status)
		if err != nil {
			s.log.Warn("ignoring a verdict with an unknown status",
				"peer", peerNodeID, "monitor_uuid", vote.MonitorUUID, "status", vote.Status)
			continue
		}
		entry := models.PeerVote{
			MonitorUUID: vote.MonitorUUID,
			NodeID:      peerNodeID,
			Status:      status,
			LatencyMS:   vote.LatencyMS,
			Message:     vote.Message,
			Important:   vote.Important,
			CheckedAt:   vote.CheckedAt,
			FetchedAt:   now,
		}
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "monitor_uuid"}, {Name: "node_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"status", "latency_ms", "message", "important", "checked_at", "fetched_at",
			}),
		}).Create(&entry).Error; err != nil {
			return fmt.Errorf("storing a verdict from %s: %w", peerNodeID, err)
		}
		stored++
	}
	if stored > 0 {
		s.log.Debug("stored the verdicts of a peer", "peer", peerNodeID, "votes", stored)
	}
	return nil
}

// fetchVotes pulls the verdicts of one peer.
func (s *SyncService) fetchVotes(ctx context.Context, peer models.Node) (*models.SyncVotesResponse, error) {
	var out models.SyncVotesResponse
	if err := s.cluster.peerRequest(ctx, peer, "/api/cluster/sync/votes", "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PruneVotes removes the verdicts older than the retention.
func (s *SyncService) PruneVotes(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("fetched_at < ?", before).Delete(&models.PeerVote{})
	if result.Error != nil {
		return 0, ErrInternal(result.Error)
	}
	return result.RowsAffected, nil
}

// pruneVotes is the opportunistic sweep the pull loop runs on its own cadence.
func (s *SyncService) pruneVotes(ctx context.Context) {
	if _, err := s.PruneVotes(ctx, time.Now().UTC().Add(-votesRetention)); err != nil {
		s.log.Warn("could not prune the stored verdicts", "error", err)
	}
}
