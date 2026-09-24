package services

import (
	"context"
	"sort"
	"time"

	"gorm.io/gorm"

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
	// Sync is the health of the synchronisation itself.
	Sync PeerSyncSummary `json:"sync"`
	// RecentConflicts and RecentDeadLetters are what went wrong lately, so an
	// operator never has to read a log file to find out (phase 5 exit criterion).
	RecentConflicts   []ConflictView   `json:"recent_conflicts"`
	RecentDeadLetters []DeadLetterView `json:"recent_dead_letters"`
}

// PeerSyncSummary is the synchronisation health this node can see by itself: what is
// waiting to be resolved, what was lost, and how much of the cluster it can see.
//
// It exists because the failure modes of a federated cluster are QUIET. A reference
// whose target never arrived, an edit that lost the merge, a change that could not be
// applied, and a partition that shrinks the vote denominator are invisible in a peer
// table, and every one of them ends up looking like "the two dashboards disagree".
type PeerSyncSummary struct {
	// PendingLinks are references waiting for a row that has not arrived; the ones
	// past the retry cap are listed separately, because they need an operator.
	PendingLinks        int `json:"pending_links"`
	PendingLinksGivenUp int `json:"pending_links_given_up"`
	// Conflicts are the edits that lost the merge, DeadLetters the changes this node
	// could not apply, of which Skipped were stepped over so the rest could flow.
	Conflicts   int `json:"conflicts"`
	DeadLetters int `json:"dead_letters"`
	Skipped     int `json:"dead_letters_skipped"`
	// Quorum is how much of the cluster this node can see. With the QUORUM failure
	// strategy the denominator is the REACHABLE nodes (decision D7), so an isolated
	// minority can report DOWN from a single vote: the split is shown here so a
	// partition is visible instead of hidden.
	Known     int `json:"quorum_known"`
	Reachable int `json:"quorum_reachable"`
	Settled   int `json:"quorum_settled"`
}

// ConflictView is one merge conflict as the operator sees it.
type ConflictView struct {
	Entity       string    `json:"entity"`
	UUID         string    `json:"uuid"`
	KeptOrigin   string    `json:"kept_origin"`
	KeptRevision int64     `json:"kept_revision"`
	LostOrigin   string    `json:"lost_origin"`
	LostRevision int64     `json:"lost_revision"`
	DetectedAt   time.Time `json:"detected_at"`
}

// DeadLetterView is one change this node could not apply.
type DeadLetterView struct {
	ChangeID  int64     `json:"change_id"`
	Entity    string    `json:"entity"`
	UUID      string    `json:"uuid"`
	Attempts  int       `json:"attempts"`
	Skipped   bool      `json:"skipped"`
	LastError string    `json:"last_error"`
	UpdatedAt time.Time `json:"updated_at"`
}

// recentSyncHistoryLimit bounds the conflict and dead-letter lists of the report: the
// UI shows what went wrong LATELY, and the counts above carry the totals.
const recentSyncHistoryLimit = 20

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
	// --- Synchronisation progress -------------------------------------------
	// LastChangeID is this node's cursor into the peer's outbox. A cursor that stops
	// advancing is the first symptom of a stalled sync, and it is invisible in the
	// peer status alone (the peer stays "online" while nothing flows).
	LastChangeID int64 `json:"last_change_id"`
	// LastManifestAt/LastManifestOK record the healing pass: when the checksums were
	// compared and whether they agreed. A peer that lags is behind; a peer whose
	// checksums disagree is DIVERGING, and only this pair shows the difference.
	LastManifestAt *time.Time `json:"last_manifest_at"`
	LastManifestOK bool       `json:"last_manifest_ok"`
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

			LastChangeID:   row.LastChangeID,
			LastManifestAt: row.LastManifestAt,
			LastManifestOK: row.LastManifestOK,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].PeerNodeID < views[j].PeerNodeID })

	summary, err := s.syncSummary(ctx, len(nodes), views)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.recentConflicts(ctx)
	if err != nil {
		return nil, err
	}
	deadLetters, err := s.recentDeadLetters(ctx)
	if err != nil {
		return nil, err
	}

	return &PeerStatusReport{
		NodeID:            s.cfg.NodeID,
		Mode:              s.Mode(),
		PeerAPIEnabled:    s.cfg.ClusterPeerAPI,
		Version:           version.Readable(),
		ProtocolVersion:   models.ProtocolVersion,
		SettleSeconds:     s.cfg.ClusterLeaderSettleSeconds,
		Peers:             views,
		Sync:              summary,
		RecentConflicts:   conflicts,
		RecentDeadLetters: deadLetters,
	}, nil
}

