package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/beszel"
	"github.com/ivancarlosti/up/internal/i18n"
	"github.com/ivancarlosti/up/internal/models"
	"golang.org/x/crypto/ssh"
)

// maxAgentTokenLength is the longest token the agent protocol accepts (the hub
// side of beszel refuses anything above 64 bytes).
const maxAgentTokenLength = 64

// Authenticate resolves the agent handshake: the token presented in X-Token, or
// the stable error that says why it was refused.
//
// The token is looked up by its SHA-256, so the plaintext never has to be stored
// and a database dump cannot be replayed as a credential. A disabled system is
// refused even with a valid token: "disabled" means "stop collecting", and an
// agent that keeps reconnecting must not defeat it.
func (s *SystemService) Authenticate(ctx context.Context, token string) (*models.System, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > maxAgentTokenLength {
		return nil, ErrForbidden(i18n.CodeSystemAgentAuth, "the agent token is missing or too long")
	}
	var system models.System
	err := s.db.WithContext(ctx).Where("token_hash = ?", hashAgentToken(token)).First(&system).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrForbidden(i18n.CodeSystemAgentAuth, "unknown agent token")
		}
		return nil, ErrInternal(fmt.Errorf("reading the system of a token: %w", err))
	}
	if !system.Enabled {
		return nil, ErrForbidden(i18n.CodeSystemAgentAuth, "the system is disabled")
	}
	return &system, nil
}

// BindFingerprint stores the fingerprint of the machine behind a token, or
// refuses a machine that does not match it.
//
// The fingerprint is bound on the first successful handshake and checked on every
// later one. It is what stops a token copied from one machine from impersonating
// it on another: the credential may leak, the machine identity cannot be forged
// without the same KEY, the same data directory and the same host.
func (s *SystemService) BindFingerprint(ctx context.Context, system *models.System, fingerprint, hostname, agentVersion string) error {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return ErrForbidden(i18n.CodeSystemAgentAuth, "the agent reported no fingerprint")
	}
	now := timeNow()
	if system.Fingerprint == "" {
		columns := map[string]any{
			"fingerprint":   trimmed(fingerprint, 64),
			"last_seen_at":  now,
			"agent_version": trimmed(agentVersion, 32),
			"updated_at":    now,
			"revision":      gorm.Expr("revision + 1"),
		}
		if hostname != "" && system.Hostname == "" {
			columns["hostname"] = trimmed(hostname, 150)
		}
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.System{}).Where("id = ?", system.ID).Updates(columns).Error; err != nil {
				return ErrInternal(fmt.Errorf("binding the agent fingerprint: %w", err))
			}
			// The fingerprint is part of the configuration a peer needs (it is
			// what lets another node refuse the wrong machine), so it is published
			// like the rest of the row.
			return s.emit(ctx, tx, system.ID)
		})
	}
	if system.Fingerprint != fingerprint {
		return ErrForbidden(i18n.CodeSystemAgentAuth,
			"the agent fingerprint does not match the one this system was bound to")
	}
	// A reconnection refreshes the liveness and the version without touching the
	// revision: nothing an operator edits changed.
	if err := s.db.WithContext(ctx).Model(&models.System{}).Where("id = ?", system.ID).
		Updates(map[string]any{
			"last_seen_at":  now,
			"agent_version": trimmed(agentVersion, 32),
			"updated_at":    now,
		}).Error; err != nil {
		return ErrInternal(fmt.Errorf("refreshing the agent liveness: %w", err))
	}
	return nil
}

// Signer is the host key every agent of this installation was started with.
//
// It is derived from the shared cluster key (beszel.HostKey), so every node of a
// cluster signs with the same one and any node can take an agent over.
func (s *SystemService) Signer(ctx context.Context) (ssh.Signer, error) {
	if s.settings == nil {
		return nil, ErrInternal(errors.New("the settings service is not wired"))
	}
	clusterKey, err := s.settings.ClusterPrivateKey(ctx)
	if err != nil {
		return nil, ErrInternal(fmt.Errorf("reading the cluster key: %w", err))
	}
	signer, err := beszel.HostSigner(clusterKey)
	if err != nil {
		return nil, ErrInternal(err)
	}
	return signer, nil
}

// HostKey renders the KEY value to paste into the agent.
func (s *SystemService) HostKey(ctx context.Context) (string, error) {
	signer, err := s.Signer(ctx)
	if err != nil {
		return "", err
	}
	return beszel.HostPublicKey(signer), nil
}

// HostKeyInfo is the host key as the dialog shows it.
type HostKeyInfo struct {
	// Key is the authorized_keys line (the agent KEY variable).
	Key string `json:"key"`
	// Fingerprint identifies it in a log without copying the key.
	Fingerprint string `json:"fingerprint"`
	// MinimumVersion is the oldest agent this build accepts.
	MinimumVersion string `json:"minimum_version"`
}

// HostKeyInfo describes the host key and the accepted agent range.
func (s *SystemService) HostKeyInfo(ctx context.Context) (*HostKeyInfo, error) {
	signer, err := s.Signer(ctx)
	if err != nil {
		return nil, err
	}
	return &HostKeyInfo{
		Key:            beszel.HostPublicKey(signer),
		Fingerprint:    beszel.HostPublicKeyFingerprint(signer),
		MinimumVersion: beszel.MinimumAgentVersion,
	}, nil
}
