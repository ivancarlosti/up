package services

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// SystemService owns the hardware monitored machines: the CRUD of the systems,
// the agent handshake, the ownership lease and the sample ingestion.
//
// It is the hub side of the Beszel protocol (internal/beszel): an agent dials
// this node, proves it holds the token of one system, and is then polled once a
// minute. The samples are stored here and read by the Systems pages.
type SystemService struct {
	db       *gorm.DB
	cfg      *config.Config
	log      *slog.Logger
	settings *SettingService
	cluster  *ClusterService
	hub      EventPublisher
	sync     *SyncEmitter
	// agents holds the live connections (system_agent.go).
	agents *agentRegistry
}

// NewSystemService builds the hardware monitoring service.
func NewSystemService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *SystemService {
	return &SystemService{db: db, cfg: cfg, log: log, agents: newAgentRegistry()}
}

// SetPublisher injects the real time publisher.
func (s *SystemService) SetPublisher(p EventPublisher) { s.hub = p }

// SetSyncEmitter injects the federated emitter (nil disables publishing).
func (s *SystemService) SetSyncEmitter(e *SyncEmitter) { s.sync = e }

// SetClusterService injects the cluster service: it decides whether this node may
// hold an agent connection, and how a peer is reached for the history of a system
// it owns.
func (s *SystemService) SetClusterService(c *ClusterService) { s.cluster = c }

// SetSettingService injects the settings store (retention policy).
func (s *SystemService) SetSettingService(settings *SettingService) { s.settings = settings }

// SystemInput is the editable part of a system.
type SystemInput struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Enabled *bool  `json:"enabled"`
}

// SystemView is a system as the API returns it: the stored row, decorated with
// the runtime state (status, owner node, live connection).
type SystemView struct {
	models.System
	// Token is only present in the answer that created the system or rotated its
	// token: it is shown once and never stored in clear.
	Token string `json:"token,omitempty"`
	// InstallCommand is the docker run line an operator pastes on the machine.
	InstallCommand string `json:"install_command,omitempty"`
	// AgentKey is the public key the agent must be started with (KEY).
	AgentKey string `json:"agent_key,omitempty"`
}

// agentTokenLength is the length of the generated token. 32 base64url characters
// carry 192 bits, well past guessing, and stay under the 64 byte limit the agent
// protocol imposes on the header.
const agentTokenLength = 32

// generateAgentToken returns a fresh token and its stored hash.
func generateAgentToken() (string, string, error) {
	buffer := make([]byte, agentTokenLength)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", fmt.Errorf("generating the agent token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)[:agentTokenLength]
	return token, utils.SHA256Hex(token), nil
}

// hashAgentToken is the lookup key of a presented token.
func hashAgentToken(token string) string { return utils.SHA256Hex(token) }

// tokenPrefix is the visible beginning of a token, for the UI.
func tokenPrefix(token string) string {
	if len(token) <= 6 {
		return token
	}
	return token[:6]
}
