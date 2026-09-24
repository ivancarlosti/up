package services

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// maxPullPagesPerPeer bounds how many batches one pull may chain, so a peer that
// keeps answering "has_more" cannot hold the loop forever.
const maxPullPagesPerPeer = 20

// SyncService owns the federated synchronisation: it serves this node's changes to
// its peers and applies what it pulls from them.
type SyncService struct {
	db      *gorm.DB
	cfg     *config.Config
	log     *slog.Logger
	cluster *ClusterService
	emitter *SyncEmitter
	hub     EventPublisher
	// settings is the local settings store, used by the opt-in settings sync. It may be
	// nil on a node that never wires it.
	settings *SettingService
	// pulls serialises the pulls of one peer (see pullGuard).
	pulls pullGuard
	// pushMu guards lastAnnouncedID, the highest outbox id this node has told its peers
	// about: the push is sent only when that number grows.
	pushMu          sync.Mutex
	lastAnnouncedID int64
}

// SetPublisher injects the real time publisher, so an applied change reaches the
// dashboards exactly like a local edit.
func (s *SyncService) SetPublisher(p EventPublisher) { s.hub = p }

// SetSettingsService injects the settings store for the opt-in settings sync. Without it
// that sync is a no-op, which keeps every other deployment unchanged.
func (s *SyncService) SetSettingsService(settings *SettingService) { s.settings = settings }

// publish forwards an event to the WebSocket hub when one is wired.
func (s *SyncService) publish(event string, payload any) {
	if s.hub != nil {
		s.hub.Publish(event, payload)
	}
}

// NewSyncService builds the synchronisation service.
//
// It warns at boot when the delivery channels take part in the synchronised
// configuration but the peers are not reached over TLS. The channel payload
// carries credentials, so that combination deserves to be said out loud even
// though it is allowed (a lab, an internal network).
func NewSyncService(db *gorm.DB, cfg *config.Config, log *slog.Logger, cluster *ClusterService, emitter *SyncEmitter) *SyncService {
	service := &SyncService{db: db, cfg: cfg, log: log, cluster: cluster, emitter: emitter}
	if cfg.SyncsNotifications() && !cfg.SyncsOverTLS() {
		log.Warn("the delivery channels are synchronised over a non-TLS URL: their credentials cross the network readable",
			"app_url", cfg.AppURL)
	}
	// The opt-in is honoured, and it is said out loud: an unverified peer certificate is
	// exactly the kind of setting that is convenient in a lab and dangerous in production.
	if cfg.ClusterInsecureSkipVerify {
		log.Warn("CLUSTER_INSECURE_SKIP_VERIFY is on: the peer TLS certificate is not verified, " +
			"so the cluster key and the channel credentials can be intercepted by anyone on the path")
	}
	return service
}

// Enabled reports whether this node takes part in the synchronisation.
func (s *SyncService) Enabled() bool { return s.cfg.SyncEnabled() }

// PruneOutbox removes the outbox rows a peer can no longer need.
//
// The tombstones in sync_objects are deliberately NOT pruned: they are what stops
// a re-delivered old upsert from recreating a deleted row, and expiring them would
// make that guarantee time limited. A peer whose cursor points before the pruned
// region reconciles through the manifest and the snapshots instead of through the
// outbox, which is what makes the prune safe.
func (s *SyncService) PruneOutbox(ctx context.Context, before time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("created_at < ?", before).Delete(&models.SyncOutbox{})
	if result.Error != nil {
		return 0, ErrInternal(result.Error)
	}
	return result.RowsAffected, nil
}

