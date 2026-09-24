// Package services implements the business rules of Up (monitors, heartbeats,
// statistics, notifications, cluster, status pages, tokens and security).
//
// Handlers only validate HTTP input and translate errors into JSON; every
// decision lives here so it can be reused by the scheduler, the cluster worker
// and the public API.
package services

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
	"github.com/ivancarlosti/up/internal/utils"
)

// SettingService reads and writes the shared key/value configuration. Since
// the table lives in the external database, every node of the cluster sees the
// same values (session secret, cluster private key, defaults...).
type SettingService struct {
	db    *gorm.DB
	cfg   *config.Config
	log   *slog.Logger
	mu    sync.RWMutex
	cache map[string]string
}

// NewSettingService creates the service and loads the current values.
func NewSettingService(db *gorm.DB, cfg *config.Config, log *slog.Logger) *SettingService {
	svc := &SettingService{db: db, cfg: cfg, log: log, cache: map[string]string{}}
	ctx := context.Background()
	var rows []models.Setting
	if err := db.WithContext(ctx).Find(&rows).Error; err == nil {
		for _, row := range rows {
			svc.cache[row.Key] = row.Value
		}
	}
	return svc
}

// Get returns a setting value.
func (s *SettingService) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.cache[key]
	return value, ok
}

// GetOr returns the setting value or a fallback.
func (s *SettingService) GetOr(key, fallback string) string {
	if value, ok := s.Get(key); ok && value != "" {
		return value
	}
	return fallback
}

// Set upserts a setting (shared by every node).
func (s *SettingService) Set(ctx context.Context, key, value string) error {
	row := models.Setting{Key: key, Value: value}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = value
	s.mu.Unlock()
	return nil
}

// SetFromPeer applies a setting that came from another node, keeping the newer value.
//
// It is last-write-wins on `updated_at`, and it keeps the SENDER's timestamp: a plain Set
// would stamp the local clock, so the two nodes would then overwrite each other on every
// pass — each one always looking newer than the other. It reports whether the value was
// applied, and refreshes the cache the readers actually use (DefaultLocale, SessionSecret
// and the settings page all read it instead of the database).
func (s *SettingService) SetFromPeer(ctx context.Context, key, value string, updatedAt time.Time) (bool, error) {
	if strings.TrimSpace(key) == "" || updatedAt.IsZero() {
		return false, nil
	}

	var current models.Setting
	err := s.db.WithContext(ctx).Where("setting_key = ?", key).First(&current).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Never seen here: apply it.
	case err != nil:
		return false, ErrInternal(err)
	case !updatedAt.After(current.UpdatedAt):
		// Older or equal: this node already has the newer value, so nothing is written.
		// The cache is refreshed from the row just read anyway: the database is the
		// truth, and a write that bypassed this service (an operator, a migration, the
		// seed of another process) would otherwise leave this node serving the value it
		// booted with for ever — the sync finds "equal" and never touches the cache.
		s.mu.Lock()
		s.cache[key] = current.Value
		s.mu.Unlock()
		return false, nil
	}

	row := models.Setting{Key: key, Value: value, UpdatedAt: updatedAt}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "setting_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&row).Error; err != nil {
		return false, ErrInternal(err)
	}

	s.mu.Lock()
	s.cache[key] = value
	s.mu.Unlock()
	return true, nil
}

// All returns a copy of the settings map.
func (s *SettingService) All() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.cache))
	for k, v := range s.cache {
		out[k] = v
	}
	return out
}

// DefaultLocale is the language offered to first time visitors.
func (s *SettingService) DefaultLocale() string {
	return s.GetOr(models.SettingDefaultLocale, s.cfg.DefaultLocale)
}

// DefaultTheme is the theme offered to first time visitors.
func (s *SettingService) DefaultTheme() string {
	return s.GetOr(models.SettingDefaultTheme, s.cfg.DefaultTheme)
}

// TimeFormat returns the clock the UI renders (auto, 12h or 24h).
func (s *SettingService) TimeFormat() string {
	fallback := strings.ToLower(strings.TrimSpace(s.cfg.DefaultTimeFormat))
	if !containsString(config.SupportedTimeFormats, fallback) {
		fallback = config.TimeFormatAuto
	}
	value := s.GetOr(models.SettingTimeFormat, fallback)
	if !containsString(config.SupportedTimeFormats, value) {
		return config.TimeFormatAuto
	}
	return value
}

// SetDefaults stores the locale/theme/time-format defaults edited in
// Admin > Settings.
func (s *SettingService) SetDefaults(ctx context.Context, locale, theme, timeFormat string) error {
	if locale != "" {
		if !containsString(config.SupportedLocales, locale) {
			return errors.New("unsupported locale " + locale)
		}
		if err := s.Set(ctx, models.SettingDefaultLocale, locale); err != nil {
			return err
		}
	}
	if theme != "" {
		if !containsString(config.SupportedThemes, theme) {
			return errors.New("unsupported theme " + theme)
		}
		if err := s.Set(ctx, models.SettingDefaultTheme, theme); err != nil {
			return err
		}
	}
	if timeFormat != "" {
		if !containsString(config.SupportedTimeFormats, timeFormat) {
			return errors.New("unsupported time format " + timeFormat)
		}
		if err := s.Set(ctx, models.SettingTimeFormat, timeFormat); err != nil {
			return err
		}
	}
	return nil
}