// syncSummary counts what the synchronisation is holding and how much of the cluster
// this node can see.
func (s *ClusterService) syncSummary(ctx context.Context, known int, views []PeerStatusView) (PeerSyncSummary, error) {
	var pending, givenUp, conflicts, deadLetters, skipped int64
	counts := []struct {
		target *int64
		apply  func(*gorm.DB) *gorm.DB
	}{
		{&pending, func(q *gorm.DB) *gorm.DB { return q.Model(&models.SyncPendingLink{}) }},
		{&givenUp, func(q *gorm.DB) *gorm.DB {
			return q.Model(&models.SyncPendingLink{}).Where("attempts >= ?", maxPendingLinkAttempts)
		}},
		{&conflicts, func(q *gorm.DB) *gorm.DB { return q.Model(&models.SyncConflict{}) }},
		{&deadLetters, func(q *gorm.DB) *gorm.DB { return q.Model(&models.SyncDeadLetter{}) }},
		{&skipped, func(q *gorm.DB) *gorm.DB {
			return q.Model(&models.SyncDeadLetter{}).Where("skipped = ?", true)
		}},
	}
	for _, count := range counts {
		if err := count.apply(s.db.WithContext(ctx)).Count(count.target).Error; err != nil {
			return PeerSyncSummary{}, ErrInternal(err)
		}
	}

	// This node is always reachable and settled to itself, so both counts start at one:
	// a quorum of one must not read as "no nodes are reachable".
	reachable, settled := 1, 1
	for _, view := range views {
		if view.PeerStatus == models.PeerStatusOnline {
			reachable++
		}
		if view.Settled {
			settled++
		}
	}
	return PeerSyncSummary{
		PendingLinks:        int(pending),
		PendingLinksGivenUp: int(givenUp),
		Conflicts:           int(conflicts),
		DeadLetters:         int(deadLetters),
		Skipped:             int(skipped),
		Known:               known,
		Reachable:           reachable,
		Settled:             settled,
	}, nil
}

// recentConflicts lists the edits that lost the merge, newest first.
func (s *ClusterService) recentConflicts(ctx context.Context) ([]ConflictView, error) {
	var rows []models.SyncConflict
	if err := s.db.WithContext(ctx).Order("id DESC").Limit(recentSyncHistoryLimit).
		Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	views := make([]ConflictView, 0, len(rows))
	for _, row := range rows {
		views = append(views, ConflictView{
			Entity:       row.Entity,
			UUID:         row.UUID,
			KeptOrigin:   row.KeptOrigin,
			KeptRevision: row.KeptRevision,
			LostOrigin:   row.LostOrigin,
			LostRevision: row.LostRevision,
			DetectedAt:   row.DetectedAt,
		})
	}
	return views, nil
}

// recentDeadLetters lists the changes this node could not apply, newest first.
func (s *ClusterService) recentDeadLetters(ctx context.Context) ([]DeadLetterView, error) {
	var rows []models.SyncDeadLetter
	if err := s.db.WithContext(ctx).Order("updated_at DESC").Limit(recentSyncHistoryLimit).
		Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	views := make([]DeadLetterView, 0, len(rows))
	for _, row := range rows {
		views = append(views, DeadLetterView{
			ChangeID:  row.ChangeID,
			Entity:    row.Entity,
			UUID:      row.UUID,
			Attempts:  row.Attempts,
			Skipped:   row.Skipped,
			LastError: row.LastError,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return views, nil
}
