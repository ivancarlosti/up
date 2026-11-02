package services

import (
	"context"
	"encoding/json"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// History assembles the detail page of a system: the row, the charts, the
// containers and the SMART report.
//
// In shared mode (one database) every node reads the same tables, so this is a
// local read. In federated mode the samples live only on the node that owns the
// agent connection, so a node that does not own it asks the owner over the peer
// API instead of replicating high volume rows (docs/hardware-monitoring.md). A
// peer that cannot be reached is not an error: the page renders with the local
// knowledge (name, host, owner) and says the history is unavailable, which is
// better than an empty page with a stack trace.
func (s *SystemService) History(ctx context.Context, systemID uint, hours int) (*models.SystemHistory, error) {
	view, err := s.Get(ctx, systemID)
	if err != nil {
		return nil, err
	}
	hours = models.NormalizeSystemChartWindow(hours)

	if s.remoteHistory(ctx, view) {
		return s.fetchRemoteHistory(ctx, view, hours)
	}

	metrics, err := s.Series(ctx, systemID, hours)
	if err != nil {
		return nil, err
	}
	containers, err := s.Containers(ctx, systemID)
	if err != nil {
		return nil, err
	}
	history := &models.SystemHistory{
		System:     view.System,
		Metrics:    metrics,
		Containers: containers,
	}
	if smart, err := s.Smart(ctx, systemID); err == nil && len(smart) == 1 {
		history.Smart = smart[0].Disks
		history.SmartOK = smart[0].Complete
		at := smart[0].CollectedAt
		history.SmartAt = &at
	}
	return history, nil
}

// remoteHistory reports whether the history of a system has to be read from
// another node.
//
// Only federated mode has per node databases: in shared mode the owner writes to
// the same tables this node reads, so a peer call would be a round trip for the
// answer it already has.
func (s *SystemService) remoteHistory(ctx context.Context, view *SystemView) bool {
	if s.cluster == nil || !s.cluster.Federated() {
		return false
	}
	if view.OwnerNode == "" || view.OwnerNode == s.nodeID() {
		return false
	}
	// The owner has to be a node this node knows how to reach.
	return s.cluster.PeerKnown(ctx, view.OwnerNode)
}

// fetchRemoteHistory asks the owning node for the history of a system.
func (s *SystemService) fetchRemoteHistory(ctx context.Context, view *SystemView, hours int) (*models.SystemHistory, error) {
	response, err := s.fetchOwnedHistory(ctx, view)
	if err != nil {
		// The page still renders: the row is local, only the charts are missing.
		s.log.Warn("could not read the history of a system from its owner",
			"system_id", view.ID, "owner", view.OwnerNode, "error", err)
		return &models.SystemHistory{System: view.System, Metrics: []models.SystemPoint{}}, nil
	}
	history := &models.SystemHistory{
		System:     view.System,
		Metrics:    response.Metrics,
		Containers: response.Containers,
		Smart:      response.Smart,
		SmartOK:    response.SmartOK,
		SmartAt:    response.SmartAt,
		Remote:     true,
	}
	return history, nil
}

// fetchOwnedHistory performs the peer call that answers with the history of a
// system this node does not own.
func (s *SystemService) fetchOwnedHistory(ctx context.Context, view *SystemView) (*models.SystemSeriesResponse, error) {
	query := "uuid=" + view.UUID
	var response models.SystemSeriesResponse
	if err := s.cluster.PeerGet(ctx, view.OwnerNode, "/api/cluster/sync/systems/history", query, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// ServeHistory answers GET /api/cluster/sync/systems/history for a peer.
//
// The answer carries the samples this node owns; a system this node does not own
// is reported as not found, because the caller already knows which node owns it and
// asking the wrong one must not be answered with an empty chart that looks like a
// machine that stopped reporting.
func (s *SystemService) ServeHistory(ctx context.Context, uuid string, hours int) (*models.SystemSeriesResponse, error) {
	var system models.System
	if err := s.db.WithContext(ctx).Where("uuid = ?", uuid).First(&system).Error; err != nil {
		return nil, ErrNotFound(i18n.CodeSystemNotFound, "system not found")
	}
	hours = models.NormalizeSystemChartWindow(hours)
	metrics, err := s.Series(ctx, system.ID, hours)
	if err != nil {
		return nil, err
	}
	containers, err := s.Containers(ctx, system.ID)
	if err != nil {
		return nil, err
	}
	response := &models.SystemSeriesResponse{
		ProtocolVersion: models.ProtocolVersion,
		UUID:            system.UUID,
		Hours:           hours,
		Metrics:         metrics,
		Containers:      containers,
		System:          &system,
	}
	if smart, err := s.Smart(ctx, system.ID); err == nil && len(smart) == 1 {
		var disks any
		if json.Unmarshal(smart[0].Disks, &disks) == nil {
			response.Smart = smart[0].Disks
		}
		response.SmartOK = smart[0].Complete
		at := smart[0].CollectedAt
		response.SmartAt = &at
	}
	return response, nil
}
