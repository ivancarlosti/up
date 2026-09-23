package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// Peer request tuning.
const (
	// peerRequestTimeout bounds an outbound liveness ping. It is much shorter
	// than the join timeout on purpose: the ping runs every 30 s and a silent
	// peer must not hold the loop.
	peerRequestTimeout = 10 * time.Second
	// peerMaxResponseBytes caps what we read from a peer, so a wrong or hostile
	// endpoint cannot make the node buffer an unbounded answer.
	peerMaxResponseBytes = 1 << 20 // 1 MiB
)

// PingResponse is the payload of GET /api/cluster/sync/ping: who the peer is and
// which protocol it speaks. Per entity counts arrive with the manifest endpoint
// (phase 2), which is where they are actually consumed.
type PingResponse struct {
	NodeID          string    `json:"node_id"`
	NodeName        string    `json:"node_name"`
	APIURL          string    `json:"api_url"`
	Version         string    `json:"version"`
	ProtocolVersion int       `json:"protocol_version"`
	Mode            string    `json:"mode"`
	ClusterEnabled  bool      `json:"cluster_enabled"`
	UptimeSeconds   int64     `json:"uptime_seconds"`
	ServerTime      time.Time `json:"server_time"`
}

// AuthenticatePeer verifies an inbound node to node request and, when it is
// valid, refreshes the liveness of the caller.
//
// A correctly signed request is proof that the peer is alive, so liveness works
// in both directions and a node behind a one way firewall is still seen.
func (s *ClusterService) AuthenticatePeer(ctx context.Context, in PeerSignatureInput, presented string) error {
	if !s.cfg.ClusterPeerAPI {
		return ErrForbidden(i18n.CodeClusterDisabled, "the peer API is disabled on this node")
	}
	key, err := s.PrivateKey(ctx)
	if err != nil {
		return err
	}
	// The cause is logged server side; the caller only gets a 403.
	if err := s.peerAuth.Verify(key, in, presented, time.Now().UTC()); err != nil {
		s.log.Warn("rejected a peer request",
			"peer", in.NodeID, "method", in.Method, "target", in.Target, "error", err)
		return err
	}
	s.notePeerSeen(ctx, in.NodeID)
	return nil
}

// notePeerSeen refreshes the liveness of a peer this node just had contact with.
//
// It deliberately does not touch the peer's `nodes.status`: Sweep stays the only
// writer of that column, so the offline/degraded thresholds keep a single owner.
func (s *ClusterService) notePeerSeen(ctx context.Context, nodeID string) {
	if nodeID == "" || nodeID == s.cfg.NodeID {
		return
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("node_id = ?", nodeID).
		Updates(map[string]any{"last_heartbeat": now, "updated_at": now}).Error; err != nil {
		s.log.Warn("could not refresh the peer liveness", "peer", nodeID, "error", err)
	}
	if err := s.db.WithContext(ctx).Model(&models.SyncPeer{}).
		Where("peer_node_id = ?", nodeID).
		Update("last_seen_at", now).Error; err != nil {
		s.log.Warn("could not record the peer sighting", "peer", nodeID, "error", err)
	}
}

// EnsurePeerRow creates the local row of a peer if it is missing, so a node
// registered through the join handshake is tracked from the first ping.
func (s *ClusterService) EnsurePeerRow(ctx context.Context, nodeID, name, apiURL string) error {
	if nodeID == "" || nodeID == s.cfg.NodeID {
		return nil
	}
	row := models.SyncPeer{
		PeerNodeID: nodeID,
		PeerName:   name,
		PeerAPIURL: apiURL,
		Status:     models.PeerStatusUnknown,
		UpdatedAt:  time.Now().UTC(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "peer_node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"peer_name", "peer_api_url", "updated_at"}),
	}).Create(&row).Error
}

// PingPeers performs one signed liveness request per known peer. It runs at boot
// and then on the maintenance ticker, next to the local liveness refresh: the
// local row says "I am alive", the peer rows say "they answer".
func (s *ClusterService) PingPeers(ctx context.Context) {
	if !s.cfg.ClusterPeerAPI {
		return
	}
	nodes, err := s.peerTargets(ctx)
	if err != nil {
		s.log.Warn("could not list the peers to ping", "error", err)
		return
	}
	if len(nodes) == 0 {
		return
	}

	// Concurrent on purpose: a silent peer must not delay the others (the
	// timeout is PeerRequestTimeout on a 30 s loop).
	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go func(node models.Node) {
			defer wg.Done()
			s.pingPeer(ctx, node)
		}(node)
	}
	wg.Wait()
}

