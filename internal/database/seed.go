package database

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ivancarlosti/up/internal/config"
	"github.com/ivancarlosti/up/internal/models"
)

// Seed creates the rows required for a first boot. It is idempotent: running it
// on every start only fills what is missing.
//
// Created/ensured here:
//   - the default locale and theme (from DEFAULT_LOCALE / DEFAULT_THEME);
//   - the session secret (used to sign session cookies and OIDC state);
//   - the cluster private key (from CLUSTER_PRIVATE_KEY or a fresh UUID);
//   - the single cluster_settings row with the behaviour rules;
//   - the row of the node running right now, with the primary flag.
func Seed(ctx context.Context, db *gorm.DB, cfg *config.Config, log *slog.Logger) error {
	defaults := map[string]string{
		models.SettingDefaultLocale: cfg.DefaultLocale,
		models.SettingDefaultTheme:  cfg.DefaultTheme,
		models.SettingAppName:       "Up",
		models.SettingRateLogin:     "0",
		models.SettingRatePublic:    "0",
	}
	for key, value := range defaults {
		if err := ensureSetting(ctx, db, key, value); err != nil {
			return err
		}
	}

	// Session secret: generated once and shared by every cluster node (the
	// settings table lives in the shared database).
	secret, err := ensureRandomSetting(ctx, db, models.SettingSessionSecret, 32)
	if err != nil {
		return err
	}
	log.Debug("session secret ready", "length", len(secret))

	// Cluster private key: CLUSTER_PRIVATE_KEY wins, otherwise a UUID is
	// generated on the very first boot and shown in Admin > Cluster.
	keyValue := cfg.ClusterPrivateKey
	if keyValue == "" {
		existing, found, err := readSetting(ctx, db, models.SettingClusterPrivateKey)
		if err != nil {
			return err
		}
		if found && existing != "" {
			keyValue = existing
		} else {
			keyValue = newUUID()
			log.Info("generated a new cluster private key (see Admin > Cluster)")
		}
	}
	if err := ensureSetting(ctx, db, models.SettingClusterPrivateKey, keyValue); err != nil {
		return err
	}

	// Cluster behaviour rules (single row, id = 1).
	var settingsCount int64
	if err := db.WithContext(ctx).Model(&models.ClusterSettings{}).Count(&settingsCount).Error; err != nil {
		return err
	}
	if settingsCount == 0 {
		if err := db.WithContext(ctx).Create(models.DefaultClusterSettings()).Error; err != nil {
			return err
		}
		log.Info("cluster settings initialised", "failure_strategy", models.FailureStrategyAllNodesFail)
	}

	// Node registration: every installation (even without clustering) has a
	// node row, which is what the heartbeats reference through node_id.
	//
	// The claim itself lives in ClaimSelf so that the boot path and the runtime
	// path (ClusterService.EnsureSelf) cannot disagree about who is primary.
	_, err = ClaimSelf(ctx, db, cfg, log)
	return err
}

// ensureSetting creates the setting when it does not exist yet.
func ensureSetting(ctx context.Context, db *gorm.DB, key, value string) error {
	row := models.Setting{Key: key, Value: value, UpdatedAt: time.Now().UTC()}
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

// ensureRandomSetting generates a random hex value once.
func ensureRandomSetting(ctx context.Context, db *gorm.DB, key string, bytes int) (string, error) {
	existing, found, err := readSetting(ctx, db, key)
	if err != nil {
		return "", err
	}
	if found && existing != "" {
		return existing, nil
	}
	value, err := randomHex(bytes)
	if err != nil {
		return "", err
	}
	if err := ensureSetting(ctx, db, key, value); err != nil {
		return "", err
	}
	return value, nil
}

// readSetting reads a single setting.
func readSetting(ctx context.Context, db *gorm.DB, key string) (string, bool, error) {
	var row models.Setting
	err := db.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.Value, true, nil
}
