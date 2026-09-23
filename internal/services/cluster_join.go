package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// JoinRequest is the payload of POST /api/cluster/join.
//
// Two shapes are accepted by the same endpoint:
//
//   - With primary_url: sent by the UI of the JOINING node. Up then performs an
//     outbound request to the primary and reports the result.
//   - Without primary_url: sent by the joining node to the primary (server to
//     server). The private key is validated against the shared key.
type JoinRequest struct {
	PrimaryURL string `json:"primary_url"`
	PrivateKey string `json:"private_key"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	// APIURL is the URL other nodes use to reach the joining node.
	APIURL string `json:"api_url,omitempty"`
}

// JoinResponse is returned by both flavors of the join endpoint.
type JoinResponse struct {
	Status  *models.ClusterStatus `json:"status"`
	NodeID  string                `json:"node_id"`
	Joined  bool                  `json:"joined"`
	Message string                `json:"message"`
}

// clusterPrimaryURL matches the URL of a peer node: an http(s) scheme, a host
// name or IP address (v4, or v6 between brackets), an optional port and an
// optional path. It is the guard that keeps the join request - which carries the
// cluster private key in its headers - from being pointed at something that is
// not a node of the cluster (file://, gopher://, a URL with credentials, a relay
// service). See `go/request-forgery` and docs/clustering.md §3.
var clusterPrimaryURL = regexp.MustCompile(`(?i)^https?://(?:[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?|\[[0-9a-f:.]+\])(?::[0-9]{1,5})?(?:/[^\s?#]*)?$`)

// Join (outbound) asks the primary node to register this instance.
func (s *ClusterService) Join(ctx context.Context, req JoinRequest) (*JoinResponse, error) {
	if !s.cfg.ClusterEnabled {
		return nil, ErrForbidden(i18n.CodeClusterDisabled,
			"set CLUSTER_ENABLED=true on this node before joining a cluster")
	}
	primaryURL := strings.TrimRight(strings.TrimSpace(req.PrimaryURL), "/")
	if primaryURL == "" {
		return nil, ErrBadRequest(i18n.CodeValidation, "primary_url is required")
	}
	// Validate before anything is sent: the outbound request below carries the
	// cluster key, so the URL has to look like the URL of a node.
	if !clusterPrimaryURL.MatchString(primaryURL) {
		return nil, ErrBadRequest(i18n.CodeValidation,
			fmt.Sprintf("invalid primary_url %q: expected http(s)://host[:port][/path]", primaryURL))
	}
	key, err := s.PrivateKey(ctx)
	if err != nil {
		return nil, err
	}
	// The key typed by the operator wins: that is the primary's key.
	if strings.TrimSpace(req.PrivateKey) != "" {
		key = strings.TrimSpace(req.PrivateKey)
	}

	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID == "" {
		nodeID = s.cfg.NodeID
	}
	nodeName := strings.TrimSpace(req.NodeName)
	if nodeName == "" {
		nodeName = s.cfg.NodeName
	}

	payload := JoinRequest{PrivateKey: key, NodeID: nodeID, NodeName: nodeName, APIURL: s.cfg.AppURL}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrInternal(err)
	}

	endpoint := primaryURL + "/api/cluster/join"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ErrBadRequest(i18n.CodeClusterJoinFailed, fmt.Sprintf("invalid primary_url %q", primaryURL))
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Cluster-Key", key)

	client := &http.Client{
		Timeout: 20 * time.Second,
		// Following a redirect would send the cluster key to a host the operator
		// never named: the redirect is reported instead (see the switch below).
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, ErrBadRequest(i18n.CodeClusterJoinFailed,
			fmt.Sprintf("could not reach the primary node at %s: %v", primaryURL, err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest:
		return nil, ErrBadRequest(i18n.CodeClusterJoinFailed,
			fmt.Sprintf("the primary node answered %s: join in using the URL it redirects to", resp.Status))
	case resp.StatusCode == http.StatusForbidden:
		return nil, ErrForbidden(i18n.CodeClusterKeyInvalid, "the primary node rejected the provided private key")
	case resp.StatusCode >= http.StatusBadRequest:
		return nil, ErrBadRequest(i18n.CodeClusterJoinFailed,
			fmt.Sprintf("primary node answered %s: %s", resp.Status, strings.TrimSpace(string(raw))))
	}

	// Store the shared key locally too, so this node can validate sessions and
	// accept further nodes.
	if err := s.settings.Set(ctx, models.SettingClusterPrivateKey, key); err != nil {
		s.log.Warn("could not persist the cluster private key", "error", err)
	}

	// Mirror the primary's node registry so the local view of the cluster
	// matches: the node reachable at primary_url becomes the primary and the
	// joining node is flagged as secondary.
	if err := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("node_id = ?", s.cfg.NodeID).
		Updates(map[string]any{"is_primary": false, "updated_at": time.Now().UTC()}).Error; err != nil {
		s.log.Warn("could not update the local node role", "error", err)
	}

	var primaryResponse JoinResponse
	if err := json.Unmarshal(raw, &primaryResponse); err == nil && primaryResponse.Status != nil {
		for _, node := range primaryResponse.Status.Nodes {
			if node.NodeID == s.cfg.NodeID {
				continue
			}
			upserted := models.Node{
				NodeID:    node.NodeID,
				Name:      node.Name,
				APIURL:    node.APIURL,
				IsPrimary: node.IsPrimary,
				Status:    node.Status,
			}
			if err := s.UpsertNode(ctx, upserted); err != nil {
				s.log.Warn("could not mirror the primary node registry", "node_id", node.NodeID, "error", err)
			}
			// Same reason as in RegisterNode: the ping loop walks the peer table.
			if err := s.EnsurePeerRow(ctx, node.NodeID, node.Name, node.APIURL); err != nil {
				s.log.Warn("could not register the peer row", "node_id", node.NodeID, "error", err)
			}
		}
	} else {
		// Fall back to the URL the operator typed.
		s.log.Warn("could not parse the primary node registry; using primary_url")
		fallback := models.Node{
			NodeID:    "primary",
			Name:      "Primary node",
			APIURL:    primaryURL,
			IsPrimary: true,
		}
		if err := s.UpsertNode(ctx, fallback); err != nil {
			s.log.Warn("could not store the primary node reference", "error", err)
		}
	}

	status, err := s.Status(ctx)
	if err != nil {
		return nil, err
	}
	s.log.Info("joined the cluster", "primary", primaryURL, "node_id", s.cfg.NodeID)
	s.publish("cluster.nodes", nil)
	return &JoinResponse{Status: status, NodeID: s.cfg.NodeID, Joined: true, Message: "node joined the cluster"}, nil
}
