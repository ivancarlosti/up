package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/ivancarlosti/up/internal/models"
)

// storedTemplate is one template as this pass reads it: the id to update and the
// raw JSON column.
//
// The column is read raw on purpose. `models.MonitorTemplate.AfterFind` strips
// the credentials on every read (that is what keeps them out of the API and out
// of the apply path), so a pass that loaded rows through the model could never
// see what it is here to remove.
type storedTemplate struct {
	ID     uint
	Config []byte
}

// BackfillTemplateAuth removes the HTTP authentication a monitor template may
// still store from before templates became credential free.
//
// Why it is needed: the credentials used to live in the template configuration
// and travelled with it into every monitor created from the template, so a
// template row written by an older release can hold a username, a password or a
// bearer token. Authentication is a property of the MONITOR now (see
// models.MonitorConfig.WithoutAuth): reading such a row strips the fields in
// memory, but the secret would stay on disk (and in every backup) forever
// without this pass.
//
// It runs on every boot, right after the sync identity backfill, and is
// idempotent: a template that stores no credential is left byte for byte as it
// is (the statement only touches the rows that have something to remove). The
// revision is intentionally NOT advanced: this is not a new version of the row,
// it is the removal of a value that must never have been there, and every node
// runs the same pass on its own copy at boot — which is what keeps a cluster
// consistent without inventing a change nobody made.
func BackfillTemplateAuth(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	var rows []storedTemplate
	if err := db.WithContext(ctx).Raw("SELECT id, config FROM monitor_templates").Scan(&rows).Error; err != nil {
		return fmt.Errorf("reading the monitor templates: %w", err)
	}
	cleaned := 0
	for _, row := range rows {
		next, changed, err := stripTemplateAuth(row.Config)
		if err != nil {
			return fmt.Errorf("cleaning the config of template %d: %w", row.ID, err)
		}
		if !changed {
			continue
		}
		if err := db.WithContext(ctx).Exec(
			"UPDATE monitor_templates SET config = ? WHERE id = ?", string(next), row.ID).Error; err != nil {
			return fmt.Errorf("cleaning the config of template %d: %w", row.ID, err)
		}
		cleaned++
	}
	if cleaned == 0 {
		log.Debug("template auth backfill: nothing to do")
	} else {
		log.Info("template authentication removed from the monitor templates", "rows", cleaned)
	}
	return nil
}

// stripTemplateAuth returns the stored configuration without the credentials and
// whether anything had to be removed.
//
// It is pure so the rule can be tested without a database, and it is deliberately
// conservative about what counts as "something to remove": a configuration that
// already matches what the write path stores (auth_type "none" or absent, no
// user, no password, no token) comes back untouched, so the pass never rewrites a
// row it has nothing to say about.
func stripTemplateAuth(stored []byte) ([]byte, bool, error) {
	if len(stored) == 0 {
		// A row whose config column is NULL or empty: the model fills the
		// defaults on the next write, there is nothing to clean here.
		return stored, false, nil
	}
	var config models.MonitorConfig
	if err := json.Unmarshal(stored, &config); err != nil {
		return nil, false, fmt.Errorf("decoding the stored configuration: %w", err)
	}
	if !configHasAuth(config) {
		return stored, false, nil
	}
	clean, err := json.Marshal(config.WithoutAuth())
	if err != nil {
		return nil, false, fmt.Errorf("encoding the cleaned configuration: %w", err)
	}
	return clean, true, nil
}

// configHasAuth reports whether a stored configuration carries authentication
// (a credential, or an auth type that means one).
func configHasAuth(config models.MonitorConfig) bool {
	if config.BasicUser != "" || config.BasicPass != "" || config.BearerToken != "" {
		return true
	}
	return config.AuthType == "basic" || config.AuthType == "bearer"
}