// peerTargets lists the nodes this node should ping.
func (s *ClusterService) peerTargets(ctx context.Context) ([]models.Node, error) {
	var nodes []models.Node
	err := s.db.WithContext(ctx).
		Where("node_id <> ?", s.cfg.NodeID).
		Where("api_url IS NOT NULL AND api_url <> ''").
		Order("node_id ASC").Find(&nodes).Error
	if err != nil {
		return nil, ErrInternal(err)
	}
	return nodes, nil
}

// pingPeer performs one signed request and records the outcome.
func (s *ClusterService) pingPeer(ctx context.Context, node models.Node) {
	response, err := s.fetchPing(ctx, node)
	if err != nil {
		s.recordPeerFailure(ctx, node.NodeID, err)
		return
	}
	s.recordPeerSuccess(ctx, node, response)
}

// fetchPing signs and sends GET /api/cluster/sync/ping to one peer.
func (s *ClusterService) fetchPing(ctx context.Context, node models.Node) (*PingResponse, error) {
	key, err := s.PrivateKey(ctx)
	if err != nil {
		return nil, err
	}
	target, err := peerURL(node.APIURL, "/api/cluster/sync/ping")
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, peerRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid peer url %q: %w", node.APIURL, err)
	}

	nonce, err := utils.RandomHex(16)
	if err != nil {
		return nil, err
	}
	// The signed target is the request URI as sent - not the path we asked for:
	// a peer published behind a path prefix (http://host/up) sees the prefixed
	// path, and the signature has to match what it computes.
	signed := PeerSignatureInput{
		Method:    http.MethodGet,
		Target:    req.URL.RequestURI(),
		Timestamp: strconv.FormatInt(time.Now().UTC().Unix(), 10),
		Nonce:     nonce,
		NodeID:    s.cfg.NodeID,
	}
	req.Header.Set(HeaderPeerNode, signed.NodeID)
	req.Header.Set(HeaderPeerTimestamp, signed.Timestamp)
	req.Header.Set(HeaderPeerNonce, signed.Nonce)
	req.Header.Set(HeaderPeerSignature, SignPeerRequest(key, signed))

	// The same guards as the join flow: the cluster key must never reach a host
	// the operator did not name, so redirects are reported instead of followed.
	client := &http.Client{
		Timeout:       peerRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, peerMaxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer answered %s: %s", resp.Status, truncateForLog(string(body), 200))
	}
	var out PingResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("peer answered with an unreadable payload: %w", err)
	}
	if out.NodeID == "" {
		return nil, fmt.Errorf("peer answered with an empty node identity")
	}
	return &out, nil
}

