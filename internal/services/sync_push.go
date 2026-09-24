package services

import (
	"context"
	"strings"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// RequestPull runs one immediate pull of a peer, on behalf of an inbound push
// notification (POST /api/cluster/sync/now).
//
// It answers at once and pulls in the background: the caller is a peer that only wants to
// know the request was accepted, and making it wait for a whole page of changes would
// turn a latency optimisation into a coupling. The pull gets its own context, because the
// request's is cancelled the moment the response is written.
//
// The caller must be a peer this node already knows: the signature proves it holds the
// cluster key, the registry proves it is part of this cluster. An unknown caller is
// refused instead of being turned into an outbound request, or a peer could make this
// node fetch from any URL it names.
func (s *SyncService) RequestPull(ctx context.Context, peerNodeID string) error {
	if !s.Enabled() {
		return ErrForbidden(i18n.CodeClusterDisabled, "synchronisation is not enabled on this node")
	}
	if strings.TrimSpace(peerNodeID) == "" {
		return ErrBadRequest(i18n.CodeValidation, "the request does not name the peer to pull from")
	}
	peer, ok, err := s.cluster.peerByID(ctx, peerNodeID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadRequest(i18n.CodeValidation, "unknown peer "+peerNodeID)
	}

	go func() {
		pullCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.PullPeer(pullCtx, peer); err != nil {
			s.log.Debug("push-triggered pull failed", "peer", peer.NodeID, "error", err)
		}
	}()
	return nil
}

// announceChanges tells the peers that this node has published something.
//
// It is opt-in (CLUSTER_SYNC_PUSH) and best effort: a failure is logged at debug and
// ignored, because the periodic pull still delivers the changes. The point is latency,
// never reliability — which is why pull stays the default: a node behind NAT needs no
// inbound access at all, and a push it cannot receive costs it nothing.
//
// It is driven by the outbox growing, not by the write paths: a write does not have to
// know that a peer exists, and a burst of edits produces one notification per cycle
// instead of one per edit.
func (s *SyncService) announceChanges(ctx context.Context, peers []models.Node) {
	var latest int64
	if err := s.db.WithContext(ctx).Model(&models.SyncOutbox{}).
		Select("COALESCE(MAX(id), 0)").Scan(&latest).Error; err != nil {
		s.log.Debug("could not read the outbox head", "error", err)
		return
	}

	s.pushMu.Lock()
	if latest <= s.lastAnnouncedID {
		s.pushMu.Unlock()
		return
	}
	s.lastAnnouncedID = latest
	s.pushMu.Unlock()

	for _, peer := range peers {
		go func(peer models.Node) {
			pushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.cluster.peerPost(pushCtx, peer, "/api/cluster/sync/now"); err != nil {
				s.log.Debug("could not ask a peer to pull", "peer", peer.NodeID, "error", err)
			}
		}(peer)
	}
	s.log.Debug("asked the peers to pull", "outbox_id", latest, "peers", len(peers))
}
