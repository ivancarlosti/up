package services

import (
	"context"
	"time"

	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
)

// settingsWhitelist is what CLUSTER_SYNC_SETTINGS carries: presentation only.
//
// The compatibility table of the design is explicit that API tokens, IP rules and the
// per-node rate limits are NEVER synchronised, because security configuration stays a
// per-node concern. The cluster key is missing for a different reason: it is exchanged at
// join, and a synced copy would let any node re-key the cluster or sign as another one.
var settingsWhitelist = []string{
	models.SettingAppName,
	models.SettingDefaultLocale,
	models.SettingDefaultTheme,
}

// ServeSettings answers GET /api/cluster/sync/settings.
//
// Each group is served only when its own opt-in is on: the whitelist under
// CLUSTER_SYNC_SETTINGS, the session secret under CLUSTER_SYNC_SESSION_SECRET (D4). A node
// with both off answers with an empty list rather than refusing, so a peer that asks does
// not treat the answer as an error.
func (s *SyncService) ServeSettings(ctx context.Context) (*models.SyncSettingsResponse, error) {
	if !s.Enabled() {
		return nil, ErrForbidden(i18n.CodeClusterDisabled, "synchronisation is not enabled on this node")
	}
	response := &models.SyncSettingsResponse{
		NodeID:          s.cfg.NodeID,
		ProtocolVersion: models.ProtocolVersion,
		ServerTime:      time.Now().UTC(),
		Settings:        []models.SettingPayload{},
	}

	keys := make([]string, 0, len(settingsWhitelist)+1)
	if s.cfg.SyncsSettings() {
		keys = append(keys, settingsWhitelist...)
	}
	if s.cfg.SyncsSessionSecret() {
		keys = append(keys, models.SettingSessionSecret)
	}
	if len(keys) == 0 {
		return response, nil
	}

	var rows []models.Setting
	if err := s.db.WithContext(ctx).Where("setting_key IN ?", keys).Find(&rows).Error; err != nil {
		return nil, ErrInternal(err)
	}
	for _, row := range rows {
		response.Settings = append(response.Settings, models.SettingPayload{
			Key:       row.Key,
			Value:     row.Value,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return response, nil
}

// PullSettings fetches the whitelisted settings of every peer and applies them.
//
// The merge is last-write-wins per key, which is what lets an operator edit the app name on
// any dashboard; the session secret converges the same way, with one consequence worth
// knowing: every node seeds its own secret at first boot, so the first sync picks the newer
// one and the node that loses is LOGGED OUT (its issued cookies stop verifying).
func (s *SyncService) PullSettings(ctx context.Context) {
	if !s.Enabled() || s.settings == nil {
		return
	}
	if !s.cfg.SyncsSettings() && !s.cfg.SyncsSessionSecret() {
		return
	}
	peers, err := s.cluster.peerTargets(ctx)
	if err != nil {
		s.log.Warn("could not list the peers to fetch settings from", "error", err)
		return
	}
	if len(peers) == 0 {
		return
	}

	for _, peer := range peers {
		response, err := s.fetchSettings(ctx, peer)
		if err != nil {
			s.log.Debug("could not fetch the settings of a peer", "peer", peer.NodeID, "error", err)
			continue
		}
		if response.ProtocolVersion != models.ProtocolVersion {
			s.log.Warn("a peer answers the settings endpoint with another protocol version",
				"peer", peer.NodeID, "protocol_version", response.ProtocolVersion)
			continue
		}
		for _, setting := range response.Settings {
			if !s.mayApplySetting(setting.Key) {
				continue
			}
			applied, err := s.settings.SetFromPeer(ctx, setting.Key, setting.Value, setting.UpdatedAt)
			if err != nil {
				s.log.Warn("could not store a setting from a peer",
					"peer", peer.NodeID, "key", setting.Key, "error", err)
				continue
			}
			if applied {
				s.log.Info("a setting was synchronised", "key", setting.Key, "peer", peer.NodeID)
			}
		}
	}
}

// mayApplySetting is the receiver-side gate: it refuses a key this node did not opt into,
// so a peer cannot push something the operator chose not to synchronise.
func (s *SyncService) mayApplySetting(key string) bool {
	for _, allowed := range settingsWhitelist {
		if key == allowed {
			return s.cfg.SyncsSettings()
		}
	}
	if key == models.SettingSessionSecret {
		return s.cfg.SyncsSessionSecret()
	}
	return false
}

// fetchSettings pulls the settings of one peer.
func (s *SyncService) fetchSettings(ctx context.Context, peer models.Node) (*models.SyncSettingsResponse, error) {
	var out models.SyncSettingsResponse
	if err := s.cluster.peerRequest(ctx, peer, "/api/cluster/sync/settings", "", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