// recordPeerSuccess stores a successful ping: the peer identity and version, the
// moment it became continuously reachable (the settle input) and the liveness.
func (s *ClusterService) recordPeerSuccess(ctx context.Context, node models.Node, response *PingResponse) {
	now := time.Now().UTC()
	previous := s.loadPeerRow(ctx, node.NodeID)

	status := models.PeerStatusOnline
	note := ""
	switch {
	case response.ProtocolVersion != models.ProtocolVersion:
		status = models.PeerStatusIncompatible
		note = fmt.Sprintf("protocol version %d, this node speaks %d",
			response.ProtocolVersion, models.ProtocolVersion)
	case response.Mode != s.cfg.ClusterMode:
		status = models.PeerStatusIncompatible
		note = fmt.Sprintf("cluster mode %q, this node runs %q", response.Mode, s.cfg.ClusterMode)
	}

	// OnlineSince is set when the peer becomes reachable and left alone while
	// every exchange succeeds: that is what "continuously online" means, and it
	// is what stops a flapping peer from taking the leader role mid-incident.
	onlineSince := previous.OnlineSince
	if onlineSince == nil {
		onlineSince = &now
	}
	apiURL := strings.TrimSpace(response.APIURL)
	if apiURL == "" {
		apiURL = node.APIURL
	}
	row := models.SyncPeer{
		PeerNodeID:      node.NodeID,
		PeerName:        response.NodeName,
		PeerAPIURL:      apiURL,
		PeerVersion:     response.Version,
		ProtocolVersion: response.ProtocolVersion,
		Status:          status,
		LastSeenAt:      &now,
		LastSuccessAt:   &now,
		OnlineSince:     onlineSince,
		LastError:       note,
		UpdatedAt:       now,
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "peer_node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"peer_name", "peer_api_url", "peer_version", "protocol_version", "status",
			"last_seen_at", "last_success_at", "online_since", "last_error", "updated_at",
		}),
	}).Create(&row).Error; err != nil {
		s.log.Warn("could not store the peer status", "peer", node.NodeID, "error", err)
		return
	}

	// It answered, so its node row is refreshed too (Sweep keeps owning status).
	s.notePeerSeen(ctx, node.NodeID)

	// Logged on a transition only, so a healthy cluster stays quiet.
	if previous.Status != status {
		if status == models.PeerStatusIncompatible {
			s.log.Warn("peer speaks a different protocol, synchronisation with it is off",
				"peer", node.NodeID, "detail", note)
		} else {
			s.log.Info("peer reachable", "peer", node.NodeID, "version", response.Version)
		}
	}
}

// recordPeerFailure stores a failed ping.
//
// It deliberately leaves nodes.last_heartbeat alone: Sweep ages it out with the
// documented thresholds (60 s degraded, 120 s offline), so a single failed ping
// never flaps the peer status or restarts the settle timer prematurely.
func (s *ClusterService) recordPeerFailure(ctx context.Context, nodeID string, cause error) {
	now := time.Now().UTC()
	previous := s.loadPeerRow(ctx, nodeID)

	updates := map[string]any{
		"status":        models.PeerStatusError,
		"last_error":    truncateForLog(cause.Error(), 500),
		"last_error_at": now,
		// The settle timer restarts when the peer comes back.
		"online_since": nil,
		"updated_at":   now,
	}
	if err := s.db.WithContext(ctx).Model(&models.SyncPeer{}).
		Where("peer_node_id = ?", nodeID).Updates(updates).Error; err != nil {
		s.log.Warn("could not store the peer failure", "peer", nodeID, "error", err)
		return
	}
	if previous.Status != models.PeerStatusError {
		s.log.Warn("peer unreachable", "peer", nodeID, "error", cause)
	}
}

// loadPeerRow reads the local row of a peer, returning a zero row when there is
// none yet (first ping) instead of failing.
func (s *ClusterService) loadPeerRow(ctx context.Context, nodeID string) models.SyncPeer {
	var row models.SyncPeer
	if err := s.db.WithContext(ctx).Where("peer_node_id = ?", nodeID).First(&row).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			s.log.Warn("could not read the peer row", "peer", nodeID, "error", err)
		}
		return models.SyncPeer{}
	}
	return row
}

// peerURL joins the base URL of a peer with an endpoint path.
//
// The base goes through the same validator as the join flow: it must look like
// the URL of a node, so a signed request (which carries the cluster key inside
// the HMAC input) can never be pointed at gopher://, a URL with credentials or a
// relay service.
func peerURL(base, path string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	if trimmed == "" {
		return "", fmt.Errorf("the peer has no api_url")
	}
	if !clusterPrimaryURL.MatchString(trimmed) {
		return "", fmt.Errorf("invalid peer api_url %q: expected http(s)://host[:port][/path]", base)
	}
	return trimmed + path, nil
}

// truncateForLog shortens a message to fit a column or a log line, on rune
// boundaries so the result stays valid UTF-8.
func truncateForLog(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
