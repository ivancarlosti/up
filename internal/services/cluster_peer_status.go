package services

import (
	"context"
	"sort"
	"time"

	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/version"
)

// PeerStatusReport is the payload of the local admin view
// GET /api/cluster/sync/status. It exists because in federated mode the
// dashboard shows a per-node view: without the peer lag and the last error, a
// stalled sync is invisible and the mode is undebuggable.
type PeerStatusReport struct {
	NodeID          string `json:"node_id"`
	Mode            string `json:"mode"`
	PeerAPIEnabled  bool   `json:"peer_api_enabled"`
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	// SettleSeconds is how long a peer must be continuously reachable before it
	// may take the leader role (and the notification duty).
	SettleSeconds int              `json:"settle_seconds"`
	Peers         []PeerStatusView `json:"peers"`
}

// PeerStatusView is one peer as this node currently sees it.
type PeerStatusView struct {
	PeerNodeID      string            `json:"peer_node_id"`
	PeerName        string            `json:"peer_name"`
	APIURL          string            `json:"api_url"`
	Version         string            `json:"peer_version"`
	ProtocolVersion int               `json:"protocol_version"`
	NodeStatus      models.NodeStatus `json:"node_status"`
	PeerStatus      string            `json:"peer_status"`
	Settled         bool              `json:"settled"`
	OnlineSince     *time.Time        `json:"online_since"`
	LastSeenAt      *time.Time        `json:"last_seen_at"`
	LastSuccessAt   *time.Time        `json:"last_success_at"`
	LastError       string            `json:"last_error"`
	LastErrorAt     *time.Time        `json:"last_error_at"`
}

// PeerStatusReport builds the local peer view.
//
// It merges the two sources on purpose: `sync_peers` holds what this node
// observed, `nodes` holds what the cluster registry knows. A peer registered by
// the join handshake but never pinged yet (the peer API was just enabled) still
// shows up, with an unknown peer status, instead of being invisible.
func (s *ClusterService) PeerStatusReport(ctx context.Context) (*PeerStatusReport, error) {
	var rows []models.SyncPeer
	if err := s.db.WithContext(ctx).Order("peer_node_id ASC").Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	nodes, err := s.Nodes(ctx)
	if err != nil {
		return nil, err
	}

	observed := make(map[string]models.SyncPeer, len(rows))
	for _, row := range rows {
		observed[row.PeerNodeID] = row
	}

	settle := time.Duration(s.cfg.ClusterLeaderSettleSeconds) * time.Second
	now := time.Now().UTC()
	views := make([]PeerStatusView, 0, len(nodes))
	for _, node := range nodes {
		if node.IsSelf {
			continue
		}
		row, ok := observed[node.NodeID]
		if !ok {
			row = models.SyncPeer{PeerNodeID: node.NodeID, Status: models.PeerStatusUnknown}
		}
		name := row.PeerName
		if name == "" {
			name = node.Name
		}
		apiURL := row.PeerAPIURL
		if apiURL == "" {
			apiURL = node.APIURL
		}
		views = append(views, PeerStatusView{
			PeerNodeID:      row.PeerNodeID,
			PeerName:        name,
			APIURL:          apiURL,
			Version:         row.PeerVersion,
			ProtocolVersion: row.ProtocolVersion,
			NodeStatus:      node.Status,
			PeerStatus:      row.Status,
			Settled:         row.Settled(now, settle),
			OnlineSince:     row.OnlineSince,
			LastSeenAt:      row.LastSeenAt,
			LastSuccessAt:   row.LastSuccessAt,
			LastError:       row.LastError,
			LastErrorAt:     row.LastErrorAt,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].PeerNodeID < views[j].PeerNodeID })

	return &PeerStatusReport{
		NodeID:          s.cfg.NodeID,
		Mode:            s.Mode(),
		PeerAPIEnabled:  s.cfg.ClusterPeerAPI,
		Version:         version.Readable(),
		ProtocolVersion: models.ProtocolVersion,
		SettleSeconds:   s.cfg.ClusterLeaderSettleSeconds,
		Peers:           views,
	}, nil
}