// ExpirySettings returns the admin managed configuration of the daily expiry job
// (Admin > TLD/SSL expiration).
func (s *SettingService) ExpirySettings() models.ExpirySettings {
	out := models.DefaultExpirySettings()
	if value, ok := s.Get(models.SettingExpiryCheckTime); ok && strings.TrimSpace(value) != "" {
		out.CheckTime = strings.TrimSpace(value)
	}
	if value, ok := s.Get(models.SettingExpiryCheckTimezone); ok && strings.TrimSpace(value) != "" {
		out.CheckTimezone = strings.TrimSpace(value)
	}
	if value, ok := s.Get(models.SettingExpiryRDAPEnabled); ok {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			out.RDAPEnabled = parsed
		}
	}
	if value, ok := s.Get(models.SettingExpiryWHOISEnabled); ok {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			out.WHOISEnabled = parsed
		}
	}
	if value, ok := s.Get(models.SettingExpiryRateLimitMS); ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			out.RateLimitMS = parsed
		}
	}
	if value, ok := s.Get(models.SettingExpiryTimeoutSeconds); ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			out.TimeoutSeconds = parsed
		}
	}
	out.Normalize()
	return out
}

// SetExpirySettings stores the daily expiry job configuration.
func (s *SettingService) SetExpirySettings(ctx context.Context, settings models.ExpirySettings) error {
	settings.Normalize()
	stored := map[string]string{
		models.SettingExpiryCheckTime:      settings.CheckTime,
		models.SettingExpiryCheckTimezone:  settings.CheckTimezone,
		models.SettingExpiryRDAPEnabled:    strconv.FormatBool(settings.RDAPEnabled),
		models.SettingExpiryWHOISEnabled:   strconv.FormatBool(settings.WHOISEnabled),
		models.SettingExpiryRateLimitMS:    strconv.Itoa(settings.RateLimitMS),
		models.SettingExpiryTimeoutSeconds: strconv.Itoa(settings.TimeoutSeconds),
	}
	for key, value := range stored {
		if err := s.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

// ExpiryLastRunDay returns the day bucket of the last daily run (0 = never).
func (s *SettingService) ExpiryLastRunDay() int64 {
	value, ok := s.Get(models.SettingExpiryLastRunDay)
	if !ok {
		return 0
	}
	day, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return day
}

// SetExpiryLastRunDay records the day bucket of the last daily run.
func (s *SettingService) SetExpiryLastRunDay(ctx context.Context, day int64) error {
	return s.Set(ctx, models.SettingExpiryLastRunDay, strconv.FormatInt(day, 10))
}

// SessionSecret returns (generating it on the first boot) the secret used to
// sign session cookies and OIDC state values. It is stored in the database so
// every node of the cluster accepts the same cookie.
func (s *SettingService) SessionSecret(ctx context.Context) (string, error) {
	if value, ok := s.Get(models.SettingSessionSecret); ok && value != "" {
		return value, nil
	}
	secret, err := utils.RandomHex(32)
	if err != nil {
		return "", err
	}
	if err := s.Set(ctx, models.SettingSessionSecret, secret); err != nil {
		return "", err
	}
	s.log.Info("generated a new session secret (stored in the settings table)")
	return secret, nil
}

// ClusterPrivateKey returns the shared cluster key: CLUSTER_PRIVATE_KEY from
// the environment wins (and is persisted), otherwise the value stored in the
// settings table is used, and a fresh UUID is generated on the very first boot.
func (s *SettingService) ClusterPrivateKey(ctx context.Context) (string, error) {
	if s.cfg.ClusterPrivateKey != "" {
		if stored, ok := s.Get(models.SettingClusterPrivateKey); !ok || stored != s.cfg.ClusterPrivateKey {
			if err := s.Set(ctx, models.SettingClusterPrivateKey, s.cfg.ClusterPrivateKey); err != nil {
				return "", err
			}
		}
		return s.cfg.ClusterPrivateKey, nil
	}
	if value, ok := s.Get(models.SettingClusterPrivateKey); ok && value != "" {
		return value, nil
	}
	key := utils.NewUUID()
	if err := s.Set(ctx, models.SettingClusterPrivateKey, key); err != nil {
		return "", err
	}
	s.log.Info("generated a new cluster private key (visible in Admin > Cluster)")
	return key, nil
}

// RegenerateClusterPrivateKey replaces the shared key. Every other node must
// join again with the new value.
func (s *SettingService) RegenerateClusterPrivateKey(ctx context.Context) (string, error) {
	key := utils.NewUUID()
	if err := s.Set(ctx, models.SettingClusterPrivateKey, key); err != nil {
		return "", err
	}
	s.log.Warn("cluster private key regenerated; nodes must join again")
	return key, nil
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// normalizeLocale falls back to the configured default locale.
func normalizeLocale(locale, fallback string) string {
	locale = strings.TrimSpace(locale)
	if containsString(config.SupportedLocales, locale) {
		return locale
	}
	return fallback
}