// PruneSyncHistory removes the conflict log and the dead letters older than the
// retention.
//
// Unlike the tombstones in sync_objects, which are kept because they are what stops a
// re-delivered old upsert from recreating a deleted row, a conflict or a dead letter is
// HISTORY: worth keeping long enough for an operator to see what happened, and not
// worth keeping for ever.
//
// A pending link is NOT pruned here. An unresolved reference is not history but an
// open problem, it stops being retried on its own past the attempt cap, and the report
// counts it separately so it cannot be forgotten.
func (s *SyncService) PruneSyncHistory(ctx context.Context, before time.Time) (int64, error) {
	conflicts := s.db.WithContext(ctx).Where("detected_at < ?", before).Delete(&models.SyncConflict{})
	if conflicts.Error != nil {
		return 0, ErrInternal(conflicts.Error)
	}
	letters := s.db.WithContext(ctx).Where("updated_at < ?", before).Delete(&models.SyncDeadLetter{})
	if letters.Error != nil {
		return 0, ErrInternal(letters.Error)
	}
	return conflicts.RowsAffected + letters.RowsAffected, nil
}

// pullGuard serialises the pulls of one peer.
//
// Three paths can want the same peer at once: the periodic loop, an inbound push
// notification, and a healing pass. Two concurrent pulls of one peer would interleave
// their cursors — one advancing to 12 while the other writes 7 afterwards — which either
// re-applies changes or skips them. The design promises one in-flight pull per peer.
type pullGuard struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lock takes the per-peer lock and returns its release function.
func (g *pullGuard) lock(nodeID string) func() {
	g.mu.Lock()
	if g.locks == nil {
		g.locks = make(map[string]*sync.Mutex)
	}
	lock, ok := g.locks[nodeID]
	if !ok {
		lock = &sync.Mutex{}
		g.locks[nodeID] = lock
	}
	g.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}
func (s *SyncService) NextSyncTick() time.Duration {
	return time.Duration(s.cfg.ClusterSyncSeconds) * time.Second
}

// syncedEntities lists the entities this node publishes, in a stable order.
func (s *SyncService) syncedEntities() []string {
	out := make([]string, 0, len(models.SyncedEntities()))
	for _, entity := range models.SyncedEntities() {
		if s.cfg.SyncEntityEnabled(entity) {
			out = append(out, entity)
		}
	}
	return out
}

// ServeChanges answers GET /api/cluster/sync/changes: the outbox rows a peer has
// not seen yet, in id order.
func (s *SyncService) ServeChanges(ctx context.Context, since int64, limit int) (*models.SyncChangesResponse, error) {
	if !s.Enabled() {
		return nil, ErrForbidden(i18n.CodeClusterDisabled, "synchronisation is not enabled on this node")
	}
	response := &models.SyncChangesResponse{
		Changes:         []models.SyncChangePayload{},
		NextSince:       since,
		ProtocolVersion: models.ProtocolVersion,
	}
	entities := s.syncedEntities()
	if len(entities) == 0 {
		return response, nil
	}
	if limit <= 0 || limit > s.cfg.ClusterSyncBatch {
		limit = s.cfg.ClusterSyncBatch
	}

	if err := s.markCursorExpired(ctx, since, entities, response); err != nil {
		return nil, err
	}

	var rows []models.SyncOutbox
	if err := s.db.WithContext(ctx).
		Where("id > ?", since).
		Where("entity IN ?", entities).
		Order("id ASC").
		Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	// One row past the limit is fetched on purpose: it answers has_more without a
	// second query.
	if len(rows) > limit {
		response.HasMore = true
		rows = rows[:limit]
	}
	for _, row := range rows {
		response.Changes = append(response.Changes, models.OutboxToChange(row))
		response.NextSince = int64(row.ID)
		if row.Revision > response.LatestRevision {
			response.LatestRevision = row.Revision
		}
	}
	return response, nil
}

// markCursorExpired flags a cursor that points before the oldest row still kept,
// which is what the tombstone prune leaves behind. Serving the surviving rows to
// such a caller would silently skip the pruned ones forever, so it has to fall
// back to a snapshot instead.
func (s *SyncService) markCursorExpired(ctx context.Context, since int64, entities []string, response *models.SyncChangesResponse) error {
	if since <= 0 {
		return nil
	}
	var oldest struct{ ID int64 }
	if err := s.db.WithContext(ctx).Model(&models.SyncOutbox{}).
		Select("COALESCE(MIN(id), 0) AS id").
		Where("entity IN ?", entities).Scan(&oldest).Error; err != nil {
		return ErrInternal(err)
	}
	response.CursorExpired = models.CursorExpired(oldest.ID, since)
	return nil
}

