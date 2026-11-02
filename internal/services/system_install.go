package services

import (
	"fmt"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/models"
)

// AgentImage is the container image of the agent the install command uses.
//
// The tag is pinned to the version this build was verified against: a floating
// "latest" would silently move the protocol every time the agent is restarted,
// and the operator would have no way to know which one is actually running.
const AgentImage = "henrygd/beszel-agent:0.21.1"

// InstallCommand renders the docker run line an operator pastes on the machine.
//
// It is generated from the values the agent needs (KEY, TOKEN, HUB_URL) so the
// dialog never asks the operator to copy three strings by hand - which is where a
// token gets mistyped and a system silently never appears.
func (s *SystemService) InstallCommand(host, token string) string {
	base := s.cfg.AppURL
	if base == "" {
		base = fmt.Sprintf("http://localhost:%d", s.cfg.AppPort)
	}
	return fmt.Sprintf(`docker run -d --name up-agent --restart unless-stopped \
  --network host \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /var/lib/beszel-agent:/var/lib/beszel-agent \
  -e KEY="%s" \
  -e TOKEN="%s" \
  -e HUB_URL="%s" \
  %s`, s.agentKeyForCommand(), token, base, AgentImage)
}

// agentKeyForCommand renders the KEY value without an error path.
//
// The key is derived from the cluster private key, which the boot seeded in the
// settings table; the command is a convenience string, so a missing key (an
// installation that never finished its boot) renders empty instead of turning the
// dialog into an error the operator cannot act on. The dialog also shows the key
// on its own line, with its own error path.
func (s *SystemService) agentKeyForCommand() string {
	if s.settings == nil {
		return ""
	}
	clusterKey, ok := s.settings.Get(models.SettingClusterPrivateKey)
	if !ok || clusterKey == "" {
		return ""
	}
	signer, err := beszel.HostSigner(clusterKey)
	if err != nil {
		return ""
	}
	return beszel.HostPublicKey(signer)
}