// PullPeers pulls the changes of every peer. The peers are handled concurrently,
// one request at a time each, so a silent peer cannot delay the others.
func (s *SyncService) PullPeers(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	peers, err := s.cluster.peerTargets(ctx)
	if err != nil {
		s.log.Warn("could not list the peers to synchronise with", "error", err)
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
			// A failure here is also a liveness fact: it is recorded so the peer
			// table shows why the synchronisation stopped.
			if err := s.PullPeer(ctx, peer); err != nil {
				s.cluster.recordPeerFailure(ctx, peer.NodeID, err)
				return
			}
			// The healing pass: it notices what the incremental pull missed, which
			// is why it runs on the same loop.
			if s.manifestDue(ctx, peer.NodeID) {
				if err := s.reconcilePeer(ctx, peer); err != nil {
					s.cluster.recordPeerFailure(ctx, peer.NodeID, err)
					return
				}
			}
			// A relation whose target had not arrived is worth one bounded retry.
			if err := s.retryPendingLinks(ctx, peer); err != nil {
				s.cluster.recordPeerFailure(ctx, peer.NodeID, err)
			}
		}(peer)
	}
	wg.Wait()

	// The opt-in settings sync rides on the same loop: settings change rarely, so the
	// pull cadence is the right one for them.
	if s.cfg.SyncsSettings() || s.cfg.SyncsSessionSecret() {
		s.PullSettings(ctx)
	}

	// Push: tell the peers that this node has published something, so they pull now
	// instead of waiting a whole interval. Opt-in, and a no-op when the outbox has not
	// grown since the last cycle.
	if s.cfg.ClusterSyncPush {
		s.announceChanges(ctx, peers)
	}
}

// PullPeer applies everything a peer has published since this node's cursor.
func (s *SyncService) PullPeer(ctx context.Context, peer models.Node) error {
	// One in-flight pull per peer: the periodic loop, an inbound push and a healing pass
	// can all want the same peer at once, and two of them would interleave cursors.
	release := s.pulls.lock(peer.NodeID)
	defer release()

	cursor := s.cluster.loadPeerRow(ctx, peer.NodeID).LastChangeID

	for page := 0; page < maxPullPagesPerPeer; page++ {
		batch, err := s.fetchChanges(ctx, peer, cursor)
		if err != nil {
			return err
		}
		if batch.ProtocolVersion != models.ProtocolVersion {
			return fmt.Errorf("peer speaks protocol %d, this node speaks %d",
				batch.ProtocolVersion, models.ProtocolVersion)
		}
		if batch.CursorExpired {
			if err := s.healPeer(ctx, peer); err != nil {
				return err
			}
			// The cursor cannot stay where it is: it points at rows the peer has
			// pruned, so every later cycle would expire again and the node would
			// re-run the whole manifest pass for ever. The snapshots have just
			// reconciled the state, so the position may jump to the head of what
			// this same response describes — anything published after it has a
			// higher id and is pulled normally.
			return s.realignCursor(ctx, peer.NodeID, batch.NextSince)
		}
		if len(batch.Changes) == 0 {
			return nil
		}
		if err := s.ApplyBatch(ctx, peer.NodeID, batch.Changes); err != nil {
			return err
		}
		cursor = batch.NextSince
		if !batch.HasMore {
			return nil
		}
	}
	s.log.Warn("stopped pulling after the page cap: the peer has more changes than one cycle should carry",
		"peer", peer.NodeID, "pages", maxPullPagesPerPeer)
	return nil
}

// fetchChanges pulls one batch from a peer.
func (s *SyncService) fetchChanges(ctx context.Context, peer models.Node, since int64) (*models.SyncChangesResponse, error) {
	query := url.Values{}
	query.Set("since", strconv.FormatInt(since, 10))
	query.Set("limit", strconv.Itoa(s.cfg.ClusterSyncBatch))

	var out models.SyncChangesResponse
	if err := s.cluster.peerRequest(ctx, peer, "/api/cluster/sync/changes", query.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}
